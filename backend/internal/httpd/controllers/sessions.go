package controllers

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	previewutil "github.com/aoagents/agent-orchestrator/backend/internal/preview"
	"github.com/aoagents/agent-orchestrator/backend/internal/previewserver"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
	usagesvc "github.com/aoagents/agent-orchestrator/backend/internal/service/usage"
	"github.com/aoagents/agent-orchestrator/backend/internal/workspacewatch"
)

const (
	maxPromptLen      = 4096
	maxMessageLen     = 4096
	maxModelLen       = 256
	maxDisplayNameLen = 20
	// Attachment limits guard the daemon against oversized spawn bodies. Files
	// are pasted/dropped into the task brief and inlined as base64 in the JSON
	// body, so the caps are deliberately conservative.
	maxAttachments      = 8
	maxAttachmentBytes  = 10 << 20 // 10 MiB per file, decoded
	maxAttachmentsBytes = 25 << 20 // 25 MiB total, decoded
	// maxSpawnBodyBytes bounds the raw request body before it is decoded. The
	// per-attachment and total caps above only apply after the whole body is
	// materialized, so without this an oversized body (base64 inflates the
	// decoded total by ~4/3) would allocate in full first. Derived from the
	// decoded total plus headroom for the prompt and JSON envelope.
	maxSpawnBodyBytes = maxAttachmentsBytes*4/3 + (2 << 20)
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
	SpawnOrchestrator(ctx context.Context, projectID domain.ProjectID, clean bool, requestedMode domain.SessionMode) (domain.Session, error)
	Get(ctx context.Context, id domain.SessionID) (domain.Session, error)
	Restore(ctx context.Context, id domain.SessionID) (sessionsvc.RestoreOutcome, error)
	ResumeAgent(ctx context.Context, id domain.SessionID) (sessionsvc.ResumeAgentOutcome, error)
	Kill(ctx context.Context, id domain.SessionID) (bool, error)
	RollbackSpawn(ctx context.Context, id domain.SessionID) (sessionsvc.RollbackOutcome, error)
	Cleanup(ctx context.Context, project domain.ProjectID) (sessionsvc.CleanupOutcome, error)
	Rename(ctx context.Context, id domain.SessionID, displayName string) error
	SetPreview(ctx context.Context, id domain.SessionID, previewURL string) (domain.Session, error)
	SetTerminateOnPRMerge(ctx context.Context, id domain.SessionID, terminate bool) (domain.Session, error)
	SetAutoInjectReview(ctx context.Context, id domain.SessionID, autoInject bool) (domain.Session, error)
	SetReviewerHarness(ctx context.Context, id domain.SessionID, harness domain.ReviewerHarness) (domain.Session, error)
	Send(ctx context.Context, id domain.SessionID, message string) error
	DelegateTask(ctx context.Context, in sessionsvc.DelegateTaskInput) (sessionsvc.DelegateTaskOutcome, error)
	ListPRSummaries(ctx context.Context, id domain.SessionID) ([]sessionsvc.PRSummary, error)
	ClaimPR(ctx context.Context, id domain.SessionID, ref string, opts sessionsvc.ClaimPROptions) (sessionsvc.ClaimPRResult, error)
	StageAttachments(ctx context.Context, id domain.SessionID, attachments []ports.SpawnAttachment) ([]string, error)
	WorkspaceWatchPaths(ctx context.Context, id domain.SessionID) ([]string, error)
	ListWorkspaceFiles(ctx context.Context, id domain.SessionID) (sessionsvc.WorkspaceFiles, error)
	GetWorkspaceFile(ctx context.Context, id domain.SessionID, path string) (sessionsvc.WorkspaceFileDetail, error)
	InvalidateWorkspaceCache(id domain.SessionID)
	Pin(ctx context.Context, id domain.SessionID) (domain.Session, error)
	Unpin(ctx context.Context, id domain.SessionID) (domain.Session, error)
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
	Start(ctx context.Context, sessionID domain.SessionID, workspacePath, configurationName string) (previewserver.Status, error)
	Stop(ctx context.Context, sessionID domain.SessionID) (previewserver.Status, error)
	Status(sessionID domain.SessionID) previewserver.Status
}

// SessionCapabilityValidator verifies the daemon-issued token injected only
// into the owning worker session.
type SessionCapabilityValidator interface {
	Valid(sessionID domain.SessionID, token, verifier string) bool
}

// UsageHookRecorder consumes transcript metadata from the same native hook
// callback without changing activity-state semantics.
type UsageHookRecorder interface {
	RecordHook(ctx context.Context, id domain.SessionID, signal usagesvc.HookSignal) error
}

// SessionsController owns the session routes. Nil keeps routes registered but
// returns OpenAPI-backed 501s.
type SessionsController struct {
	Svc           SessionService
	Activity      ActivityRecorder
	Usage         UsageHookRecorder
	PreviewServer ManagedPreviewServer
	Capabilities  SessionCapabilityValidator
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
	r.Post("/sessions/{sessionId}/attachments", c.stageAttachments)
	r.Get("/sessions/{sessionId}/workspace/files", c.listWorkspaceFiles)
	r.Get("/sessions/{sessionId}/workspace/file", c.getWorkspaceFile)
	r.Get("/sessions/{sessionId}/pr", c.listPRs)
	r.Post("/sessions/{sessionId}/pr/claim", c.claimPR)
	r.Patch("/sessions/{sessionId}", c.rename)
	r.Patch("/sessions/{sessionId}/merge-policy", c.setMergePolicy)
	r.Patch("/sessions/{sessionId}/auto-inject-review", c.setAutoInjectReviewPolicy)
	r.Put("/sessions/{sessionId}/reviewer", c.setReviewer)
	r.Post("/sessions/{sessionId}/restore", c.restore)
	r.Post("/sessions/{sessionId}/resume-agent", c.resumeAgent)
	r.Get("/sessions/{sessionId}/interface-transition", c.interfaceTransitionStatus)
	r.Post("/sessions/{sessionId}/interface-transition", c.startInterfaceTransition)
	r.Delete("/sessions/{sessionId}/interface-transition", c.cancelInterfaceTransition)
	r.Post("/sessions/{sessionId}/kill", c.kill)
	r.Post("/sessions/{sessionId}/rollback", c.rollback)
	r.Post("/sessions/{sessionId}/send", c.send)
	r.Post("/sessions/{sessionId}/activity", c.activity)
	r.Post("/sessions/{sessionId}/pin", c.pin)
	r.Delete("/sessions/{sessionId}/pin", c.unpin)
	r.Get("/orchestrators", c.listOrchestrators)
	r.Post("/orchestrators", c.spawnOrchestrator)
	r.Post("/orchestrators/delegate", c.delegateTask)
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
	envelope.WriteJSON(w, http.StatusOK, ListSessionsResponse{Sessions: sessionViews(sessions)})
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
	if in.ProjectID == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "PROJECT_ID_REQUIRED", "projectId is required", nil)
		return
	}
	mode, err := domain.ParseSessionMode(string(in.Mode))
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", "SESSION_MODE_INVALID", err.Error(), nil)
		return
	}
	in.Mode = mode
	if len(in.Prompt) > maxPromptLen {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "PROMPT_TOO_LONG", "prompt is too long", nil)
		return
	}
	// displayName is optional at the API (the desktop new-task dialog omits it
	// and the read model falls back to the session id). `ao spawn` makes it
	// required CLI-side. When present, it is held to the same length cap here so
	// a direct API call cannot exceed it.
	displayName := strings.TrimSpace(in.DisplayName)
	if utf8.RuneCountInString(displayName) > maxDisplayNameLen {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "DISPLAY_NAME_TOO_LONG", "displayName must be 20 characters or fewer", nil)
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
	sess, promptBytes, systemPromptBytes, err := c.Svc.Spawn(r.Context(), ports.SpawnConfig{ProjectID: in.ProjectID, IssueID: in.IssueID, Kind: in.Kind, Harness: in.Harness, Branch: in.Branch, RequestedMode: in.Mode, Prompt: in.Prompt, DisplayName: displayName, Attachments: attachments})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, SpawnSessionResponse{Session: sessionView(sess), PromptBytes: promptBytes, SystemPromptBytes: systemPromptBytes})
}

// spawnAttachmentError carries a client-facing API error code + message for a
// rejected attachment payload.
type spawnAttachmentError struct {
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
		"image/jpeg": ".jpg",
		"image/jpg":  ".jpg",
		"text/plain": ".txt",
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

// decodeSpawnAttachments validates and base64-decodes the inline file
// attachments from a spawn request, enforcing count, per-file, and total size
// caps. It accepts any MIME type except explicitly blocked ones (e.g., SVG
// for security reasons). Returns a nil slice when there are no attachments.
func decodeSpawnAttachments(in []SpawnAttachmentInput) ([]ports.SpawnAttachment, *spawnAttachmentError) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > maxAttachments {
		return nil, &spawnAttachmentError{"TOO_MANY_ATTACHMENTS", "too many attachments"}
	}
	out := make([]ports.SpawnAttachment, 0, len(in))
	total := 0
	for _, a := range in {
		mimeType := strings.ToLower(strings.TrimSpace(a.MimeType))

		// Check for blocked MIME types (e.g., SVG for security)
		if blockedAttachmentMimes[mimeType] {
			return nil, &spawnAttachmentError{"UNSUPPORTED_ATTACHMENT_TYPE", "unsupported attachment type"}
		}

		// Get file extension for the MIME type
		ext := extensionForMimeType(mimeType)

		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(a.Data))
		if err != nil {
			return nil, &spawnAttachmentError{"INVALID_ATTACHMENT_DATA", "attachment data is not valid base64"}
		}
		if len(data) == 0 {
			return nil, &spawnAttachmentError{"INVALID_ATTACHMENT_DATA", "attachment is empty"}
		}
		if len(data) > maxAttachmentBytes {
			return nil, &spawnAttachmentError{"ATTACHMENT_TOO_LARGE", "attachment is too large"}
		}
		total += len(data)
		if total > maxAttachmentsBytes {
			return nil, &spawnAttachmentError{"ATTACHMENTS_TOO_LARGE", "attachments are too large"}
		}
		out = append(out, ports.SpawnAttachment{Ext: ext, Data: data})
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
	envelope.WriteJSON(w, http.StatusOK, SessionResponse{Session: sessionView(sess)})
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
	c.serveWorkspacePreviewFile(w, r, sess.Metadata.WorkspacePath, chi.URLParam(r, "*"))
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
	id, ok := previewutil.SessionIDFromHost(r.Host)
	if !ok {
		return false
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
	entry, ok := previewOriginEntry(sess)
	if !ok {
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "NO_PREVIEW_ENTRY", "No preview entry point found in session workspace", nil)
		return true
	}
	asset := previewOriginAssetPath(entry, r.URL.Path)
	c.serveWorkspacePreviewFile(w, r, sess.Metadata.WorkspacePath, asset)
	return true
}

func previewOriginEntry(sess domain.Session) (string, bool) {
	if entry, ok := previewutil.StoredWorkspaceEntry(sess.Metadata.PreviewURL, sess.ID); ok {
		if stored, exists := previewutil.EntryAtPath(sess.Metadata.WorkspacePath, entry); exists {
			return stored.Path, true
		}
	}
	return discoverPreviewEntry(sess.Metadata.WorkspacePath)
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

// serveWorkspacePreviewFile is the single serving path for both the legacy API
// route and isolated preview origins. OpenRoot keeps symlink traversal and the
// subsequent read on the same workspace-confined file handle.
func (c *SessionsController) serveWorkspacePreviewFile(w http.ResponseWriter, r *http.Request, workspacePath, assetPath string) {
	file, info, clean, err := previewutil.OpenWorkspaceFile(workspacePath, assetPath)
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "PREVIEW_FILE_NOT_FOUND", "Preview file not found", nil)
		return
	}
	defer func() { _ = file.Close() }()
	if !previewutil.IsMarkdownPath(clean) {
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
	file, err := c.Svc.GetWorkspaceFile(r.Context(), sessionID(r), relPath)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, workspaceFileResponse(file))
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

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
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
			if _, err := fmt.Fprint(w, "event: workspace_changed\ndata: {}\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-keepAlive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
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
			previewURL, resolveErr = resolvePreviewTarget(r, sessionID(r), sess.Metadata.WorkspacePath, existing)
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
		previewURL, resolveErr = resolvePreviewTarget(r, sessionID(r), sess.Metadata.WorkspacePath, previewURL)
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
	envelope.WriteJSON(w, http.StatusOK, SessionResponse{Session: sessionView(updated)})
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
	envelope.WriteJSON(w, http.StatusOK, SessionResponse{Session: sessionView(updated)})
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
	previous := c.PreviewServer.Status(sessionID(r))
	status, err := c.PreviewServer.Start(
		r.Context(),
		sessionID(r),
		sess.Metadata.WorkspacePath,
		strings.TrimSpace(in.Configuration),
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
	if !c.Capabilities.Valid(
		id,
		strings.TrimSpace(r.Header.Get(browserCapabilityHeader)),
		sess.Metadata.BrowserCapabilityVerifier,
	) {
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
	return true
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
	prs, err := c.Svc.ListPRSummaries(r.Context(), sessionID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ListSessionPRsResponse{SessionID: sessionID(r), PRs: sessionPRSummaries(prs)})
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
		Session:            sessionView(sess),
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
		Session:          sessionView(sess),
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
	sess, err := c.Svc.SetReviewerHarness(r.Context(), sessionID(r), in.Harness)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, SessionResponse{Session: sessionView(sess)})
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
	envelope.WriteJSON(w, http.StatusOK, RestoreSessionResponse{OK: true, SessionID: sessionID(r), RestoreMode: out.Mode, Session: sessionView(out.Session)})
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
	envelope.WriteJSON(w, http.StatusOK, SessionResponse{Session: sessionView(sess)})
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
	envelope.WriteJSON(w, http.StatusOK, SessionResponse{Session: sessionView(sess)})
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
		Session:    sessionView(out.Session),
	})
}

func (c *SessionsController) kill(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/kill")
		return
	}
	freed, err := c.Svc.Kill(r.Context(), sessionID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, KillSessionResponse{OK: true, SessionID: sessionID(r), Freed: freed})
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
	envelope.WriteJSON(w, http.StatusOK, CleanupSessionsResponse{OK: true, Cleaned: out.Cleaned, Skipped: skipped})
}

func (c *SessionsController) send(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/send")
		return
	}
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
	message := domain.SanitizeControlChars(in.Message)
	if err := c.Svc.Send(r.Context(), sessionID(r), message); err != nil {
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
	if in.ProjectID == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "PROJECT_ID_REQUIRED", "projectId is required", nil)
		return
	}
	if len(in.Brief) > maxPromptLen {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "TASK_TOO_LONG", "Task is too long", nil)
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
	attachments, attachErr := decodeSpawnAttachments(in.Attachments)
	if attachErr != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", attachErr.code, attachErr.message, nil)
		return
	}

	out, err := c.Svc.DelegateTask(r.Context(), sessionsvc.DelegateTaskInput{
		ProjectID:      in.ProjectID,
		Brief:          domain.SanitizeControlChars(in.Brief),
		RequestedAgent: in.Agent,
		Model:          domain.SanitizeControlChars(strings.TrimSpace(in.Model)),
		RequestedMode:  in.Mode,
		Attachments:    attachments,
	})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, DelegateTaskResponse{OK: true, WorkerID: out.WorkerID, OrchestratorID: out.OrchestratorID})
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
	if state == "" && agentSessionID == "" && in.Usage == nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "ACTIVITY_OR_SESSION_ID_REQUIRED", "Activity state or agent session ID is required", nil)
		return
	}
	// The correlation fields ride the same lenient decode: absent on old CLIs.
	// They are externally-supplied strings headed for logs and in-memory maps,
	// so sanitize control chars and cap their length (a truncated id could
	// never match its pre/post counterpart, so overlong values are dropped by
	// the CLI; the cap here is defense against non-AO callers).
	sig := ports.ActivitySignal{
		Valid:          state != "",
		State:          state,
		Event:          capActivityMeta(domain.SanitizeControlChars(in.Event)),
		ToolName:       capActivityMeta(domain.SanitizeControlChars(in.ToolName)),
		ToolUseID:      capActivityMeta(domain.SanitizeControlChars(in.ToolUseID)),
		AgentSessionID: agentSessionID,
		LaunchID:       capActivityMeta(domain.SanitizeControlChars(strings.TrimSpace(in.LaunchID))),
	}
	if c.Activity != nil && (sig.Valid || sig.AgentSessionID != "") {
		if err := c.Activity.ApplyActivitySignal(r.Context(), sessionID(r), sig); err != nil {
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
	envelope.WriteJSON(w, http.StatusOK, SetActivityResponse{OK: true, SessionID: sessionID(r), State: in.State})
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
	sess, err := c.Svc.SpawnOrchestrator(r.Context(), in.ProjectID, in.Clean, in.Mode)
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
	envelope.WriteJSON(w, http.StatusOK, ListSessionsResponse{Sessions: sessionViews(sessions)})
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
	envelope.WriteJSON(w, http.StatusOK, SessionResponse{Session: sessionView(sess)})
}

func sessionID(r *http.Request) domain.SessionID {
	return domain.SessionID(chi.URLParam(r, "sessionId"))
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
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_PR_REF", "PR reference must be a github.com PR URL or a number", nil)
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
		envelope.WriteAPIError(w, r, http.StatusUnprocessableEntity, "unprocessable", "PR_PROJECT_MISMATCH", "PR does not belong to the session project", nil)
	case errors.Is(err, sessionsvc.ErrSCMUnavailable):
		envelope.WriteAPIError(w, r, http.StatusServiceUnavailable, "unavailable", "SCM_UNAVAILABLE", "SCM unavailable", nil)
	default:
		envelope.WriteError(w, r, err)
	}
}

func discoverPreviewEntry(workspacePath string) (string, bool) {
	entry, ok := previewutil.DiscoverEntry(workspacePath)
	return entry.Path, ok
}

// resolveLocalPreview maps a workspace-local path (e.g. "index.html" or
// "./dist/index.html") to its preview/files proxy URL when the path resolves to
// a regular file inside the session workspace. It returns ok=false for anything
// that already looks like a URL (an http(s)/file scheme, or a host:port dev
// server) and for paths that escape the workspace or do not point at a file, so
// the caller keeps those targets verbatim.
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

func resolvePreviewTarget(r *http.Request, id domain.SessionID, workspacePath, raw string) (string, error) {
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
	if resolved, ok, err := resolveLocalPreview(r, id, workspacePath, raw); ok || err != nil {
		return resolved, err
	}
	return raw, nil
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

func sessionView(s domain.Session) SessionView {
	return SessionView{
		Session:         s,
		Branch:          s.Metadata.Branch,
		PreviewURL:      s.Metadata.PreviewURL,
		PreviewRevision: s.Metadata.PreviewRevision,
		PRs:             sessionPRFacts(s.PRs),
	}
}

func sessionViews(sessions []domain.Session) []SessionView {
	out := make([]SessionView, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, sessionView(s))
	}
	return out
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
	out := make([]WorkspaceFileSummary, 0, len(files.Files))
	for _, file := range files.Files {
		out = append(out, WorkspaceFileSummary{
			Path:         file.Path,
			PreviousPath: file.PreviousPath,
			Status:       file.Status,
			Additions:    file.Additions,
			Deletions:    file.Deletions,
			Size:         file.Size,
			Binary:       file.Binary,
		})
	}
	return ListWorkspaceFilesResponse{
		SessionID:      files.SessionID,
		CompareBaseSHA: files.CompareBaseSHA,
		CompareBaseRef: files.CompareBaseRef,
		CompareMode:    files.CompareMode,
		Files:          out,
		Truncated:      files.Truncated,
	}
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
		Content:          file.Content,
		ContentTruncated: file.ContentTruncated,
		Diff:             file.Diff,
		DiffTruncated:    file.DiffTruncated,
		CompareBaseSHA:   file.CompareBaseSHA,
		CompareBaseRef:   file.CompareBaseRef,
		CompareMode:      file.CompareMode,
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
