package controllers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/attachmentstore"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	previewutil "github.com/aoagents/agent-orchestrator/backend/internal/preview"
	"github.com/aoagents/agent-orchestrator/backend/internal/previewserver"
	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
	usagesvc "github.com/aoagents/agent-orchestrator/backend/internal/service/usage"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
	"github.com/aoagents/agent-orchestrator/backend/internal/workspacewatch"
)

const (
	maxPromptLen      = 16 << 10
	maxMessageLen     = 4096
	maxModelLen       = 256
	maxDisplayNameLen = 100
	maxIdempotencyKey = 128

	// Agent-authored handoffs are deliberately bounded. Deterministic AO
	// context is stored separately and does not need to be repeated here.
	maxAgentHandoffBodyBytes = 256 << 10
	maxAgentHandoffBytes     = 64 << 10

	// Attachment limits guard the daemon against oversized spawn bodies. Files
	// are pasted/dropped into the task brief and inlined as base64 in the JSON
	// body, so the caps are deliberately conservative.
	maxAttachments      = 8
	maxAttachmentBytes  = attachmentstore.MaxFileBytes // 50 MiB per file, decoded
	maxAttachmentsBytes = 100 << 20                    // 100 MiB total, decoded
	// maxSpawnBodyBytes bounds the raw request body before it is decoded. The
	// per-attachment and total caps above only apply after the whole body is
	// materialized, so without this an oversized body (base64 inflates the
	// decoded total by ~4/3) would allocate in full first. Derived from the
	// decoded total plus headroom for the prompt and JSON envelope.
	maxSpawnBodyBytes = maxAttachmentsBytes*4/3 + (2 << 20)
	// maxSendBodyBytes is the /send equivalent of maxSpawnBodyBytes, sized off
	// maxAttachmentBytes (one attachment) rather than maxAttachmentsBytes
	// (spawn's up-to-eight): unlike spawn, a send carries at most one inline
	// attachment.
	maxSendBodyBytes = maxAttachmentBytes*4/3 + (2 << 20)
)

// blockedAttachmentMimes contains MIME types that are explicitly rejected for
// security reasons. SVG is excluded because it is XML that can carry active
// content (scripts/external entities).
var blockedAttachmentMimes = map[string]bool{
	"image/svg+xml": true,
}

var (
	errPreviewFileNotFound         = errors.New("preview file not found")
	errPreviewFileOutsideWorkspace = errors.New("preview file is outside the session workspace")
)

// SessionService is the controller-facing session service contract.
type SessionService interface {
	List(ctx context.Context, filter sessionsvc.ListFilter) ([]domain.Session, error)
	Spawn(ctx context.Context, cfg ports.SpawnConfig) (domain.Session, int, int, error)
	SpawnOrchestrator(ctx context.Context, projectID domain.ProjectID, clean bool, requestedMode domain.SessionMode, approval domain.PermissionMode) (domain.Session, error)
	Get(ctx context.Context, id domain.SessionID) (domain.Session, error)
	Restore(ctx context.Context, id domain.SessionID) (sessionsvc.RestoreOutcome, error)
	ExitAgent(ctx context.Context, id domain.SessionID) (sessionsvc.ExitAgentOutcome, error)
	ResumeAgent(ctx context.Context, id domain.SessionID) (sessionsvc.ResumeAgentOutcome, error)
	SwitchAgent(ctx context.Context, id domain.SessionID, in sessionsvc.SwitchAgentInput) (domain.AgentSwitch, error)
	RecoverAgentSwitch(ctx context.Context, id domain.SessionID, switchID domain.AgentSwitchID) (domain.AgentSwitch, error)
	ListAgentSwitches(ctx context.Context, id domain.SessionID) ([]domain.AgentSwitch, error)
	SubmitAgentHandoff(ctx context.Context, id domain.SessionID, switchID domain.AgentSwitchID, sourceGenerationID domain.AgentGenerationID, handoff json.RawMessage) (domain.AgentSwitch, error)
	RequestKill(ctx context.Context, id domain.SessionID) (sessionmanager.KillResult, error)
	RollbackSpawn(ctx context.Context, id domain.SessionID) (sessionsvc.RollbackOutcome, error)
	Cleanup(ctx context.Context, project domain.ProjectID) (sessionsvc.CleanupOutcome, error)
	Rename(ctx context.Context, id domain.SessionID, displayName string) error
	SetPreview(ctx context.Context, id domain.SessionID, previewURL string) (domain.Session, error)
	SetTerminateOnPRMerge(ctx context.Context, id domain.SessionID, terminate bool) (domain.Session, error)
	SetAutoInjectReview(ctx context.Context, id domain.SessionID, autoInject bool) (domain.Session, error)
	SetAutoInjectCI(ctx context.Context, id domain.SessionID, autoInject bool) (domain.Session, error)
	SetReviewerHarness(ctx context.Context, id domain.SessionID, harness domain.ReviewerHarness, config domain.AgentConfig) (domain.Session, error)
	SetAutoReview(ctx context.Context, id domain.SessionID, enabled bool) (domain.Session, error)
	Send(ctx context.Context, id domain.SessionID, message string, attachment *ports.SpawnAttachment) error
	DelegateTask(ctx context.Context, in sessionsvc.DelegateTaskInput) (sessionsvc.DelegateTaskOutcome, error)
	PrepareTask(ctx context.Context, projectID domain.ProjectID) (string, error)
	CancelTaskPreparation(ctx context.Context, token string) error
	ListPRListing(ctx context.Context, id domain.SessionID) (sessionsvc.PRListing, error)
	ClaimPR(ctx context.Context, id domain.SessionID, ref string, opts sessionsvc.ClaimPROptions) (sessionsvc.ClaimPRResult, error)
	StageAttachments(ctx context.Context, id domain.SessionID, attachments []ports.SpawnAttachment) ([]string, error)
	WorkspaceWatchPaths(ctx context.Context, id domain.SessionID) ([]string, error)
	GetWorkspaceManifest(ctx context.Context, id domain.SessionID) (sessionsvc.WorkspaceManifest, error)
	RefreshWorkspaceManifest(ctx context.Context, id domain.SessionID) (sessionsvc.WorkspaceManifest, error)
	ReconcileWorkspaceManifest(ctx context.Context, id domain.SessionID) (sessionsvc.WorkspaceManifest, error)
	GetWorkspaceHistory(ctx context.Context, id domain.SessionID) (sessionsvc.WorkspaceHistory, error)
	ListWorkspaceFiles(ctx context.Context, id domain.SessionID) (sessionsvc.WorkspaceFiles, error)
	GetWorkspaceFile(ctx context.Context, id domain.SessionID, path string, section sessionsvc.WorkspaceFileSection) (sessionsvc.WorkspaceFileDetail, error)
	GetWorkspaceFileAtCommit(ctx context.Context, id domain.SessionID, path, commitSHA string) (sessionsvc.WorkspaceFileDetail, error)
	UpdateWorkspaceFile(ctx context.Context, id domain.SessionID, input sessionsvc.UpdateWorkspaceFileInput) (sessionsvc.WorkspaceFileDetail, error)
	ListPRFiles(ctx context.Context, id domain.SessionID, number int, sourceURL string) (sessionsvc.PRFiles, error)
	GetPRFile(ctx context.Context, id domain.SessionID, number int, sourceURL, path string, previousPath *string) (sessionsvc.WorkspaceFileDetail, error)
	GetPRFileAtCommit(ctx context.Context, id domain.SessionID, number int, sourceURL, path, commitSHA string) (sessionsvc.WorkspaceFileDetail, error)
	GetPRFileRevision(ctx context.Context, id domain.SessionID, number int, sourceURL, path string, side sessionsvc.WorkspaceFileBlobSide) (sessionsvc.WorkspaceFileRevision, error)
	GetPRFileRevisionAtCommit(ctx context.Context, id domain.SessionID, number int, sourceURL, path string, side sessionsvc.WorkspaceFileBlobSide, commitSHA string) (sessionsvc.WorkspaceFileRevision, error)
	GetWorkspaceFileBlob(ctx context.Context, id domain.SessionID, path string, side sessionsvc.WorkspaceFileBlobSide) (sessionsvc.WorkspaceFileBlob, error)
	GetWorkspaceDiffs(ctx context.Context, id domain.SessionID, input sessionsvc.WorkspaceDiffInput) (sessionsvc.WorkspaceDiffs, error)
	GetWorkspaceFileRevision(ctx context.Context, id domain.SessionID, path string, scope sessionsvc.WorkspaceDiffScope, side sessionsvc.WorkspaceFileBlobSide, workspaceVersion, expectedRevision string) (sessionsvc.WorkspaceFileRevision, error)
	GetWorkspaceFileRevisionAtCommit(ctx context.Context, id domain.SessionID, path string, side sessionsvc.WorkspaceFileBlobSide, workspaceVersion, expectedRevision, commitSHA string) (sessionsvc.WorkspaceFileRevision, error)
	SearchWorkspaceFiles(ctx context.Context, id domain.SessionID, query, cursor string, limit int) (sessionsvc.WorkspaceFileSearch, error)
	ListWorkspaceTree(ctx context.Context, id domain.SessionID, path string) (sessionsvc.WorkspaceTree, error)
	InvalidateWorkspaceCache(id domain.SessionID)
	Pin(ctx context.Context, id domain.SessionID) (domain.Session, error)
	Unpin(ctx context.Context, id domain.SessionID) (domain.Session, error)
}

type sessionMessageOptionsSender interface {
	SendWithOptions(ctx context.Context, id domain.SessionID, message string, attachment *ports.SpawnAttachment, options ports.MessageDeliveryOptions) error
}

// ActivityRecorder applies an agent activity-state signal to a session. It is
// satisfied directly by *lifecycle.Manager: an activity signal is a pure
// lifecycle reduction (no runtime/workspace teardown), so it bypasses
// SessionService rather than threading a no-op passthrough through the session
// manager.
type ActivityRecorder interface {
	ApplyActivitySignal(ctx context.Context, id domain.SessionID, s ports.ActivitySignal) error
}

// ManagedPreviewServer is the deterministic server lifecycle attached to a
// worker. It is separate from static file rendering and browser automation.
type ManagedPreviewServer interface {
	Start(ctx context.Context, sessionID domain.SessionID, workspacePath, configurationName string, projectEnv map[string]string) (previewserver.Status, error)
	Stop(ctx context.Context, sessionID domain.SessionID) (previewserver.Status, error)
	Status(sessionID domain.SessionID) previewserver.Status
}

// SessionCapabilityValidator verifies the daemon-issued token injected only
// into the owning worker session.
type SessionCapabilityValidator interface {
	Valid(sessionID domain.SessionID, token, verifier string) bool
}

// ShellPreviewCapabilityValidator checks the separate, preview-only bearer
// held by a live session-scoped user shell.
type ShellPreviewCapabilityValidator interface {
	ValidPreviewCapability(ctx context.Context, sessionID domain.SessionID, token string) (bool, error)
}

// UsageHookRecorder consumes transcript metadata from the same native hook
// callback without changing activity-state semantics.
type UsageHookRecorder interface {
	RecordHook(ctx context.Context, id domain.SessionID, signal usagesvc.HookSignal) error
}

// SessionsController owns the session routes. Nil keeps routes registered but
// returns OpenAPI-backed 501s.
type SessionsController struct {
	Svc      SessionService
	Projects interface {
		Get(context.Context, domain.ProjectID) (projectsvc.GetResult, error)
	}
	Activity                 ActivityRecorder
	NativeSessions           ports.AgentNativeSessionResolver
	DataDir                  string
	Usage                    UsageHookRecorder
	Attachments              *attachmentstore.Store
	PreviewServer            ManagedPreviewServer
	Capabilities             SessionCapabilityValidator
	ShellPreviewCapabilities ShellPreviewCapabilityValidator
}

// Register mounts the session routes on the supplied router.
func (c *SessionsController) Register(r chi.Router) {
	r.Get("/sessions", c.list)
	r.Post("/sessions", c.spawn)
	r.Post("/sessions/cleanup", c.cleanup)
	r.Get("/sessions/{sessionId}", c.get)
	r.Get("/sessions/{sessionId}/preview", c.preview)
	r.Post("/sessions/{sessionId}/preview", c.setPreview)
	r.Delete("/sessions/{sessionId}/preview", c.clearPreview)
	r.Get("/sessions/{sessionId}/preview/server", c.previewServerStatus)
	r.Post("/sessions/{sessionId}/preview/server", c.startPreviewServer)
	r.Delete("/sessions/{sessionId}/preview/server", c.stopPreviewServer)
	r.Get("/sessions/{sessionId}/preview/files/*", c.previewFile)
	r.Get("/sessions/{sessionId}/artifact-files/*", c.artifactFile)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		r.Method(method, "/sessions/{sessionId}/preview/app/*", http.HandlerFunc(c.previewApp))
	}
	r.Post("/sessions/{sessionId}/attachments", c.stageAttachments)
	r.Get("/sessions/{sessionId}/workspace/manifest", c.getWorkspaceManifest)
	r.Get("/sessions/{sessionId}/workspace/history", c.getWorkspaceHistory)
	r.Get("/sessions/{sessionId}/workspace/files", c.listWorkspaceFiles)
	r.Get("/sessions/{sessionId}/workspace/file", c.getWorkspaceFile)
	r.Put("/sessions/{sessionId}/workspace/file", c.updateWorkspaceFile)
	r.Get("/sessions/{sessionId}/workspace/file/blob", c.getWorkspaceFileBlob)
	r.Post("/sessions/{sessionId}/workspace/diffs", c.getWorkspaceDiffs)
	r.Get("/sessions/{sessionId}/workspace/file/revision", c.getWorkspaceFileRevision)
	r.Get("/sessions/{sessionId}/workspace/search", c.searchWorkspaceFiles)
	r.Get("/sessions/{sessionId}/workspace/tree", c.listWorkspaceTree)
	r.Get("/sessions/{sessionId}/pr/{prNumber}/files", c.listPRFiles)
	r.Get("/sessions/{sessionId}/pr/{prNumber}/file", c.getPRFile)
	r.Get("/sessions/{sessionId}/pr/{prNumber}/file/revision", c.getPRFileRevision)
	r.Get("/sessions/{sessionId}/pr", c.listPRs)
	r.Post("/sessions/{sessionId}/pr/claim", c.claimPR)
	r.Patch("/sessions/{sessionId}", c.rename)
	r.Patch("/sessions/{sessionId}/merge-policy", c.setMergePolicy)
	r.Patch("/sessions/{sessionId}/auto-inject-review", c.setAutoInjectReviewPolicy)
	r.Patch("/sessions/{sessionId}/auto-inject-ci", c.setAutoInjectCIPolicy)
	r.Put("/sessions/{sessionId}/reviewer", c.setReviewer)
	r.Put("/sessions/{sessionId}/auto-review", c.setAutoReview)
	r.Post("/sessions/{sessionId}/restore", c.restore)
	r.Post("/sessions/{sessionId}/exit-agent", c.exitAgent)
	r.Post("/sessions/{sessionId}/resume-agent", c.resumeAgent)
	r.Post("/sessions/{sessionId}/switch-agent", c.switchAgent)
	r.Get("/sessions/{sessionId}/agent-switches", c.listAgentSwitches)
	r.Post("/sessions/{sessionId}/agent-switches/{switchId}/recover", c.recoverAgentSwitch)
	r.Post("/sessions/{sessionId}/agent-switches/{switchId}/handoff", c.submitAgentHandoff)
	r.Get("/sessions/{sessionId}/interface-transition", c.interfaceTransitionStatus)
	r.Post("/sessions/{sessionId}/interface-transition", c.startInterfaceTransition)
	r.Delete("/sessions/{sessionId}/interface-transition", c.cancelInterfaceTransition)
	r.Put("/sessions/{sessionId}/interface-transition/{transitionId}/notice-acknowledgement", c.acknowledgeInterfaceTransitionNotice)
	r.Post("/sessions/{sessionId}/kill", c.kill)
	r.Post("/sessions/{sessionId}/rollback", c.rollback)
	r.Post("/sessions/{sessionId}/send", c.send)
	r.Post("/sessions/{sessionId}/activity", c.activity)
	r.Post("/sessions/{sessionId}/activity/codewhale", c.codewhaleActivity)
	r.Post("/sessions/{sessionId}/pin", c.pin)
	r.Delete("/sessions/{sessionId}/pin", c.unpin)
	r.Get("/orchestrators", c.listOrchestrators)
	r.Post("/orchestrators", c.spawnOrchestrator)
	r.Post("/orchestrators/delegate", c.delegateTask)
	r.Post("/projects/{id}/tasks/prepare", c.prepareTask)
	r.Delete("/task-preparations/{token}", c.cancelTaskPreparation)
	r.Get("/orchestrators/{id}", c.getOrchestrator)
}

// RegisterStreams mounts long-lived session streams outside the REST timeout
// middleware. Worktree notifications remain active only while a client is
// actually viewing that session's files.
func (c *SessionsController) RegisterStreams(r chi.Router) {
	r.Get("/sessions/{sessionId}/workspace/events", c.streamWorkspaceChanges)
}

func (c *SessionsController) list(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/sessions")
		return
	}
	filter, err := parseSessionListFilter(r)
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_QUERY", err.Error(), nil)
		return
	}
	sessions, err := c.Svc.List(r.Context(), filter)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ListSessionsResponse{Sessions: sessionViews(r, sessions)})
}

func (c *SessionsController) spawn(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions")
		return
	}
	// Bound the body before decoding: this route is served on the LAN listener
	// (AO Mobile), not just loopback, and the attachment caps only run after the
	// whole body is decoded. MaxBytesReader stops the read past the limit so an
	// oversized base64 payload can't allocate in full first.
	r.Body = http.MaxBytesReader(w, r.Body, maxSpawnBodyBytes)
	var in SpawnSessionRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	if !validClientRequestID(in.ClientRequestID) {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", "INVALID_CLIENT_REQUEST_ID", "clientRequestId must be 1-128 visible characters", nil)
		return
	}
	mode, err := domain.ParseSessionMode(string(in.Mode))
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", "SESSION_MODE_INVALID", err.Error(), nil)
		return
	}
	in.Mode = mode
	if !in.ApprovalMode.Valid() {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_APPROVAL_MODE", "approvalMode is invalid", nil)
		return
	}
	if len(in.Prompt) > maxPromptLen {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "PROMPT_TOO_LONG", "Prompt must be 16 KiB or fewer", nil)
		return
	}
	// displayName is optional at the API (the desktop new-task dialog omits it
	// and the read model falls back to the session id). `ao spawn` makes it
	// required CLI-side. When present, it is held to the same length cap here so
	// a direct API call cannot exceed it.
	displayName := strings.TrimSpace(in.DisplayName)
	if utf8.RuneCountInString(displayName) > maxDisplayNameLen {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "DISPLAY_NAME_TOO_LONG", fmt.Sprintf("displayName must be %d characters or fewer", maxDisplayNameLen), nil)
		return
	}
	if in.Kind == "" {
		in.Kind = domain.KindWorker
	}
	attachments, attachErr := decodeSpawnAttachments(in.Attachments)
	if attachErr != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", attachErr.code, attachErr.message, nil)
		return
	}
	requestHash := ""
	if in.ClientRequestID != "" {
		keyless := in
		keyless.ClientRequestID = ""
		keyless.Attachments = nil
		requestHash = clientRequestFingerprint("session", keyless, attachments)
	}
	sess, promptBytes, systemPromptBytes, err := c.Svc.Spawn(r.Context(), ports.SpawnConfig{AccountID: in.AccountID, ProjectID: in.ProjectID, IssueID: in.IssueID, ParentSessionID: in.ParentSessionID, TrackerProvider: in.TrackerProvider, Kind: in.Kind, Harness: in.Harness, Branch: in.Branch, RequestedMode: in.Mode, Prompt: in.Prompt, DisplayName: displayName, Attachments: attachments, AgentConfig: ports.AgentConfig{Model: in.Model, Effort: in.Effort, Permissions: in.ApprovalMode}, ClientRequestID: in.ClientRequestID, ClientRequestHash: requestHash})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, SpawnSessionResponse{Session: sessionView(r, sess), PromptBytes: promptBytes, SystemPromptBytes: systemPromptBytes})
}

// attachmentError carries a client-facing API error code + message for a
// rejected attachment payload.
type attachmentError struct {
	code    string
	message string
}

// extensionForMimeType returns a file extension for a MIME type. For known
// MIME types, it returns a standard extension. For unknown types, it attempts
// to extract a reasonable extension from the MIME type, or falls back to ".bin".
func extensionForMimeType(mimeType string) string {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))

	// Preferred extensions for MIME types with multiple options
	preferredExts := map[string]string{
		"image/jpeg":      ".jpg",
		"image/jpg":       ".jpg",
		"text/plain":      ".txt",
		"video/quicktime": ".mov",
	}

	// Check if we have a preferred extension for this MIME type
	if pref, ok := preferredExts[mimeType]; ok {
		return pref
	}

	// Try the standard mime package
	exts, err := mime.ExtensionsByType(mimeType)
	if err == nil && len(exts) > 0 {
		// Prefer common extensions when available
		for _, ext := range exts {
			switch ext {
			case ".jpg", ".jpeg", ".png", ".gif", ".pdf", ".txt", ".json", ".xml", ".html", ".css", ".js", ".md", ".zip", ".tar", ".gz":
				return ext
			}
		}
		return exts[0] // Return the primary extension if no preferred match
	}

	// Fallback: extract from the MIME type itself
	// e.g., "application/pdf" -> ".pdf", "text/plain" -> ".plain"
	parts := strings.SplitN(mimeType, "/", 2)
	if len(parts) == 2 {
		subtype := parts[1]
		// Handle common suffixes like "+xml", "+json"
		if idx := strings.Index(subtype, "+"); idx >= 0 {
			subtype = subtype[:idx]
		}
		return "." + subtype
	}

	// Ultimate fallback for unknown MIME types
	return ".bin"
}

// decodeAttachment validates and base64-decodes a single inline file
// attachment shared by spawn, delegate, stage, send, and chat requests, enforcing
// the blocked-MIME-type rule and the caller's per-file cap. Callers handling multiple
// attachments are responsible for the count and total-size caps.
func decodeAttachment(a AttachmentInput, maxBytes int) (ports.SpawnAttachment, *attachmentError) {
	mimeType := strings.ToLower(strings.TrimSpace(a.MimeType))
	if blockedAttachmentMimes[mimeType] {
		return ports.SpawnAttachment{}, &attachmentError{"UNSUPPORTED_ATTACHMENT_TYPE", "unsupported attachment type"}
	}
	ext := extensionForMimeType(mimeType)
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(a.Data))
	if err != nil {
		return ports.SpawnAttachment{}, &attachmentError{"INVALID_ATTACHMENT_DATA", "attachment data is not valid base64"}
	}
	if len(data) == 0 {
		return ports.SpawnAttachment{}, &attachmentError{"INVALID_ATTACHMENT_DATA", "attachment is empty"}
	}
	if len(data) > maxBytes {
		return ports.SpawnAttachment{}, &attachmentError{"ATTACHMENT_TOO_LARGE", "attachment is too large"}
	}
	return ports.SpawnAttachment{Ext: ext, Data: data}, nil
}

func decodeSpawnAttachments(in []AttachmentInput) ([]ports.SpawnAttachment, *attachmentError) {
	return decodeAttachments(in, maxAttachmentBytes, maxAttachmentsBytes)
}

// decodeAttachments enforces the caller's count and size caps, accepting any
// MIME type except explicitly blocked ones (e.g., SVG).
func decodeAttachments(in []AttachmentInput, maxFileBytes, maxTotalBytes int) ([]ports.SpawnAttachment, *attachmentError) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > maxAttachments {
		return nil, &attachmentError{"TOO_MANY_ATTACHMENTS", "too many attachments"}
	}
	out := make([]ports.SpawnAttachment, 0, len(in))
	total := 0
	for _, a := range in {
		attachment, err := decodeAttachment(a, maxFileBytes)
		if err != nil {
			return nil, err
		}
		total += len(attachment.Data)
		if total > maxTotalBytes {
			return nil, &attachmentError{"ATTACHMENTS_TOO_LARGE", "attachments are too large"}
		}
		out = append(out, attachment)
	}
	return out, nil
}

func (c *SessionsController) get(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/sessions/{sessionId}")
		return
	}
	sess, err := c.Svc.Get(r.Context(), sessionID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, SessionResponse{Session: sessionView(r, sess)})
}

func (c *SessionsController) preview(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/sessions/{sessionId}/preview")
		return
	}
	sess, err := c.Svc.Get(r.Context(), sessionID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	entry, ok := discoverPreviewEntry(sess.Metadata.WorkspacePath)
	res := SessionPreviewResponse{SessionID: sessionID(r)}
	if ok {
		res.Entry = entry
		res.PreviewURL, err = previewFileURL(r, sessionID(r), entry)
		if err != nil {
			writePreviewResolveError(w, r, err)
			return
		}
	}
	envelope.WriteJSON(w, http.StatusOK, res)
}

func (c *SessionsController) previewFile(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/sessions/{sessionId}/preview/files/*")
		return
	}
	sess, err := c.Svc.Get(r.Context(), sessionID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	assetPath := chi.URLParam(r, "*")
	if r.URL.Query().Get("source") == "artifact" {
		c.serveRootedPreviewFile(w, r, sess.Metadata.ArtifactDir, assetPath)
		return
	}
	if name, ok := attachmentstore.NameFromWorkspacePath(assetPath); ok && c.Attachments != nil {
		file, info, openErr := c.Attachments.Open(r.Context(), sess.ID, name)
		if openErr == nil {
			defer func() { _ = file.Close() }()
			serveOpenedPreviewFile(w, r, file, info, assetPath)
			return
		}
	}
	// __ao_artifacts__/ is a reserved marker AO itself prepends when it builds
	// an artifact preview link (see ArtifactEntryPath), but it was a valid
	// workspace-relative path before artifact previews existed. A real
	// workspace file at that exact path must keep winning, so only route to
	// the artifact directory when no such workspace file actually exists.
	if rel, ok := previewutil.ArtifactEntryRelative(assetPath); ok {
		if _, existsInWorkspace := previewutil.EntryAtPath(sess.Metadata.WorkspacePath, assetPath); !existsInWorkspace {
			c.serveRootedPreviewFile(w, r, sess.Metadata.ArtifactDir, rel)
			return
		}
	}
	c.serveRootedPreviewFile(w, r, sess.Metadata.WorkspacePath, assetPath)
}

// previewApp reaches only the running, session-owned managed preview process.
// The selected URL comes from the manager, never from a caller-supplied port.
func (c *SessionsController) previewApp(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil || c.PreviewServer == nil {
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "PREVIEW_NOT_FOUND", "Preview not found", nil)
		return
	}
	sess, err := c.Svc.Get(r.Context(), sessionID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	status := c.PreviewServer.Status(sess.ID)
	target, err := url.Parse(status.URL)
	if err != nil || status.State != previewserver.StateReady || status.TargetKind != previewserver.TargetApp ||
		status.URL != sess.Metadata.PreviewURL || target == nil || target.Scheme != "http" ||
		!isPreviewLoopback(target.Hostname()) || target.User != nil || target.Port() == "" ||
		strconv.Itoa(status.Port) != target.Port() || sess.IsTerminated {
		w.Header().Set("X-AO-Preview-Managed-Required", "1")
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "PREVIEW_MANAGED_REQUIRED", "Run ao preview start in this session to open a remote live preview", nil)
		return
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(req *httputil.ProxyRequest) {
			appAuthorization := req.Out.Header.Get("X-AO-Preview-App-Authorization")
			hadPreviewOrigin := req.Out.Header.Get("X-AO-Preview-App-Origin") != ""
			req.SetURL(target)
			req.Out.Host = target.Host
			req.Out.URL.Path = "/" + strings.TrimPrefix(chi.URLParam(r, "*"), "/")
			req.Out.URL.RawPath = ""
			req.Out.URL.RawQuery = req.In.URL.RawQuery
			// AO credentials authenticate the tunnel, not the agent-run web app.
			req.Out.Header.Del("Authorization")
			req.Out.Header.Del("Proxy-Authorization")
			req.Out.Header.Del("X-AO-Preview-App-Authorization")
			req.Out.Header.Del("X-AO-Preview-App-Origin")
			req.Out.Header.Del("Origin")
			if appAuthorization != "" {
				req.Out.Header.Set("Authorization", appAuthorization)
			}
			if hadPreviewOrigin {
				req.Out.Header.Set("Origin", target.Scheme+"://"+target.Host)
			}
			req.Out.Header.Del(browserCapabilityHeader)
			cookies := req.Out.Cookies()
			req.Out.Header.Del("Cookie")
			for _, cookie := range cookies {
				if cookie.Name != "ao_conn" {
					req.Out.AddCookie(cookie)
				}
			}
		},
		ModifyResponse: func(res *http.Response) error {
			if location := res.Header.Get("Location"); location != "" {
				if redirect, parseErr := url.Parse(location); parseErr == nil && redirect.IsAbs() && isPreviewLoopback(redirect.Hostname()) {
					if redirect.Port() != target.Port() {
						return errors.New("preview redirect to another local port")
					}
					res.Header.Set("Location", redirect.RequestURI())
				}
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "Preview target unavailable or redirected to another local port", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

// inlineArtifactOrigin serves the session's artifact directory on its inline
// origin, the one the chat thread frames an HTML artifact from: the request
// path is the artifact path, files only, read-only, in the inline sandbox.
func (c *SessionsController) inlineArtifactOrigin(w http.ResponseWriter, r *http.Request, id domain.SessionID) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		envelope.WriteAPIError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "METHOD_NOT_ALLOWED",
			r.Method+" not allowed on an artifact origin", nil)
		return
	}
	if c.Svc == nil {
		writeArtifactFileNotFound(w, r)
		return
	}
	// The session read fills in the default artifact directory for a row
	// stored without one.
	sess, err := c.Svc.Get(r.Context(), id)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	asset := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	serveArtifactFile(w, r, inlineArtifactContentSecurityPolicy, sess.Metadata.ArtifactDir, asset)
}

func isPreviewLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// PreviewOrigin serves a workspace preview from its isolated *.localhost
// origin. It returns false when the request host is not a preview origin so the
// daemon router can continue handling its normal API and control surfaces.
//
// The selected entry's directory is mounted at the origin root. For example,
// dist/index.html is reachable at both /dist/ (the persisted URL) and /, while
// /assets/app.css maps to dist/assets/app.css. This mirrors a production static
// server and fixes root-relative URLs without rewriting user-generated files.
func (c *SessionsController) PreviewOrigin(w http.ResponseWriter, r *http.Request) bool {
	if id, ok := previewutil.SessionIDFromInlineArtifactHost(r.Host); ok {
		c.inlineArtifactOrigin(w, r, id)
		return true
	}
	id, artifactOrigin := previewutil.SessionIDFromArtifactHost(r.Host)
	if !artifactOrigin {
		var ok bool
		id, ok = previewutil.SessionIDFromHost(r.Host)
		if !ok {
			return false
		}
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		envelope.WriteAPIError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "METHOD_NOT_ALLOWED",
			r.Method+" not allowed on preview origin", nil)
		return true
	}
	if c.Svc == nil {
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "PREVIEW_NOT_FOUND", "Preview not found", nil)
		return true
	}
	sess, err := c.Svc.Get(r.Context(), id)
	if err != nil {
		envelope.WriteError(w, r, err)
		return true
	}
	if artifactOrigin {
		// The host alone declares scope on this origin, so the request path
		// is served verbatim from ArtifactDir: there is no marker to strip
		// and nothing a real workspace path could collide with.
		asset := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		c.serveRootedPreviewFile(w, r, sess.Metadata.ArtifactDir, asset)
		return true
	}
	entry, ok := previewOriginEntry(sess, r.URL.Path)
	if !ok {
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "NO_PREVIEW_ENTRY", "No preview entry point found in session workspace", nil)
		return true
	}
	switch entry.Scope {
	case previewutil.StoredEntryScopeArtifact:
		asset := previewOriginArtifactAssetPath(entry.Path, r.URL.Path)
		c.serveRootedPreviewFile(w, r, sess.Metadata.ArtifactDir, asset)
	default:
		asset := previewOriginAssetPath(entry.Path, r.URL.Path)
		c.serveRootedPreviewFile(w, r, sess.Metadata.WorkspacePath, asset)
	}
	return true
}

func previewOriginEntry(sess domain.Session, requestPath string) (previewutil.StoredEntry, bool) {
	requested := strings.TrimPrefix(path.Clean("/"+requestPath), "/")
	if rel, ok := previewutil.ArtifactEntryRelative(requested); ok {
		// __ao_artifacts__/ is a reserved marker AO itself prepends when it
		// builds an artifact preview link (see ArtifactEntryPath), but it was
		// a valid workspace-relative path before artifact previews existed.
		// A real workspace file at that exact literal request path must keep
		// winning over the artifact directory, mirroring the same collision
		// guard below for the stored-PreviewURL path and previewFile's guard
		// for the legacy /preview/files route.
		if _, existsInWorkspace := previewutil.EntryAtPath(sess.Metadata.WorkspacePath, requested); existsInWorkspace {
			return previewutil.StoredEntry{Scope: previewutil.StoredEntryScopeWorkspace, Path: requested}, true
		}
		if _, exists := previewutil.EntryAtPath(sess.Metadata.ArtifactDir, rel); exists {
			return previewutil.StoredEntry{Scope: previewutil.StoredEntryScopeArtifact, Path: rel}, true
		}
	}
	if entry, ok := previewutil.StoredEntryFromPreview(sess.Metadata.PreviewURL, sess.ID); ok {
		switch entry.Scope {
		case previewutil.StoredEntryScopeArtifact:
			// __ao_artifacts__/ is a reserved marker AO itself prepends when
			// it builds an artifact preview link, but it was a valid
			// workspace-relative path before artifact previews existed. A
			// real workspace file at that exact literal path must keep
			// winning over the artifact directory, mirroring previewFile's
			// same collision guard for the legacy /preview/files route.
			if literal, ok := previewutil.ArtifactEntryPath(entry.Path); ok {
				if _, existsInWorkspace := previewutil.EntryAtPath(sess.Metadata.WorkspacePath, literal); existsInWorkspace {
					return previewutil.StoredEntry{Scope: previewutil.StoredEntryScopeWorkspace, Path: literal}, true
				}
			}
			if _, exists := previewutil.EntryAtPath(sess.Metadata.ArtifactDir, entry.Path); exists {
				return entry, true
			}
		default:
			if _, exists := previewutil.EntryAtPath(sess.Metadata.WorkspacePath, entry.Path); exists {
				return entry, true
			}
		}
	}
	if entry, ok := discoverPreviewEntry(sess.Metadata.WorkspacePath); ok {
		return previewutil.StoredEntry{Scope: previewutil.StoredEntryScopeWorkspace, Path: entry}, true
	}
	return previewutil.StoredEntry{}, false
}

func previewOriginAssetPath(entry, requestPath string) string {
	requested := strings.TrimPrefix(path.Clean("/"+requestPath), "/")
	if requested == "" || requested == "." {
		return entry
	}
	root := path.Dir(entry)
	if root == "." {
		return requested
	}
	if requested == root {
		return entry
	}
	requested = strings.TrimPrefix(requested, root+"/")
	return path.Join(root, requested)
}

func previewOriginArtifactAssetPath(entry, requestPath string) string {
	requested := strings.TrimPrefix(path.Clean("/"+requestPath), "/")
	if rel, ok := previewutil.ArtifactEntryRelative(requested); ok {
		return previewOriginAssetPath(entry, rel)
	}
	return previewOriginAssetPath(entry, requestPath)
}

// serveWorkspacePreviewFile is the single serving path for both the legacy API
// route and isolated preview origins. OpenRoot keeps symlink traversal and the
// subsequent read on the same workspace-confined file handle.
func (c *SessionsController) serveRootedPreviewFile(w http.ResponseWriter, r *http.Request, rootPath, assetPath string) {
	file, info, clean, err := previewutil.OpenWorkspaceFile(rootPath, assetPath)
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "PREVIEW_FILE_NOT_FOUND", "Preview file not found", nil)
		return
	}
	defer func() { _ = file.Close() }()
	serveOpenedPreviewFile(w, r, file, info, clean)
}

func serveOpenedPreviewFile(w http.ResponseWriter, r *http.Request, file *os.File, info fs.FileInfo, clean string) {
	if !previewutil.IsMarkdownPath(clean) || previewRawRequested(r) {
		http.ServeContent(w, r, info.Name(), info.ModTime(), file)
		return
	}

	source, err := io.ReadAll(file)
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "PREVIEW_FILE_NOT_FOUND", "Preview file not found", nil)
		return
	}
	rendered, err := previewutil.RenderMarkdown(source, filepath.Base(clean))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method != http.MethodHead {
		_, _ = w.Write(rendered) //nolint:gosec // G705: preview content is workspace-local and agent-trusted
	}
}

func previewRawRequested(r *http.Request) bool {
	raw := r.URL.Query().Get("raw")
	if raw == "" {
		return false
	}
	enabled, err := strconv.ParseBool(raw)
	return err == nil && enabled
}

func (c *SessionsController) listWorkspaceFiles(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/sessions/{sessionId}/workspace/files")
		return
	}
	files, err := c.Svc.ListWorkspaceFiles(r.Context(), sessionID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, workspaceFilesResponse(files))
}

func (c *SessionsController) getWorkspaceManifest(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/sessions/{sessionId}/workspace/manifest")
		return
	}
	manifest, err := c.Svc.GetWorkspaceManifest(r.Context(), sessionID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, workspaceManifestResponse(manifest))
}

func (c *SessionsController) getWorkspaceHistory(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/sessions/{sessionId}/workspace/history")
		return
	}
	history, err := c.Svc.GetWorkspaceHistory(r.Context(), sessionID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, workspaceHistoryResponse(history))
}

func (c *SessionsController) getWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/sessions/{sessionId}/workspace/file")
		return
	}
	relPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if relPath == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "WORKSPACE_PATH_REQUIRED", "path is required", nil)
		return
	}
	section := sessionsvc.WorkspaceFileSection(strings.TrimSpace(r.URL.Query().Get("section")))
	commitSHA := strings.TrimSpace(r.URL.Query().Get("commitSha"))
	var file sessionsvc.WorkspaceFileDetail
	var err error
	if commitSHA != "" {
		file, err = c.Svc.GetWorkspaceFileAtCommit(r.Context(), sessionID(r), relPath, commitSHA)
	} else {
		file, err = c.Svc.GetWorkspaceFile(r.Context(), sessionID(r), relPath, section)
	}
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, workspaceFileResponse(file))
}

func (c *SessionsController) updateWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "PUT", "/api/v1/sessions/{sessionId}/workspace/file")
		return
	}
	var in UpdateWorkspaceFileRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	if strings.TrimSpace(in.Path) == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "WORKSPACE_PATH_REQUIRED", "path is required", nil)
		return
	}
	file, err := c.Svc.UpdateWorkspaceFile(r.Context(), sessionID(r), sessionsvc.UpdateWorkspaceFileInput{
		Path:                    in.Path,
		Content:                 in.Content,
		ExpectedFileFingerprint: strings.TrimSpace(in.ExpectedFileFingerprint),
	})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, workspaceFileResponse(file))
}

func (c *SessionsController) listPRFiles(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/sessions/{sessionId}/pr/{prNumber}/files")
		return
	}
	number, err := strconv.Atoi(chi.URLParam(r, "prNumber"))
	if err != nil || number <= 0 {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_PR_NUMBER", "prNumber must be a positive integer", nil)
		return
	}
	files, err := c.Svc.ListPRFiles(r.Context(), sessionID(r), number, strings.TrimSpace(r.URL.Query().Get("sourceUrl")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, prFilesResponse(files))
}

func (c *SessionsController) getPRFile(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/sessions/{sessionId}/pr/{prNumber}/file")
		return
	}
	number, err := strconv.Atoi(chi.URLParam(r, "prNumber"))
	if err != nil || number <= 0 {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_PR_NUMBER", "prNumber must be a positive integer", nil)
		return
	}
	relPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if relPath == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "WORKSPACE_PATH_REQUIRED", "path is required", nil)
		return
	}
	query := r.URL.Query()
	var previousPath *string
	if query.Has("previousPath") {
		value := strings.TrimSpace(query.Get("previousPath"))
		previousPath = &value
	}
	var file sessionsvc.WorkspaceFileDetail
	if commitSHA := strings.TrimSpace(query.Get("commitSha")); commitSHA != "" {
		file, err = c.Svc.GetPRFileAtCommit(r.Context(), sessionID(r), number, strings.TrimSpace(query.Get("sourceUrl")), relPath, commitSHA)
	} else {
		file, err = c.Svc.GetPRFile(r.Context(), sessionID(r), number, strings.TrimSpace(query.Get("sourceUrl")), relPath, previousPath)
	}
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, workspaceFileResponse(file))
}

func (c *SessionsController) getPRFileRevision(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/sessions/{sessionId}/pr/{prNumber}/file/revision")
		return
	}
	number, err := strconv.Atoi(chi.URLParam(r, "prNumber"))
	if err != nil || number <= 0 {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_PR_NUMBER", "prNumber must be a positive integer", nil)
		return
	}
	query := r.URL.Query()
	relPath := strings.TrimSpace(query.Get("path"))
	if relPath == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "WORKSPACE_PATH_REQUIRED", "path is required", nil)
		return
	}
	side := sessionsvc.WorkspaceFileBlobSide(strings.TrimSpace(query.Get("side")))
	if side == "" {
		side = sessionsvc.WorkspaceBlobAfter
	}
	var revision sessionsvc.WorkspaceFileRevision
	if commitSHA := strings.TrimSpace(query.Get("commitSha")); commitSHA != "" {
		revision, err = c.Svc.GetPRFileRevisionAtCommit(r.Context(), sessionID(r), number, strings.TrimSpace(query.Get("sourceUrl")), relPath, side, commitSHA)
	} else {
		revision, err = c.Svc.GetPRFileRevision(r.Context(), sessionID(r), number, strings.TrimSpace(query.Get("sourceUrl")), relPath, side)
	}
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	envelope.WriteJSON(w, http.StatusOK, workspaceFileRevisionResponse(revision))
}

func (c *SessionsController) getWorkspaceDiffs(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/workspace/diffs")
		return
	}
	var in WorkspaceDiffRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	diffs, err := c.Svc.GetWorkspaceDiffs(r.Context(), sessionID(r), sessionsvc.WorkspaceDiffInput{
		Scope:            sessionsvc.WorkspaceDiffScope(in.Scope),
		Paths:            in.Paths,
		ContextLines:     in.ContextLines,
		IgnoreWhitespace: in.IgnoreWhitespace,
		WorkspaceVersion: in.WorkspaceVersion,
		CommitSHA:        strings.TrimSpace(in.CommitSHA),
	})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, workspaceDiffsResponse(diffs))
}

func (c *SessionsController) getWorkspaceFileRevision(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/sessions/{sessionId}/workspace/file/revision")
		return
	}
	query := r.URL.Query()
	relPath := strings.TrimSpace(query.Get("path"))
	if relPath == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "WORKSPACE_PATH_REQUIRED", "path is required", nil)
		return
	}
	side := sessionsvc.WorkspaceFileBlobSide(strings.TrimSpace(query.Get("side")))
	if side == "" {
		side = sessionsvc.WorkspaceBlobAfter
	}
	commitSHA := strings.TrimSpace(query.Get("commitSha"))
	var revision sessionsvc.WorkspaceFileRevision
	var err error
	if commitSHA != "" {
		revision, err = c.Svc.GetWorkspaceFileRevisionAtCommit(r.Context(), sessionID(r), relPath, side, strings.TrimSpace(query.Get("workspaceVersion")), strings.TrimSpace(query.Get("expectedRevision")), commitSHA)
	} else {
		revision, err = c.Svc.GetWorkspaceFileRevision(r.Context(), sessionID(r), relPath, sessionsvc.WorkspaceDiffScope(strings.TrimSpace(query.Get("scope"))), side, strings.TrimSpace(query.Get("workspaceVersion")), strings.TrimSpace(query.Get("expectedRevision")))
	}
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	envelope.WriteJSON(w, http.StatusOK, workspaceFileRevisionResponse(revision))
}

func (c *SessionsController) searchWorkspaceFiles(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/sessions/{sessionId}/workspace/search")
		return
	}
	query := r.URL.Query()
	limit := 0
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_WORKSPACE_SEARCH_LIMIT", "limit is invalid", nil)
			return
		}
		limit = parsed
	}
	result, err := c.Svc.SearchWorkspaceFiles(r.Context(), sessionID(r), query.Get("query"), query.Get("cursor"), limit)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, workspaceFileSearchResponse(result))
}

// listWorkspaceTree returns one directory level of the session workspace's
// full file tree, git-status decorated. Unlike listWorkspaceFiles (changed
// files only), path is optional: an empty or missing path lists the
// workspace root.
func (c *SessionsController) listWorkspaceTree(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/sessions/{sessionId}/workspace/tree")
		return
	}
	relPath := strings.TrimSpace(r.URL.Query().Get("path"))
	tree, err := c.Svc.ListWorkspaceTree(r.Context(), sessionID(r), relPath)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, workspaceTreeResponse(tree))
}

// getWorkspaceFileBlob streams one side of an image file's diff. The renderer
// points an <img> straight at this route, so the response is the raw bytes with
// their media type rather than a JSON envelope.
func (c *SessionsController) getWorkspaceFileBlob(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/sessions/{sessionId}/workspace/file/blob")
		return
	}
	relPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if relPath == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "WORKSPACE_PATH_REQUIRED", "path is required", nil)
		return
	}
	side := sessionsvc.WorkspaceFileBlobSide(strings.TrimSpace(r.URL.Query().Get("side")))
	if side == "" {
		side = sessionsvc.WorkspaceBlobAfter
	}
	blob, err := c.Svc.GetWorkspaceFileBlob(r.Context(), sessionID(r), relPath, side)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", blob.MediaType)
	h.Set("Content-Length", strconv.Itoa(len(blob.Data)))
	// The worktree changes under the viewer, and the URL carries no content
	// hash, so a cached response would show a stale image after every edit.
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(blob.Data)
}

// branchStateReconciler refreshes a session's persisted branch facts.
type branchStateReconciler interface {
	ReconcileSessionBranchState(ctx context.Context, id domain.SessionID) error
}

func (c *SessionsController) streamWorkspaceChanges(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/sessions/{sessionId}/workspace/events")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal", "SSE_UNSUPPORTED", "Streaming is not supported by this server", nil)
		return
	}
	paths, err := c.Svc.WorkspaceWatchPaths(r.Context(), sessionID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	changes, err := workspacewatch.Watch(r.Context(), paths...)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	// The watcher cannot report edits made before it existed. Reconcile both
	// workspace caches after the watch is installed and before telling the client
	// it can stop fallback polling.
	if _, err := c.Svc.ReconcileWorkspaceManifest(r.Context(), sessionID(r)); err != nil {
		envelope.WriteError(w, r, err)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprint(w, "event: ready\ndata: {}\n\n"); err != nil {
		return
	}
	flusher.Flush()

	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case _, ok := <-changes:
			if !ok {
				return
			}
			c.Svc.InvalidateWorkspaceCache(sessionID(r))
			payload := struct {
				Kind             string `json:"kind"`
				WorkspaceVersion string `json:"workspaceVersion,omitempty"`
				Refreshing       bool   `json:"refreshing"`
				Overflow         bool   `json:"overflow"`
			}{Kind: "dirty", Refreshing: true, Overflow: true}
			data, _ := json.Marshal(payload)
			if _, err := fmt.Fprintf(w, "event: workspace_changed\ndata: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
			if manifest, refreshErr := c.Svc.RefreshWorkspaceManifest(r.Context(), sessionID(r)); refreshErr == nil {
				payload.Kind = "version"
				payload.WorkspaceVersion = manifest.WorkspaceVersion
				payload.Refreshing = false
				data, _ = json.Marshal(payload)
				if _, err := fmt.Fprintf(w, "event: workspace_changed\ndata: %s\n\n", data); err != nil {
					return
				}
				flusher.Flush()
			}
			// The same watch sees commits and pushes. Branch facts refresh after
			// the file list so a commit never delays it, and their change
			// streams to every client as session_updated.
			if reconciler, ok := c.Svc.(branchStateReconciler); ok {
				_ = reconciler.ReconcileSessionBranchState(r.Context(), sessionID(r))
			}
		case <-keepAlive.C:
			if _, err := fmt.Fprint(w, "event: heartbeat\ndata: {}\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// setPreview persists the browser preview URL the desktop app opens for a
// session and fans out a session_updated CDC event so the dashboard's browser
// panel reacts live. The target is resolved as follows:
//
//   - An empty url opens the workspace's static entry point (index.html and
//     friends), falling back to the session's existing preview target only
//     when no entry point exists.
//   - An explicit workspace-local path (e.g. `index.html`, `./dist/index.html`)
//     is served through the preview/files route so local files load.
//   - Absolute paths and file: URLs are accepted only when their fully resolved
//     target remains inside the fully resolved session workspace. They use the
//     workspace-confined preview origin rather than an automatable file: URL.
//   - External http(s) URLs and host:port dev servers are kept verbatim.
//
// Every call bumps the session's preview revision, so re-running `ao preview`
// with the same target still refreshes the panel.
func (c *SessionsController) setPreview(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/preview")
		return
	}
	var in SetSessionPreviewRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	// Get first so a missing session is rejected with the normal 404 before any
	// write, and so autodetect/local resolution has the workspace path to probe.
	sess, err := c.Svc.Get(r.Context(), sessionID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	// Passive preview intentionally accepts an explicit external URL or an
	// existing workspace-local file. Arbitrary local files are rejected by every
	// browser surface because the target remains available to session automation.
	// Managed process execution uses the separately capability-protected
	// /preview/server route.
	previewURL := strings.TrimSpace(in.URL)
	if previewURL == "" {
		if entry, ok := discoverPreviewEntry(sess.Metadata.WorkspacePath); ok {
			previewURL, err = previewFileURL(r, sessionID(r), entry)
			if err != nil {
				writePreviewResolveError(w, r, err)
				return
			}
		} else if existing := strings.TrimSpace(sess.Metadata.PreviewURL); existing != "" {
			var resolveErr error
			previewURL, resolveErr = resolvePreviewTarget(r, sessionID(r), sess.Metadata.WorkspacePath, existing, false)
			if resolveErr != nil {
				writePreviewResolveError(w, r, resolveErr)
				return
			}
		} else {
			envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "NO_PREVIEW_ENTRY", "No preview entry point found in session workspace", nil)
			return
		}
	} else {
		var resolveErr error
		previewURL, resolveErr = resolvePreviewTarget(r, sessionID(r), sess.Metadata.WorkspacePath, previewURL, in.RequireWorkspaceFile)
		if resolveErr != nil {
			writePreviewResolveError(w, r, resolveErr)
			return
		}
	}
	updated, err := c.Svc.SetPreview(r.Context(), sessionID(r), previewURL)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, SessionResponse{Session: sessionView(r, updated)})
}

// clearPreview resets a session's browser preview to empty (`ao preview
// clear`). Unlike setPreview with an empty url it never autodetects: it persists
// an empty target so the desktop browser panel returns to its blank state. The
// write still bumps the preview revision, so the panel hears the change over
// CDC even though the url field is now empty.
func (c *SessionsController) clearPreview(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "DELETE", "/api/v1/sessions/{sessionId}/preview")
		return
	}
	updated, err := c.Svc.SetPreview(r.Context(), sessionID(r), "")
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, SessionResponse{Session: sessionView(r, updated)})
}

func (c *SessionsController) previewServerStatus(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil || c.PreviewServer == nil || c.Capabilities == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/sessions/{sessionId}/preview/server")
		return
	}
	if !c.authorizePreviewServer(w, r) {
		return
	}
	envelope.WriteJSON(w, http.StatusOK, previewServerStatusResponse(c.PreviewServer.Status(sessionID(r))))
}

func (c *SessionsController) startPreviewServer(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil || c.PreviewServer == nil || c.Capabilities == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/sessions/{sessionId}/preview/server")
		return
	}
	var in StartPreviewServerRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	if !c.authorizePreviewServer(w, r) {
		return
	}
	sess, err := c.Svc.Get(r.Context(), sessionID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	var projectEnv map[string]string
	if sess.ProjectID != "" && c.Projects != nil {
		project, err := c.Projects.Get(r.Context(), sess.ProjectID)
		if err != nil {
			envelope.WriteError(w, r, err)
			return
		}
		if project.Project != nil && project.Project.Config != nil {
			projectEnv = project.Project.Config.Env
		}
	}
	previous := c.PreviewServer.Status(sessionID(r))
	status, err := c.PreviewServer.Start(
		r.Context(),
		sessionID(r),
		sess.Metadata.WorkspacePath,
		strings.TrimSpace(in.Configuration),
		projectEnv,
	)
	if err != nil {
		currentStatus := c.PreviewServer.Status(sessionID(r))
		if previous.URL != "" &&
			(currentStatus.State != previewserver.StateReady || currentStatus.URL != previous.URL) {
			if current, getErr := c.Svc.Get(r.Context(), sessionID(r)); getErr == nil &&
				current.Metadata.PreviewURL == previous.URL {
				clearCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Second)
				_, _ = c.Svc.SetPreview(clearCtx, sessionID(r), "")
				cancel()
			}
		}
		writePreviewServerError(w, r, err)
		return
	}
	if status.TargetKind == previewserver.TargetApp {
		if _, err := c.Svc.SetPreview(r.Context(), sessionID(r), status.URL); err != nil {
			_, _ = c.PreviewServer.Stop(context.Background(), sessionID(r))
			envelope.WriteError(w, r, err)
			return
		}
	}
	envelope.WriteJSON(w, http.StatusOK, previewServerStatusResponse(status))
}

func (c *SessionsController) stopPreviewServer(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil || c.PreviewServer == nil || c.Capabilities == nil {
		apispec.NotImplemented(w, r, http.MethodDelete, "/api/v1/sessions/{sessionId}/preview/server")
		return
	}
	if !c.authorizePreviewServer(w, r) {
		return
	}
	previous := c.PreviewServer.Status(sessionID(r))
	status, err := c.PreviewServer.Stop(r.Context(), sessionID(r))
	if err != nil {
		writePreviewServerError(w, r, err)
		return
	}
	current, getErr := c.Svc.Get(r.Context(), sessionID(r))
	if getErr != nil {
		envelope.WriteError(w, r, getErr)
		return
	}
	if previous.URL != "" && current.Metadata.PreviewURL == previous.URL {
		if _, err := c.Svc.SetPreview(r.Context(), sessionID(r), ""); err != nil {
			envelope.WriteError(w, r, err)
			return
		}
	}
	envelope.WriteJSON(w, http.StatusOK, previewServerStatusResponse(status))
}

func (c *SessionsController) authorizePreviewServer(w http.ResponseWriter, r *http.Request) bool {
	id := sessionID(r)
	sess, err := c.Svc.Get(r.Context(), id)
	if err != nil {
		envelope.WriteError(w, r, err)
		return false
	}
	if sess.IsTerminated {
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "SESSION_TERMINATED", "Session is terminated", nil)
		return false
	}
	if c.Capabilities.Valid(
		id,
		strings.TrimSpace(r.Header.Get(browserCapabilityHeader)),
		sess.Metadata.BrowserCapabilityVerifier,
	) {
		return true
	}
	if c.ShellPreviewCapabilities != nil {
		valid, err := c.ShellPreviewCapabilities.ValidPreviewCapability(
			r.Context(), id, strings.TrimSpace(r.Header.Get("X-AO-Preview-Capability")),
		)
		if err != nil {
			envelope.WriteError(w, r, err)
			return false
		}
		if valid {
			return true
		}
	}
	envelope.WriteAPIError(
		w,
		r,
		http.StatusForbidden,
		"forbidden",
		"PREVIEW_CAPABILITY_INVALID",
		"Preview capability is invalid",
		nil,
	)
	return false
}

func previewServerStatusResponse(status previewserver.Status) PreviewServerStatusResponse {
	logs := status.Logs
	if logs == nil {
		logs = []string{}
	}
	return PreviewServerStatusResponse{
		SessionID:     status.SessionID,
		State:         string(status.State),
		Configuration: status.Configuration,
		TargetKind:    string(status.TargetKind),
		URL:           status.URL,
		Port:          status.Port,
		StartedAt:     status.StartedAt,
		Error:         status.Error,
		Logs:          logs,
	}
}

func writePreviewServerError(w http.ResponseWriter, r *http.Request, err error) {
	var serviceErr previewserver.Error
	if !errors.As(err, &serviceErr) {
		envelope.WriteError(w, r, err)
		return
	}
	status := http.StatusUnprocessableEntity
	typeName := "unprocessable"
	switch serviceErr.Code {
	case "PREVIEW_CONFIG_NOT_FOUND", "PREVIEW_CONFIGURATION_NOT_FOUND":
		status = http.StatusNotFound
		typeName = "not_found"
	case "PREVIEW_CONFIGURATION_REQUIRED":
		status = http.StatusBadRequest
		typeName = "bad_request"
	case "PREVIEW_NOT_READY":
		status = http.StatusGatewayTimeout
		typeName = "timeout"
	case "PREVIEW_START_CANCELED":
		status = http.StatusRequestTimeout
		typeName = "timeout"
	case "PREVIEW_START_FAILED", "PREVIEW_EXITED", "PREVIEW_STOP_FAILED":
		status = http.StatusInternalServerError
		typeName = "internal_error"
	}
	envelope.WriteAPIError(w, r, status, typeName, serviceErr.Code, serviceErr.Message, nil)
}

func (c *SessionsController) listPRs(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/sessions/{sessionId}/pr")
		return
	}
	listing, err := c.Svc.ListPRListing(r.Context(), sessionID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	linked := make([]SessionPRReference, len(listing.Linked))
	for i, ref := range listing.Linked {
		linked[i] = SessionPRReference{URL: ref.URL, Provider: ref.Provider, Host: ref.Host, Repo: ref.Repository, Number: ref.Number}
	}
	envelope.WriteJSON(w, http.StatusOK, ListSessionPRsResponse{SessionID: sessionID(r), PRs: sessionPRSummaries(listing.Tracked), LinkedPRs: linked})
}

func (c *SessionsController) claimPR(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/pr/claim")
		return
	}
	var in ClaimPRRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	if strings.TrimSpace(in.PR) == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "PR_REQUIRED", "pr is required", nil)
		return
	}
	allowTakeover := true
	if in.AllowTakeover != nil {
		allowTakeover = *in.AllowTakeover
	}
	res, err := c.Svc.ClaimPR(r.Context(), sessionID(r), in.PR, sessionsvc.ClaimPROptions{AllowTakeover: allowTakeover})
	if err != nil {
		writeSessionPRError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ClaimPRResponse{OK: true, SessionID: sessionID(r), PRs: sessionPRFacts(res.PRs), BranchChanged: res.BranchChanged, TakenOverFrom: nonNilSessionIDs(res.TakenOverFrom)})
}

func (c *SessionsController) rename(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "PATCH", "/api/v1/sessions/{sessionId}")
		return
	}
	var in RenameSessionRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "DISPLAY_NAME_REQUIRED", "displayName is required", nil)
		return
	}
	if utf8.RuneCountInString(displayName) > maxDisplayNameLen {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "DISPLAY_NAME_TOO_LONG", fmt.Sprintf("displayName must be %d characters or fewer", maxDisplayNameLen), nil)
		return
	}
	if err := c.Svc.Rename(r.Context(), sessionID(r), displayName); err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, RenameSessionResponse{OK: true, SessionID: sessionID(r), DisplayName: displayName})
}

func (c *SessionsController) setMergePolicy(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "PATCH", "/api/v1/sessions/{sessionId}/merge-policy")
		return
	}
	var in SetSessionMergePolicyRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	sess, err := c.Svc.SetTerminateOnPRMerge(r.Context(), sessionID(r), in.TerminateOnPRMerge)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, SetSessionMergePolicyResponse{
		OK:                 true,
		SessionID:          sessionID(r),
		TerminateOnPRMerge: in.TerminateOnPRMerge,
		Session:            sessionView(r, sess),
	})
}

func (c *SessionsController) setAutoInjectReviewPolicy(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "PATCH", "/api/v1/sessions/{sessionId}/auto-inject-review")
		return
	}
	var in SetSessionAutoInjectReviewRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	sess, err := c.Svc.SetAutoInjectReview(r.Context(), sessionID(r), in.AutoInjectReview)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, SetSessionAutoInjectReviewResponse{
		OK:               true,
		SessionID:        sessionID(r),
		AutoInjectReview: in.AutoInjectReview,
		Session:          sessionView(r, sess),
	})
}

func (c *SessionsController) setAutoInjectCIPolicy(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "PATCH", "/api/v1/sessions/{sessionId}/auto-inject-ci")
		return
	}
	// Decode through a pointer so an omitted required field remains distinct
	// from the valid explicit value {"autoInjectCI":false}. The public request
	// DTO stays non-pointer so the generated OpenAPI schema remains a required,
	// non-nullable boolean.
	var in struct {
		AutoInjectCI *bool `json:"autoInjectCI"`
	}
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	if in.AutoInjectCI == nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "AUTO_INJECT_CI_REQUIRED", "autoInjectCI is required", nil)
		return
	}
	autoInjectCI := *in.AutoInjectCI
	sess, err := c.Svc.SetAutoInjectCI(r.Context(), sessionID(r), autoInjectCI)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, SetSessionAutoInjectCIResponse{
		OK:           true,
		SessionID:    sessionID(r),
		AutoInjectCI: autoInjectCI,
		Session:      sessionView(r, sess),
	})
}

func (c *SessionsController) setReviewer(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "PUT", "/api/v1/sessions/{sessionId}/reviewer")
		return
	}
	var in SetSessionReviewerRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	sess, err := c.Svc.SetReviewerHarness(r.Context(), sessionID(r), in.Harness, in.AgentConfig)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, SessionResponse{Session: sessionView(r, sess)})
}

func (c *SessionsController) setAutoReview(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "PUT", "/api/v1/sessions/{sessionId}/auto-review")
		return
	}
	var in SetSessionAutoReviewRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	if !in.enabledPresent {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "AUTO_REVIEW_ENABLED_REQUIRED", "enabled is required", nil)
		return
	}
	sess, err := c.Svc.SetAutoReview(r.Context(), sessionID(r), in.Enabled)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, SessionResponse{Session: sessionView(r, sess)})
}

func (c *SessionsController) restore(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/restore")
		return
	}
	out, err := c.Svc.Restore(r.Context(), sessionID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, RestoreSessionResponse{OK: true, SessionID: sessionID(r), RestoreMode: out.Mode, Session: sessionView(r, out.Session)})
}

func (c *SessionsController) pin(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/pin")
		return
	}
	sess, err := c.Svc.Pin(r.Context(), sessionID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, SessionResponse{Session: sessionView(r, sess)})
}

func (c *SessionsController) unpin(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "DELETE", "/api/v1/sessions/{sessionId}/pin")
		return
	}
	sess, err := c.Svc.Unpin(r.Context(), sessionID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, SessionResponse{Session: sessionView(r, sess)})
}

func (c *SessionsController) resumeAgent(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/resume-agent")
		return
	}
	out, err := c.Svc.ResumeAgent(r.Context(), sessionID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ResumeAgentResponse{
		OK:         true,
		SessionID:  sessionID(r),
		ResumeMode: out.Mode,
		Session:    sessionView(r, out.Session),
	})
}

func (c *SessionsController) exitAgent(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/exit-agent")
		return
	}
	out, err := c.Svc.ExitAgent(r.Context(), sessionID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ExitAgentResponse{
		OK:        true,
		SessionID: sessionID(r),
		Session:   sessionView(r, out.Session),
	})
}

func (c *SessionsController) switchAgent(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/switch-agent")
		return
	}
	var in SwitchAgentRequest
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	targetHarness := domain.AgentHarness(strings.TrimSpace(string(in.TargetHarness)))
	if targetHarness == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "TARGET_HARNESS_REQUIRED", "targetHarness is required", nil)
		return
	}
	model := strings.TrimSpace(domain.SanitizeControlChars(in.Model))
	if utf8.RuneCountInString(model) > maxModelLen {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "MODEL_TOO_LONG", "Model must be 256 characters or fewer", nil)
		return
	}
	idempotencyKey := strings.TrimSpace(in.IdempotencyKey)
	if len(idempotencyKey) > maxIdempotencyKey {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "IDEMPOTENCY_KEY_TOO_LONG", "idempotencyKey is too long", nil)
		return
	}
	switchRecord, err := c.Svc.SwitchAgent(r.Context(), sessionID(r), sessionsvc.SwitchAgentInput{
		TargetHarness:  targetHarness,
		Model:          model,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, AgentSwitchResponse{Switch: agentSwitchView(switchRecord)})
}

func (c *SessionsController) listAgentSwitches(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/sessions/{sessionId}/agent-switches")
		return
	}
	switches, err := c.Svc.ListAgentSwitches(r.Context(), sessionID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ListAgentSwitchesResponse{Switches: agentSwitchViews(switches)})
}

func (c *SessionsController) recoverAgentSwitch(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/agent-switches/{switchId}/recover")
		return
	}
	switchRecord, err := c.Svc.RecoverAgentSwitch(r.Context(), sessionID(r), agentSwitchID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, AgentSwitchResponse{Switch: agentSwitchView(switchRecord)})
}

func (c *SessionsController) submitAgentHandoff(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/agent-switches/{switchId}/handoff")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAgentHandoffBodyBytes)
	var in SubmitAgentHandoffRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	sourceGenerationID := domain.AgentGenerationID(strings.TrimSpace(string(in.SourceGenerationID)))
	if sourceGenerationID == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "SOURCE_GENERATION_REQUIRED", "sourceGenerationId is required", nil)
		return
	}
	if len(bytes.TrimSpace(in.Handoff)) == 0 {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "HANDOFF_REQUIRED", "handoff is required", nil)
		return
	}
	if len(in.Handoff) > maxAgentHandoffBytes {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "HANDOFF_TOO_LARGE", "handoff must be no larger than 64 KiB", nil)
		return
	}
	switchRecord, err := c.Svc.SubmitAgentHandoff(
		r.Context(), sessionID(r), agentSwitchID(r), sourceGenerationID, in.Handoff,
	)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, AgentSwitchResponse{Switch: agentSwitchView(switchRecord)})
}

func (c *SessionsController) kill(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/kill")
		return
	}
	result, err := c.Svc.RequestKill(r.Context(), sessionID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, KillSessionResponse{OK: true, SessionID: sessionID(r), Freed: result.Freed, CleanupPending: result.CleanupPending})
}

// rollback undoes a partially-completed spawn: if the session row is still in
// seed state (no workspace, no runtime handle yet), the row is deleted
// outright. If anything observable has landed it falls back to Kill so the
// runtime/workspace are torn down. Used by `ao spawn --claim-pr` to undo a
// session whose claim step failed, avoiding the orphan terminated row a
// plain Kill would leave behind.
func (c *SessionsController) rollback(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/rollback")
		return
	}
	out, err := c.Svc.RollbackSpawn(r.Context(), sessionID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, RollbackSessionResponse{OK: true, SessionID: sessionID(r), Deleted: out.Deleted, Killed: out.Killed})
}

func (c *SessionsController) cleanup(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/cleanup")
		return
	}
	out, err := c.Svc.Cleanup(r.Context(), domain.ProjectID(r.URL.Query().Get("project")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	skipped := make([]CleanupSkippedSession, 0, len(out.Skipped))
	for _, skip := range out.Skipped {
		skipped = append(skipped, CleanupSkippedSession{SessionID: skip.SessionID, Reason: skip.Reason})
	}
	envelope.WriteJSON(w, http.StatusOK, CleanupSessionsResponse{
		OK: true, Cleaned: out.Cleaned, AlreadyGone: out.AlreadyGone, Skipped: skipped,
	})
}

func (c *SessionsController) send(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/send")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSendBodyBytes)
	var in SendSessionMessageRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	if in.Message == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "MESSAGE_REQUIRED", "Message is required", nil)
		return
	}
	if len(in.Message) > maxMessageLen {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "MESSAGE_TOO_LONG", "Message is too long", nil)
		return
	}
	var attachment *ports.SpawnAttachment
	if in.Attachment != nil {
		decoded, attachErr := decodeAttachment(*in.Attachment, maxAttachmentBytes)
		if attachErr != nil {
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", attachErr.code, attachErr.message, nil)
			return
		}
		attachment = &decoded
	}
	message := domain.SanitizeControlChars(in.Message)
	var err error
	if in.UserAuthored || in.SenderSessionID != "" {
		sender, ok := c.Svc.(sessionMessageOptionsSender)
		if !ok {
			apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/send")
			return
		}
		err = sender.SendWithOptions(r.Context(), sessionID(r), message, attachment, ports.MessageDeliveryOptions{AuthoredByUser: in.UserAuthored, SenderSessionID: in.SenderSessionID})
	} else {
		err = c.Svc.Send(r.Context(), sessionID(r), message, attachment)
	}
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, SendSessionMessageResponse{OK: true, SessionID: sessionID(r), Message: message})
}

func (c *SessionsController) delegateTask(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/orchestrators/delegate")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSpawnBodyBytes)
	var in DelegateTaskRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	if !validClientRequestID(in.ClientRequestID) {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", "INVALID_CLIENT_REQUEST_ID", "clientRequestId must be 1-128 visible characters", nil)
		return
	}
	if in.ProjectID == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "PROJECT_ID_REQUIRED", "projectId is required", nil)
		return
	}
	if len(in.Brief) > maxPromptLen {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "TASK_TOO_LONG", "Task must be 16 KiB or fewer", nil)
		return
	}
	if utf8.RuneCountInString(strings.TrimSpace(in.Model)) > maxModelLen {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "MODEL_TOO_LONG", "Model must be 256 characters or fewer", nil)
		return
	}
	if in.Mode != "" {
		mode, err := domain.ParseSessionMode(string(in.Mode))
		if err != nil {
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_SESSION_MODE", "mode must be chat or tui", nil)
			return
		}
		in.Mode = mode
	}
	if !in.ApprovalMode.Valid() {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_APPROVAL_MODE", "approvalMode is invalid", nil)
		return
	}
	attachments, attachErr := decodeSpawnAttachments(in.Attachments)
	if attachErr != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", attachErr.code, attachErr.message, nil)
		return
	}
	requestHash := ""
	if in.ClientRequestID != "" {
		keyless := in
		keyless.ClientRequestID = ""
		keyless.TaskPreparation = ""
		keyless.Attachments = nil
		requestHash = clientRequestFingerprint("delegate", keyless, attachments)
	}

	out, err := c.Svc.DelegateTask(r.Context(), sessionsvc.DelegateTaskInput{
		ClientRequestID:   in.ClientRequestID,
		ClientRequestHash: requestHash,
		AccountID:         in.AccountID,
		ProjectID:         in.ProjectID,
		Brief:             domain.SanitizeControlChars(in.Brief),
		RequestedAgent:    in.Agent,
		Model:             domain.SanitizeControlChars(strings.TrimSpace(in.Model)),
		Effort:            sanitizedOptionalString(in.Effort),
		ApprovalMode:      in.ApprovalMode,
		RequestedMode:     in.Mode,
		Attachments:       attachments,
		TaskPreparation:   domain.TaskPreparationToken(strings.TrimSpace(in.TaskPreparation)),
	})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, DelegateTaskResponse{OK: true, WorkerID: out.WorkerID, OrchestratorID: out.OrchestratorID})
}

func validClientRequestID(id string) bool {
	return id == "" || len(id) <= maxIdempotencyKey && strings.TrimSpace(id) == id && strings.IndexFunc(id, unicode.IsControl) < 0
}

func clientRequestFingerprint(route string, request any, attachments []ports.SpawnAttachment) string {
	h := sha256.New()
	_ = json.NewEncoder(h).Encode(struct {
		Route   string
		Request any
	}{route, request})
	for _, attachment := range attachments {
		_, _ = fmt.Fprintf(h, "%d:%s:%d:", len(attachment.Ext), attachment.Ext, len(attachment.Data))
		_, _ = h.Write(attachment.Data)
	}
	return "v1:" + hex.EncodeToString(h.Sum(nil))
}

func (c *SessionsController) prepareTask(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/projects/{id}/tasks/prepare")
		return
	}
	token, err := c.Svc.PrepareTask(r.Context(), projectID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, PrepareTaskResponse{OK: true, TaskPreparation: token})
}

func (c *SessionsController) cancelTaskPreparation(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "DELETE", "/api/v1/task-preparations/{token}")
		return
	}
	if err := c.Svc.CancelTaskPreparation(r.Context(), chi.URLParam(r, "token")); err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func sanitizedOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	sanitized := domain.SanitizeControlChars(strings.TrimSpace(*value))
	return &sanitized
}

// activity records an agent activity-state signal reported by an agent hook
// (via `ao hooks <agent> <event>`). It funnels through the single
// lifecycle.Manager so the reaper and hooks never race on the session's
// activity/termination columns.
func (c *SessionsController) activity(w http.ResponseWriter, r *http.Request) {
	if c.Activity == nil && c.Usage == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/activity")
		return
	}
	var in SetActivityRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	state := domain.ActivityState(in.State)
	if state != "" {
		switch state {
		case domain.ActivityActive, domain.ActivityIdle, domain.ActivityWaitingInput, domain.ActivityBlocked, domain.ActivityExited:
		default:
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_ACTIVITY_STATE", "Unknown activity state", nil)
			return
		}
	}
	agentSessionID := capActivityMeta(domain.SanitizeControlChars(strings.TrimSpace(in.AgentSessionID)))
	checkpointOrigin := domain.ConversationCheckpointOrigin(strings.TrimSpace(string(in.ConversationCheckpointOrigin)))
	if !checkpointOrigin.Valid() {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_CONVERSATION_CHECKPOINT_ORIGIN", "Conversation checkpoint origin must be human or coordination", nil)
		return
	}
	if state == "" && agentSessionID == "" && in.Usage == nil && in.SubagentID == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "ACTIVITY_OR_SESSION_ID_REQUIRED", "Activity state, agent session ID, or subagent ID is required", nil)
		return
	}
	// The correlation fields ride the same lenient decode: absent on old CLIs.
	// They are externally-supplied strings headed for logs and in-memory maps,
	// so sanitize control chars and cap their length (a truncated id could
	// never match its pre/post counterpart, so overlong values are dropped by
	// the CLI; the cap here is defense against non-AO callers).
	sig := ports.ActivitySignal{
		Timestamp:                    in.ObservedAt,
		Valid:                        state != "",
		State:                        state,
		Event:                        capActivityMeta(domain.SanitizeControlChars(in.Event)),
		ToolName:                     capActivityMeta(domain.SanitizeControlChars(in.ToolName)),
		ToolUseID:                    capActivityMeta(domain.SanitizeControlChars(in.ToolUseID)),
		SubagentID:                   capActivityMeta(domain.SanitizeControlChars(in.SubagentID)),
		AgentSessionID:               agentSessionID,
		LatestUserPrompt:             capActivityText(domain.SanitizeControlChars(strings.TrimSpace(in.LatestUserPrompt)), 16<<10),
		LatestAssistantUpdate:        capActivityText(domain.SanitizeControlChars(strings.TrimSpace(in.LatestAssistantUpdate)), 16<<10),
		ConversationCheckpointOrigin: checkpointOrigin,
		CoordinationID:               capActivityMeta(domain.SanitizeControlChars(strings.TrimSpace(in.CoordinationID))),
		ProviderTurnID:               capActivityMeta(domain.SanitizeControlChars(strings.TrimSpace(in.ProviderTurnID))),
		SubmissionID:                 capActivityMeta(domain.SanitizeControlChars(strings.TrimSpace(in.SubmissionID))),
		TranscriptPath:               capActivityText(domain.SanitizeControlChars(strings.TrimSpace(in.TranscriptPath)), 4096),
		LaunchID:                     capActivityMeta(domain.SanitizeControlChars(strings.TrimSpace(in.LaunchID))),
	}
	if in.RunningSubagentIDs != nil && len(*in.RunningSubagentIDs) <= 128 {
		running := make([]string, 0, len(*in.RunningSubagentIDs))
		for _, id := range *in.RunningSubagentIDs {
			clean := capActivityMeta(domain.SanitizeControlChars(strings.TrimSpace(id)))
			if clean == "" {
				running = nil
				break
			}
			running = append(running, clean)
		}
		if running != nil {
			sig.RunningSubagentIDs = &running
		}
	}
	var activityErr error
	if c.Activity != nil && (sig.Valid || sig.AgentSessionID != "" || sig.SubagentID != "") {
		activityErr = c.Activity.ApplyActivitySignal(r.Context(), sessionID(r), sig)
		if err := activityErr; err != nil && !errors.Is(err, ports.ErrActivityProjectionContention) {
			if errors.Is(err, ports.ErrSessionNotFound) {
				envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "SESSION_NOT_FOUND", "Unknown session", nil)
				return
			}
			envelope.WriteError(w, r, err)
			return
		}
	}
	if c.Usage != nil {
		usageSignal := usagesvc.HookSignal{
			Event:           sig.Event,
			LaunchID:        sig.LaunchID,
			NativeSessionID: agentSessionID,
		}
		if in.Usage != nil {
			usageSignal.Harness = domain.AgentHarness(capActivityMeta(domain.SanitizeControlChars(strings.TrimSpace(string(in.Usage.Harness)))))
			usageSignal.ProviderHint = capActivityMeta(domain.SanitizeControlChars(strings.TrimSpace(in.Usage.ProviderID)))
			usageSignal.TranscriptPath = capUsagePath(domain.SanitizeControlChars(strings.TrimSpace(in.Usage.TranscriptPath)))
			usageSignal.ModelID = capActivityMeta(domain.SanitizeControlChars(strings.TrimSpace(in.Usage.ModelID)))
			usageSignal.SubagentID = capActivityMeta(domain.SanitizeControlChars(strings.TrimSpace(in.Usage.SubagentID)))
			usageSignal.SubagentTranscriptPath = capUsagePath(domain.SanitizeControlChars(strings.TrimSpace(in.Usage.SubagentTranscriptPath)))
		}
		if err := c.Usage.RecordHook(r.Context(), sessionID(r), usageSignal); err != nil {
			if errors.Is(err, usagesvc.ErrUsageSessionNotFound) {
				envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "SESSION_NOT_FOUND", "Unknown session", nil)
				return
			}
			slog.Default().Warn(
				"usage hook processing failed",
				"session", sessionID(r),
				"event", sig.Event,
				"err", err,
			)
		}
	}
	if activityErr != nil {
		// The projection never committed, so the hook can retry the same payload.
		// Usage observation is independent and must still run on contention.
		w.Header().Set("Retry-After", "1")
		envelope.WriteAPIError(w, r, http.StatusServiceUnavailable, "unavailable", "ACTIVITY_PROJECTION_BUSY", "Concurrent session updates prevented this activity signal from committing; retry the hook", nil)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, SetActivityResponse{OK: true, SessionID: sessionID(r), State: in.State})
}

// codewhaleActivity translates Codewhale's v0.10 lifecycle outbox webhook into
// AO's provider-neutral activity signal. The launch id lives in the callback
// URL because Codewhale's event envelope has no supervisor-generation field.
func (c *SessionsController) codewhaleActivity(w http.ResponseWriter, r *http.Request) {
	if c.Activity == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/activity/codewhale")
		return
	}
	launchID := capActivityMeta(domain.SanitizeControlChars(strings.TrimSpace(r.URL.Query().Get("launchId"))))
	if launchID == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "LAUNCH_ID_REQUIRED", "Codewhale lifecycle callbacks require a launchId", nil)
		return
	}
	var in CodewhaleLifecycleWebhookRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	if in.Event.SchemaVersion != 1 {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "UNSUPPORTED_CODEWHALE_LIFECYCLE_SCHEMA", "Unsupported Codewhale lifecycle schema", nil)
		return
	}

	hookThreadID := capActivityMeta(domain.SanitizeControlChars(strings.TrimSpace(in.Event.ThreadID)))
	if hookThreadID == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "CODEWHALE_THREAD_ID_REQUIRED", "Codewhale lifecycle callbacks require a thread_id", nil)
		return
	}
	signal := ports.ActivitySignal{
		Timestamp:      in.Event.Timestamp,
		Event:          capActivityMeta(domain.SanitizeControlChars(strings.TrimSpace(in.Event.Event))),
		ProviderTurnID: capActivityMeta(domain.SanitizeControlChars(strings.TrimSpace(in.Event.TurnID))),
		LaunchID:       launchID,
	}
	if signal.Timestamp.IsZero() {
		signal.Timestamp = in.At
	}
	switch strings.TrimSpace(in.Event.Kind) {
	case "session.started":
		signal.Event = "session-start"
	case "turn.started":
		signal.Valid = true
		signal.State = domain.ActivityActive
		signal.Event = "user-prompt-submit"
	case "turn.completed", "turn.failed", "turn.interrupted", "turn.stalled":
		signal.Valid = true
		signal.State = domain.ActivityWaitingInput
		signal.Event = "stop"
	case "session.ended":
		signal.Valid = true
		signal.State = domain.ActivityExited
		signal.Event = "session-end"
	case "subagent.spawned", "subagent.completed":
		// Provider-internal subagents remain opaque activity inside this AO
		// session until AO has a provider-neutral nested-session model.
		envelope.WriteJSON(w, http.StatusOK, SetActivityResponse{OK: true, SessionID: sessionID(r)})
		return
	default:
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "UNKNOWN_CODEWHALE_LIFECYCLE_EVENT", "Unknown Codewhale lifecycle event", nil)
		return
	}
	// Codewhale's thread_id is a per-process HookExecutor id (sess_*), not the
	// durable UUID accepted by --resume. Terminal turn/session events run after
	// Codewhale saves the conversation, so resolve that UUID from the launch's
	// isolated Runtime store before lifecycle persists a native resume handle.
	if c.NativeSessions != nil && (signal.Event == "stop" || signal.Event == "session-end") {
		nativeID, ok, err := c.NativeSessions.ResolveNativeSessionID(r.Context(), ports.NativeSessionResolveConfig{
			DataDir:   c.DataDir,
			SessionID: sessionID(r),
			LaunchID:  launchID,
		})
		if err != nil {
			envelope.WriteError(w, r, err)
			return
		}
		if ok {
			signal.AgentSessionID = capActivityMeta(domain.SanitizeControlChars(strings.TrimSpace(nativeID)))
		}
	}
	if err := c.Activity.ApplyActivitySignal(r.Context(), sessionID(r), signal); err != nil {
		if errors.Is(err, ports.ErrActivityProjectionContention) {
			w.Header().Set("Retry-After", "1")
			envelope.WriteAPIError(w, r, http.StatusServiceUnavailable, "unavailable", "ACTIVITY_PROJECTION_BUSY", "Concurrent session updates prevented this activity signal from committing", nil)
			return
		}
		if errors.Is(err, ports.ErrSessionNotFound) {
			envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "SESSION_NOT_FOUND", "Unknown session", nil)
			return
		}
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, SetActivityResponse{OK: true, SessionID: sessionID(r), State: string(signal.State)})
}

// capActivityMeta bounds an optional activity correlation string; overlong
// values are dropped, not truncated (see the comment at its call site).
func capActivityMeta(v string) string {
	const maxLen = 256
	if len(v) > maxLen {
		return ""
	}
	return v
}

func capUsagePath(v string) string {
	const maxLen = 4096
	if len(v) > maxLen {
		return ""
	}
	return v
}

func capActivityText(v string, maxLen int) string {
	if maxLen <= 0 || len(v) <= maxLen {
		return v
	}
	const marker = "\n[... truncated by AO ...]\n"
	budget := maxLen - len(marker)
	if budget <= 0 {
		return ""
	}
	head := budget / 2
	tail := budget - head
	return strings.ToValidUTF8(string([]byte(v)[:head])+marker+string([]byte(v)[len(v)-tail:]), "?")
}

func (c *SessionsController) spawnOrchestrator(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/orchestrators")
		return
	}
	var in SpawnOrchestratorRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	if in.ProjectID == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "PROJECT_ID_REQUIRED", "projectId is required", nil)
		return
	}
	if in.Mode != "" {
		if _, err := domain.ParseSessionMode(string(in.Mode)); err != nil {
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", "SESSION_MODE_INVALID", err.Error(), nil)
			return
		}
	}
	if !in.ApprovalMode.Valid() {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_APPROVAL_MODE", "approvalMode is invalid", nil)
		return
	}
	sess, err := c.Svc.SpawnOrchestrator(r.Context(), in.ProjectID, in.Clean, in.Mode, in.ApprovalMode)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, SpawnOrchestratorResponse{
		Orchestrator: OrchestratorResponse{ID: sess.ID, ProjectID: sess.ProjectID},
	})
}

func (c *SessionsController) listOrchestrators(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/orchestrators")
		return
	}
	sessions, err := c.Svc.List(r.Context(), sessionsvc.ListFilter{OrchestratorOnly: true})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ListSessionsResponse{Sessions: sessionViews(r, sessions)})
}

func (c *SessionsController) getOrchestrator(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/orchestrators/{id}")
		return
	}
	sess, err := c.Svc.Get(r.Context(), orchestratorID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	if sess.Kind != domain.KindOrchestrator {
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "SESSION_NOT_FOUND", "Unknown session", nil)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, SessionResponse{Session: sessionView(r, sess)})
}

func sessionID(r *http.Request) domain.SessionID {
	return domain.SessionID(chi.URLParam(r, "sessionId"))
}

func agentSwitchID(r *http.Request) domain.AgentSwitchID {
	return domain.AgentSwitchID(chi.URLParam(r, "switchId"))
}

func orchestratorID(r *http.Request) domain.SessionID {
	return domain.SessionID(chi.URLParam(r, "id"))
}

func parseSessionListFilter(r *http.Request) (sessionsvc.ListFilter, error) {
	q := r.URL.Query()
	filter := sessionsvc.ListFilter{ProjectID: domain.ProjectID(q.Get("project"))}
	if raw := q.Get("active"); raw != "" {
		active, err := strconv.ParseBool(raw)
		if err != nil {
			return sessionsvc.ListFilter{}, errors.New("active must be a boolean")
		}
		filter.Active = &active
	}
	if raw := q.Get("orchestratorOnly"); raw != "" {
		orchestratorOnly, err := strconv.ParseBool(raw)
		if err != nil {
			return sessionsvc.ListFilter{}, errors.New("orchestratorOnly must be a boolean")
		}
		filter.OrchestratorOnly = orchestratorOnly
	}
	if raw := q.Get("fresh"); raw != "" {
		fresh, err := strconv.ParseBool(raw)
		if err != nil {
			return sessionsvc.ListFilter{}, errors.New("fresh must be a boolean")
		}
		filter.Fresh = fresh
	}
	return filter, nil
}

func writeSessionPRError(w http.ResponseWriter, r *http.Request, err error) {
	var claimed ports.PRClaimedByActiveSessionError
	switch {
	case errors.Is(err, sessionsvc.ErrInvalidPRRef):
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_PR_REF", "PR reference must be a full PR/MR URL or a number resolved against the project origin or canonical repository. For a workspace child repository, pass its full PR/MR URL; a root without a remote cannot resolve numbers.", nil)
	case errors.Is(err, sessionsvc.ErrPRNotFound):
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "PR_NOT_FOUND", "Unknown PR", nil)
	case errors.Is(err, sessionsvc.ErrPRNotOpen):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "PR_NOT_OPEN", "PR is not open", nil)
	case errors.As(err, &claimed):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "PR_CLAIMED_BY_ACTIVE_SESSION", "PR is already claimed by active session "+string(claimed.Owner)+" (omit --no-takeover to steal)", map[string]any{"ownerSessionId": string(claimed.Owner)})
	case errors.Is(err, sessionsvc.ErrSessionNotClaimable):
		envelope.WriteAPIError(w, r, http.StatusUnprocessableEntity, "unprocessable", "SESSION_NOT_CLAIMABLE", "Session cannot claim PRs", nil)
	case errors.Is(err, sessionsvc.ErrSessionNoWorkspace):
		envelope.WriteAPIError(w, r, http.StatusUnprocessableEntity, "unprocessable", "SESSION_NO_WORKSPACE", "Session has no workspace", nil)
	case errors.Is(err, sessionsvc.ErrProjectMismatch):
		envelope.WriteAPIError(w, r, http.StatusUnprocessableEntity, "unprocessable", "PR_PROJECT_MISMATCH", "PR repository must match the project origin, its explicit canonicalRepoURL, or a registered workspace child origin. For workspace projects, check the registered repositories with ao project get <project-id> --json and pass the child's full PR/MR URL. canonicalRepoURL requires a valid root origin and is not a child-repository allowlist. For single-repo forks, configure the upstream HTTPS repository URL with ao project set-config <project-id> --canonical-repo-url <url> (replaces config; preserve existing fields with --config-json). Git remotes alone do not grant trust.", nil)
	case errors.Is(err, sessionsvc.ErrSCMUnavailable):
		envelope.WriteAPIError(w, r, http.StatusServiceUnavailable, "unavailable", "SCM_UNAVAILABLE", "SCM unavailable", nil)
	default:
		envelope.WriteError(w, r, err)
	}
}

func discoverPreviewEntry(workspacePath string) (string, bool) {
	// Use DiscoverWebEntrypoint (index.html variants only), not DiscoverEntry
	// (which falls back to mostRecentPreviewable — the newest .md/.html in the
	// workspace). Bare `ao preview` (no args) hits this path, and agent
	// harnesses run that automatically on new sessions via the using-ao skill.
	// With the .md fallback, every new session in a Markdown-rich repo opened
	// its browser panel to an arbitrary repo doc (e.g. test/cli/README.md)
	// instead of staying empty. Mirrors the poller fix from PR #2860.
	// See issue #2859.
	entry, ok := previewutil.DiscoverWebEntrypoint(workspacePath)
	return entry.Path, ok
}

// resolveLocalPreview maps a workspace-local path (e.g. "index.html" or
// "./dist/index.html") to its preview/files proxy URL when the path resolves to
// a regular file inside the session workspace. It returns ok=false for anything
// that already looks like a URL (an http(s)/file scheme, or a host:port dev
// server) and for paths that do not point at a workspace file, so the caller
// keeps those targets verbatim. Explicit parent traversal is rejected by
// resolvePreviewTarget before this function is called.
func resolveLocalPreview(r *http.Request, id domain.SessionID, workspacePath, raw string) (string, bool, error) {
	if raw == "" || hasURLScheme(raw) {
		return "", false, nil
	}
	entry, ok := previewutil.EntryAtPath(workspacePath, raw)
	if !ok {
		return "", false, nil
	}
	resolved, err := previewFileURL(r, id, entry.Path)
	return resolved, true, err
}

func resolvePreviewTarget(r *http.Request, id domain.SessionID, workspacePath, raw string, requireWorkspaceFile bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if filePath, isFileURL, err := previewFileURLPath(raw); isFileURL {
		if err != nil {
			return "", err
		}
		return workspaceAbsolutePreviewURL(r, id, workspacePath, filePath)
	}
	if isAbsolutePreviewPath(raw) {
		return workspaceAbsolutePreviewURL(r, id, workspacePath, raw)
	}
	if !hasURLScheme(raw) && containsParentPathSegment(raw) {
		return "", errPreviewFileOutsideWorkspace
	}
	if resolved, ok, err := resolveLocalPreview(r, id, workspacePath, raw); ok || err != nil {
		return resolved, err
	}
	if requireWorkspaceFile {
		return "", errPreviewFileNotFound
	}
	return raw, nil
}

func containsParentPathSegment(raw string) bool {
	raw = strings.ReplaceAll(raw, `\`, "/")
	for _, segment := range strings.Split(raw, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func isAbsolutePreviewPath(raw string) bool {
	return filepath.IsAbs(raw) || isWindowsAbsolutePath(raw)
}

func isWindowsAbsolutePath(raw string) bool {
	return len(raw) >= 3 && ((raw[0] >= 'a' && raw[0] <= 'z') || (raw[0] >= 'A' && raw[0] <= 'Z')) && raw[1] == ':' && (raw[2] == '\\' || raw[2] == '/')
}

func previewFileURLPath(raw string) (string, bool, error) {
	if len(raw) < len("file:") || !strings.EqualFold(raw[:len("file:")], "file:") {
		return "", false, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", true, errPreviewFileNotFound
	}
	if parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost") {
		return "", true, errPreviewFileOutsideWorkspace
	}
	filePath := parsed.Path
	if len(filePath) > 1 && filePath[0] == '/' && isWindowsAbsolutePath(filePath[1:]) {
		filePath = filePath[1:]
	}
	filePath = filepath.FromSlash(filePath)
	if !isAbsolutePreviewPath(filePath) {
		return "", true, errPreviewFileNotFound
	}
	return filePath, true, nil
}

func workspaceAbsolutePreviewURL(
	r *http.Request,
	id domain.SessionID,
	workspacePath,
	filePath string,
) (string, error) {
	workspaceAbs, err := filepath.Abs(workspacePath)
	if err != nil {
		return "", errPreviewFileNotFound
	}
	workspaceResolved, err := filepath.EvalSymlinks(workspaceAbs)
	if err != nil {
		return "", errPreviewFileNotFound
	}
	fileAbs, err := filepath.Abs(filePath)
	if err != nil {
		return "", errPreviewFileNotFound
	}
	fileResolved, err := filepath.EvalSymlinks(fileAbs)
	if err != nil {
		return "", errPreviewFileNotFound
	}
	rel, err := filepath.Rel(workspaceResolved, fileResolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errPreviewFileOutsideWorkspace
	}
	entry, ok := previewutil.EntryAtPath(workspaceResolved, filepath.ToSlash(rel))
	if !ok {
		return "", errPreviewFileNotFound
	}
	return previewFileURL(r, id, entry.Path)
}

func writePreviewResolveError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errPreviewFileOutsideWorkspace) {
		envelope.WriteAPIError(w, r, http.StatusForbidden, "forbidden", "PREVIEW_FILE_OUTSIDE_WORKSPACE", "Preview files must stay inside the session workspace", nil)
		return
	}
	if errors.Is(err, errPreviewFileNotFound) {
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "PREVIEW_FILE_NOT_FOUND", "Preview file not found", nil)
		return
	}
	if errors.Is(err, previewutil.ErrPreviewHostUnsupported) {
		envelope.WriteAPIError(w, r, http.StatusUnprocessableEntity, "unprocessable", "PREVIEW_SESSION_ID_UNSUPPORTED", "Session ID is too long for an isolated preview hostname", nil)
		return
	}
	envelope.WriteError(w, r, err)
}

// hasURLScheme reports whether raw begins with an RFC-3986 "scheme:" prefix
// (http:, https:, file:, or a host:port like localhost:5173). It mirrors the
// renderer's withDefaultScheme heuristic so the daemon and browser panel agree
// on what counts as a URL versus a workspace-relative path.
func hasURLScheme(raw string) bool {
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c == ':' {
			return i > 0
		}
		isSchemeChar := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '+' || c == '.' || c == '-'
		if !isSchemeChar {
			return false
		}
	}
	return false
}

func previewFileURL(r *http.Request, id domain.SessionID, entry string) (string, error) {
	return previewutil.FileURL("http://"+r.Host, id, entry)
}

func sessionView(r *http.Request, s domain.Session) SessionView {
	terminalGeneration := s.Metadata.RuntimeLaunchID
	view := SessionView{
		Session:            s,
		Branch:             s.Metadata.Branch,
		TerminalGeneration: terminalGeneration,
		PreviewURL:         s.Metadata.PreviewURL,
		PreviewRevision:    s.Metadata.PreviewRevision,
		Model:              s.Metadata.Model,
		LastUserMessageAt: func() *time.Time {
			if s.Metadata.LatestUserPromptAt.IsZero() {
				return nil
			}
			at := s.Metadata.LatestUserPromptAt
			return &at
		}(),
		LastInteractionAt: func() *time.Time {
			at := s.Metadata.LatestUserPromptAt
			if s.Metadata.LatestInteractionAt.After(at) {
				at = s.Metadata.LatestInteractionAt
			}
			if at.IsZero() {
				return nil
			}
			return &at
		}(),
		LastEventAt:   s.LastEventAt(),
		PRs:           sessionPRFacts(s.PRs),
		ArtifactFiles: sessionArtifactFiles(r, s),
	}
	if s.ActiveAgentSwitch != nil {
		active := agentSwitchView(*s.ActiveAgentSwitch)
		view.ActiveAgentSwitch = &active
	}
	return view
}

func agentSwitchView(s domain.AgentSwitch) AgentSwitchView {
	return AgentSwitchView{
		ID:                      s.ID,
		SessionID:               s.SessionID,
		FromHarness:             s.FromHarness,
		TargetHarness:           s.TargetHarness,
		TargetStartMode:         s.TargetStartMode,
		State:                   s.State,
		AgentHandoffStatus:      s.AgentHandoffStatus,
		SemanticHandoffIncluded: s.SemanticHandoffIncluded,
		SourceTranscriptStatus:  s.SourceTranscriptStatus,
		ErrorCode:               s.ErrorCode,
		RequestedAt:             s.RequestedAt,
		UpdatedAt:               s.UpdatedAt,
	}
}

func agentSwitchViews(switches []domain.AgentSwitch) []AgentSwitchView {
	out := make([]AgentSwitchView, 0, len(switches))
	for _, agentSwitch := range switches {
		out = append(out, agentSwitchView(agentSwitch))
	}
	return out
}

func sessionViews(r *http.Request, sessions []domain.Session) []SessionView {
	out := make([]SessionView, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, sessionView(r, s))
	}
	return out
}

func sessionArtifactFiles(r *http.Request, s domain.Session) []SessionArtifactView {
	out := make([]SessionArtifactView, 0, len(s.ArtifactFiles))
	for _, artifact := range s.ArtifactFiles {
		view := SessionArtifactView{
			Path:      artifact.Path,
			Name:      artifact.Name,
			Kind:      artifact.Kind,
			Size:      artifact.Size,
			UpdatedAt: artifact.UpdatedAt,
		}
		// A distinct host (not a shared path-prefix marker) gives every
		// artifact link an unambiguous source identity: it can never collide
		// with a real workspace file, unlike the legacy __ao_artifacts__/
		// path-prefix form previewFile/previewOriginEntry still accept for
		// backward compatibility.
		if rawURL, ok := artifactRawURL(r, s.ID, artifact.Path); ok {
			view.RawURL = rawURL
		}
		if artifact.Kind == domain.SessionArtifactHTML {
			view.PreviewURL, _ = previewutil.ArtifactFileURL("http://"+r.Host, s.ID, artifact.Path)
			view.InlineURL, _ = previewutil.InlineArtifactFileURL("http://"+r.Host, s.ID, artifact.Path)
		}
		out = append(out, view)
	}
	return out
}

// artifactRawURL builds the raw-bytes fetch URL for one artifact file on the
// artifact preview origin, with ?raw=true so a markdown artifact returns its
// source text rather than server-rendered HTML (matching the ?raw=true
// behavior serveOpenedPreviewFile already applies on every preview route).
func artifactRawURL(r *http.Request, id domain.SessionID, filePath string) (string, bool) {
	raw, err := previewutil.ArtifactFileURL("http://"+r.Host, id, filePath)
	if err != nil {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	q := u.Query()
	q.Set("raw", "true")
	u.RawQuery = q.Encode()
	return u.String(), true
}

func sessionPRFacts(prs []domain.PRFacts) []SessionPRFacts {
	out := make([]SessionPRFacts, 0, len(prs))
	for _, pr := range prs {
		out = append(out, SessionPRFacts{URL: pr.URL, Number: pr.Number, State: prState(pr), CI: pr.CI, Review: pr.Review, Mergeability: pr.Mergeability, ReviewComments: pr.ReviewComments, UpdatedAt: pr.UpdatedAt})
	}
	return out
}

func sessionPRSummaries(prs []sessionsvc.PRSummary) []SessionPRSummary {
	out := make([]SessionPRSummary, 0, len(prs))
	for _, pr := range prs {
		out = append(out, NewSessionPRSummary(pr))
	}
	return out
}

func workspaceFilesResponse(files sessionsvc.WorkspaceFiles) ListWorkspaceFilesResponse {
	return ListWorkspaceFilesResponse{
		SessionID:        files.SessionID,
		WorkspaceVersion: files.WorkspaceVersion,
		CompareBaseSHA:   files.CompareBaseSHA,
		CompareBaseRef:   files.CompareBaseRef,
		CompareMode:      files.CompareMode,
		Files:            workspaceFileSummariesResponse(files.Files),
		Truncated:        files.Truncated,
		Sections:         workspaceFileSectionsResponse(files.Sections),
		Commits:          workspaceCommitsResponse(files.Commits),
		CommitsTruncated: files.CommitsTruncated,
		Summary:          WorkspaceSummary(files.Summary),
		Degraded:         files.Degraded,
		DegradedCode:     files.DegradedCode,
		Ahead:            files.Ahead,
		Behind:           files.Behind,
	}
}

func workspaceManifestResponse(manifest sessionsvc.WorkspaceManifest) WorkspaceManifestResponse {
	return WorkspaceManifestResponse{
		SessionID:        manifest.SessionID,
		WorkspaceVersion: manifest.WorkspaceVersion,
		CompareBaseSHA:   manifest.CompareBaseSHA,
		CompareBaseRef:   manifest.CompareBaseRef,
		CompareMode:      manifest.CompareMode,
		Files:            workspaceFileSummariesResponse(manifest.Files),
		Sections:         workspaceFileSectionsResponse(manifest.Sections),
		Summary:          WorkspaceSummary(manifest.Summary),
		Truncated:        manifest.Truncated,
		Stale:            manifest.Stale,
		Refreshing:       manifest.Refreshing,
		Degraded:         manifest.Degraded,
		DegradedCode:     manifest.DegradedCode,
	}
}

func workspaceHistoryResponse(history sessionsvc.WorkspaceHistory) WorkspaceHistoryResponse {
	return WorkspaceHistoryResponse{
		SessionID: history.SessionID, Commits: workspaceCommitsResponse(history.Commits),
		CommitsTruncated: history.CommitsTruncated, Ahead: history.Ahead, Behind: history.Behind,
	}
}

func prFilesResponse(files sessionsvc.PRFiles) ListPRFilesResponse {
	return ListPRFilesResponse{
		SessionID:        files.SessionID,
		Files:            workspaceFileSummariesResponse(files.Files),
		Commits:          workspaceCommitsResponse(files.Commits),
		CommitsTruncated: files.CommitsTruncated,
		Truncated:        files.Truncated,
		Summary:          WorkspaceSummary(files.Summary),
	}
}

func workspaceFileSummaryResponse(file sessionsvc.WorkspaceFileSummary) WorkspaceFileSummary {
	return WorkspaceFileSummary{
		Path:            file.Path,
		PreviousPath:    file.PreviousPath,
		Status:          file.Status,
		Additions:       file.Additions,
		Deletions:       file.Deletions,
		Size:            file.Size,
		Binary:          file.Binary,
		Editable:        file.Editable,
		FileFingerprint: file.FileFingerprint,
	}
}

func workspaceFileSummariesResponse(files []sessionsvc.WorkspaceFileSummary) []WorkspaceFileSummary {
	out := make([]WorkspaceFileSummary, 0, len(files))
	for _, file := range files {
		out = append(out, workspaceFileSummaryResponse(file))
	}
	return out
}

func workspaceFileSectionsResponse(sections sessionsvc.WorkspaceFileSections) WorkspaceFileSections {
	return WorkspaceFileSections{
		Staged:    workspaceFileSummariesResponse(sections.Staged),
		Unstaged:  workspaceFileSummariesResponse(sections.Unstaged),
		Untracked: workspaceFileSummariesResponse(sections.Untracked),
		Committed: workspaceFileSummariesResponse(sections.Committed),
	}
}

func workspaceCommitsResponse(commits []sessionsvc.CommitSummary) []WorkspaceCommitSummary {
	out := make([]WorkspaceCommitSummary, 0, len(commits))
	for _, commit := range commits {
		out = append(out, WorkspaceCommitSummary{
			SHA:       commit.SHA,
			Subject:   commit.Subject,
			Author:    commit.Author,
			Timestamp: commit.Timestamp,
			Files:     workspaceFileSummariesResponse(commit.Files),
		})
	}
	return out
}

func workspaceFileResponse(file sessionsvc.WorkspaceFileDetail) WorkspaceFileResponse {
	return WorkspaceFileResponse{
		SessionID:        file.SessionID,
		Path:             file.Path,
		PreviousPath:     file.PreviousPath,
		Status:           file.Status,
		Additions:        file.Additions,
		Deletions:        file.Deletions,
		Size:             file.Size,
		Binary:           file.Binary,
		Deleted:          file.Deleted,
		Editable:         file.Editable,
		ImageMediaType:   file.ImageMediaType,
		Content:          file.Content,
		ContentTruncated: file.ContentTruncated,
		Diff:             file.Diff,
		DiffTruncated:    file.DiffTruncated,
		CompareBaseSHA:   file.CompareBaseSHA,
		CompareBaseRef:   file.CompareBaseRef,
		CompareMode:      file.CompareMode,
		WorkspaceVersion: file.WorkspaceVersion,
		FileFingerprint:  file.FileFingerprint,
	}
}

func workspaceDiffsResponse(diffs sessionsvc.WorkspaceDiffs) WorkspaceDiffsResponse {
	groups := make([]WorkspaceDiffGroupResponse, 0, len(diffs.Groups))
	for _, group := range diffs.Groups {
		deferred := make([]WorkspaceDiffDeferredResponse, 0, len(group.Deferred))
		for _, item := range group.Deferred {
			deferred = append(deferred, WorkspaceDiffDeferredResponse{Path: item.Path, Reason: item.Reason})
		}
		groupErrors := make([]WorkspaceDiffErrorResponse, 0, len(group.Errors))
		for _, item := range group.Errors {
			groupErrors = append(groupErrors, WorkspaceDiffErrorResponse{Code: item.Code, Message: item.Message})
		}
		groups = append(groups, WorkspaceDiffGroupResponse{Repository: group.Repository, Patch: group.Patch, Truncated: group.Truncated, IncludedPaths: group.IncludedPaths, Deferred: deferred, Errors: groupErrors})
	}
	return WorkspaceDiffsResponse{SessionID: diffs.SessionID, WorkspaceVersion: diffs.WorkspaceVersion, Groups: groups}
}

func workspaceFileRevisionResponse(revision sessionsvc.WorkspaceFileRevision) WorkspaceFileRevisionResponse {
	return WorkspaceFileRevisionResponse{
		SessionID: revision.SessionID, Path: revision.Path, Side: revision.Side, Revision: revision.Revision,
		WorkspaceVersion: revision.WorkspaceVersion, MediaType: revision.MediaType, Encoding: revision.Encoding,
		Size: revision.Size, Exists: revision.Exists, Binary: revision.Binary, Truncated: revision.Truncated, Content: revision.Content,
	}
}

func workspaceFileSearchResponse(result sessionsvc.WorkspaceFileSearch) WorkspaceFileSearchResponse {
	items := make([]WorkspaceFileSearchResultResponse, 0, len(result.Results))
	for _, item := range result.Results {
		items = append(items, WorkspaceFileSearchResultResponse{Path: item.Path, Status: item.Status, Size: item.Size, Binary: item.Binary, FileFingerprint: item.FileFingerprint})
	}
	return WorkspaceFileSearchResponse{SessionID: result.SessionID, Query: result.Query, Results: items, NextCursor: result.NextCursor, Truncated: result.Truncated}
}

func workspaceTreeResponse(tree sessionsvc.WorkspaceTree) ListWorkspaceTreeResponse {
	out := make([]WorkspaceTreeEntry, 0, len(tree.Entries))
	for _, entry := range tree.Entries {
		out = append(out, WorkspaceTreeEntry{
			Name:       entry.Name,
			Path:       entry.Path,
			Type:       entry.Type,
			Status:     entry.Status,
			HasChanges: entry.HasChanges,
			Size:       entry.Size,
			Binary:     entry.Binary,
		})
	}
	return ListWorkspaceTreeResponse{
		SessionID: tree.SessionID,
		Path:      tree.Path,
		Entries:   out,
		Truncated: tree.Truncated,
	}
}

func prState(pr domain.PRFacts) string {
	switch {
	case pr.Merged:
		return string(domain.PRStateMerged)
	case pr.Closed:
		return string(domain.PRStateClosed)
	case pr.Draft:
		return string(domain.PRStateDraft)
	default:
		return string(domain.PRStateOpen)
	}
}

func nonNilSessionIDs(ids []domain.SessionID) []domain.SessionID {
	if ids == nil {
		return []domain.SessionID{}
	}
	return ids
}
