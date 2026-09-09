package controllers

import (
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/devimport"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/legacyimport"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	agentsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/agent"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/agentauth"
	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/systemcheck"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/systeminstall"

	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

// HTTP response envelopes for the projects surface — the SINGLE definition of
// each wire shape. The handlers encode these (envelope.WriteJSON), and
// apispec.Build reflects these same types into openapi.yaml, so the served
// contract and the generated spec can't disagree. The request side needs no
// wrappers: handlers decode the body straight into the project commands
// (projectsvc.AddInput), which apispec also reflects.

// ProjectIDParam is the {id} path parameter shared by the /projects/{id}
// routes. Handlers read it via chi.URLParam (see projectID); it is declared here
// so every wire input/output shape has one home, and apispec.Build reflects it
// as the path parameter.
type ProjectIDParam struct {
	ID string `path:"id" description:"Project identifier (registry key)."`
}

// AgentIDParam is the {agent} path parameter for one-agent catalog probes.
type AgentIDParam struct {
	Agent string `path:"agent" description:"Agent adapter identifier."`
}

// ListAgentAuthPlansResponse is the display-safe authentication catalog.
type ListAgentAuthPlansResponse struct {
	Plans []agentauth.Plan `json:"plans"`
}

// StartAgentAuthResponse returns the native terminal opened for authentication.
type StartAgentAuthResponse struct {
	AgentID       string                `json:"agentId"`
	Action        agentauth.Action      `json:"action"`
	Guidance      string                `json:"guidance,omitempty"`
	TerminalInput string                `json:"terminalInput,omitempty"`
	Terminal      ShellTerminalResponse `json:"terminal"`
}

// CodexAccountIDParam documents a Codex account route identifier.
type CodexAccountIDParam struct {
	AccountID string `path:"accountId" description:"AO Codex account identifier."`
}

// CodexAccountLoginIDParam documents a Codex login operation route identifier.
type CodexAccountLoginIDParam struct {
	OperationID string `path:"operationId" description:"In-memory Codex account login operation identifier."`
}

// ListProjectsResponse is the body of GET /api/v1/projects.
type ListProjectsResponse struct {
	Projects []projectsvc.Summary `json:"projects"`
}

// ProjectResponse is the { project } body shared by POST /projects (201).
type ProjectResponse struct {
	Project projectsvc.Project `json:"project"`
}

// GetProjectResponse is the { status, project } body of GET /projects/{id},
// where project is oneOf Project|Degraded discriminated by status.
type GetProjectResponse struct {
	Status  string            `json:"status" enum:"ok,degraded"`
	Project ProjectOrDegraded `json:"project"`
}

// ProjectOrDegraded is the discriminated `project` field: exactly one of
// Project/Degraded is set. It marshals as whichever is present (so the handler
// emits the right object) and exposes the oneOf variants to the spec reflector
// (so apispec.Build emits `oneOf: [Project, Degraded]`) — one type, both jobs.
type ProjectOrDegraded struct {
	Project  *projectsvc.Project
	Degraded *projectsvc.Degraded
}

// MarshalJSON encodes whichever variant is set (Project or Degraded).
func (p ProjectOrDegraded) MarshalJSON() ([]byte, error) {
	switch {
	case p.Degraded != nil:
		return json.Marshal(p.Degraded)
	case p.Project != nil:
		return json.Marshal(p.Project)
	default:
		// Unreachable in practice: the handler validates the GetResult via
		// newGetProjectResponse and writes a 500 before committing the 200
		// status, so this never encodes. Kept as a last-resort backstop —
		// erroring is still better than emitting a contract-breaking `null`,
		// though by here the status is already sent, so the real guard is
		// upstream.
		return nil, errEmptyProjectOrDegraded
	}
}

// errEmptyProjectOrDegraded marks a GetResult that set neither variant — a
// Manager-contract violation. newGetProjectResponse returns it so the handler
// can map it to a 500 before any response bytes are written.
var errEmptyProjectOrDegraded = errors.New("controllers: GetResult has neither Project nor Degraded set")

// JSONSchemaOneOf is read by swaggest's reflector (apispec.Build) to emit the
// oneOf for this field; it is not used at runtime.
func (ProjectOrDegraded) JSONSchemaOneOf() []interface{} {
	return []interface{}{projectsvc.Project{}, projectsvc.Degraded{}}
}

// newGetProjectResponse maps the internal GetResult onto the wire envelope —
// the explicit project→httpd boundary the result type exists for. It errors
// when the result sets neither variant, so the handler can return a clean 500
// BEFORE writing the 200 status rather than flushing a truncated body.
func newGetProjectResponse(res projectsvc.GetResult) (GetProjectResponse, error) {
	if res.Project == nil && res.Degraded == nil {
		return GetProjectResponse{}, errEmptyProjectOrDegraded
	}
	return GetProjectResponse{
		Status:  res.Status,
		Project: ProjectOrDegraded{Project: res.Project, Degraded: res.Degraded},
	}, nil
}

// SessionIDParam is the {sessionId} path parameter shared by session routes.
type SessionIDParam struct {
	SessionID string `path:"sessionId" description:"Session identifier, e.g. project-1."`
}

// AgentSwitchIDParam is the {switchId} path parameter for one durable switch saga.
type AgentSwitchIDParam struct {
	SwitchID string `path:"switchId" description:"Durable agent-switch identifier."`
}

// SessionInterfaceTransitionIDParam is the {transitionId} path parameter for a
// durable interface handoff.
type SessionInterfaceTransitionIDParam struct {
	TransitionID string `path:"transitionId" description:"Durable interface-transition identifier."`
}

// ListSessionsQuery is the query string accepted by GET /api/v1/sessions.
type ListSessionsQuery struct {
	Project          string `query:"project,omitempty" description:"Project id filter."`
	Active           *bool  `query:"active,omitempty" description:"When true, return non-terminated sessions; when false, return terminated sessions."`
	OrchestratorOnly *bool  `query:"orchestratorOnly,omitempty" description:"When true, return only orchestrator sessions."`
	Fresh            *bool  `query:"fresh,omitempty" description:"When true, return only fresh non-terminated sessions."`
}

// CleanupSessionsQuery is the query string accepted by POST /api/v1/sessions/cleanup.
type CleanupSessionsQuery struct {
	Project string `query:"project,omitempty" description:"Project id filter. When omitted, clean terminated sessions across all projects."`
}

// WorkspaceFileQuery is the query string accepted by GET /api/v1/sessions/{sessionId}/workspace/file.
type WorkspaceFileQuery struct {
	Path string `query:"path" description:"Session-worktree-relative file path."`
	// Section scopes the diff to one git-state section (see WorkspaceFileSections):
	// staged compares the index against HEAD, unstaged compares the worktree
	// against the index. A file can carry independent changes in both. Omit (or
	// pass committed/untracked) to diff the worktree against the compare base,
	// as before this field existed.
	Section string `query:"section,omitempty" enum:"committed,staged,unstaged,untracked" description:"Git-state section the file was opened from (see WorkspaceFileSections). staged diffs the index against HEAD; unstaged diffs the worktree against the index; omitted/committed/untracked diff the worktree against the compare base."`
}

// WorkspaceFileBlobQuery is the query string accepted by GET /api/v1/sessions/{sessionId}/workspace/file/blob.
type WorkspaceFileBlobQuery struct {
	// The handler rejects a missing path with WORKSPACE_PATH_REQUIRED, so mark it
	// required: query params carry no json tag, and requiredFromJSONTag only
	// derives `required` from those.
	Path string `query:"path" required:"true" description:"Session-worktree-relative file path."`
	Side string `query:"side,omitempty" enum:"before,after" description:"Which revision to read: the compare base (before) or the session worktree (after). Defaults to after."`
	V    string `query:"v,omitempty" description:"Cache-busting token. Ignored by the server; the response is never cached."`
}

// WorkspaceTreeQuery is the query string accepted by GET /api/v1/sessions/{sessionId}/workspace/tree.
type WorkspaceTreeQuery struct {
	Path string `query:"path,omitempty" description:"Directory path relative to the session workspace root. Empty or omitted lists the root."`
}

// ListWorkspaceTreeResponse is the body of GET /api/v1/sessions/{sessionId}/workspace/tree.
// Unlike ListWorkspaceFilesResponse (every changed file, whole worktree), this
// is one directory level of the full worktree — tracked and
// untracked-but-not-ignored — for lazily expanding a file explorer.
type ListWorkspaceTreeResponse struct {
	SessionID domain.SessionID     `json:"sessionId"`
	Path      string               `json:"path"`
	Entries   []WorkspaceTreeEntry `json:"entries"`
	Truncated bool                 `json:"truncated"`
}

// WorkspaceTreeEntry is one immediate child of a listed directory.
type WorkspaceTreeEntry struct {
	Name string                            `json:"name"`
	Path string                            `json:"path"`
	Type sessionsvc.WorkspaceTreeEntryType `json:"type" enum:"file,dir"`
	// Status is set for files only; omitted for directories.
	Status sessionsvc.WorkspaceFileStatus `json:"status,omitempty" enum:"unmodified,modified,added,deleted,renamed"`
	// HasChanges is set for directories only: true when a descendant file is
	// non-unmodified, so a collapsed folder can still show it contains changes.
	HasChanges bool  `json:"hasChanges,omitempty"`
	Size       int64 `json:"size,omitempty"`
	Binary     bool  `json:"binary,omitempty"`
}

// SessionView is the session wire shape: the domain read model plus the
// display-safe branch name and the session's attributed pull requests in the
// curated SessionPRFacts shape. One session can own many PRs (e.g. a stack), so
// prs is a list. The embedded domain.Session.Metadata and domain.Session.PRs
// fields are json:"-"; these curated fields are what serialize.
type SessionView struct {
	domain.Session
	Branch string `json:"branch,omitempty"`
	// TerminalGeneration is an opaque renderer fence. A restarted controller
	// may deliberately reuse its terminal handle; clients must still discard the
	// old attachment when this value changes.
	TerminalGeneration string `json:"terminalGeneration,omitempty"`
	// PreviewURL is the browser preview target the desktop app opens for this
	// session, set via POST /sessions/{sessionId}/preview. Empty (omitted) when
	// no preview has been requested. Pulled from the json:"-" domain Metadata.
	PreviewURL string `json:"previewUrl,omitempty"`
	// PreviewRevision bumps on every `ao preview` call (even when previewUrl is
	// unchanged) so the desktop browser panel can re-navigate / refresh on a
	// repeated preview of the same target. Pulled from the json:"-" domain
	// Metadata.
	PreviewRevision int64 `json:"previewRevision,omitempty"`
	// Model is the agent model this session resolved to at spawn time. Empty
	// means the agent's default model. Pulled from the json:"-" domain Metadata.
	Model string `json:"model,omitempty"`
	// LastUserMessageAt is the latest real user-authored task direction time.
	// Lifecycle and internal automation updates do not advance it.
	LastUserMessageAt *time.Time       `json:"lastUserMessageAt,omitempty"`
	PRs               []SessionPRFacts `json:"prs"`
	ActiveAgentSwitch *AgentSwitchView `json:"activeAgentSwitch,omitempty"`
}

// ListSessionsResponse is the body of GET /api/v1/sessions.
type ListSessionsResponse struct {
	Sessions []SessionView `json:"sessions"`
}

// SpawnSessionRequest is the body of POST /api/v1/sessions.
type SpawnSessionRequest struct {
	ProjectID       domain.ProjectID       `json:"projectId"`
	IssueID         domain.IssueID         `json:"issueId,omitempty"`
	TrackerProvider domain.TrackerProvider `json:"trackerProvider,omitempty" enum:"github,gitlab"`
	Kind            domain.SessionKind     `json:"kind,omitempty" enum:"worker,orchestrator"`
	Harness         domain.AgentHarness    `json:"harness,omitempty" enum:"claude-code,codex,aider,opencode,grok,droid,amp,agy,crush,cursor,qwen,copilot,goose,auggie,continue,devin,cline,kimi,muse,kiro,kilocode,vibe,pi,kimchi,omp,prime-agent,autohand"`
	Branch          string                 `json:"branch,omitempty"`
	// Mode picks the conversation controller: chat talks to the agent over a
	// structured connection, tui opens the agent's native terminal interface.
	// Omitted resolves to the daemon-owned preference, which defaults to Chat for
	// new sessions and falls back to TUI when Chat is unavailable. The preference
	// never mutates existing sessions automatically; compatible sessions may later
	// switch through the durable interface-transition endpoint. An unsupported
	// explicit request fails rather than quietly producing the other kind of session.
	Mode   domain.SessionMode `json:"mode,omitempty" enum:"chat,tui"`
	Prompt string             `json:"prompt,omitempty" maxLength:"16384"`
	// Model is an optional agent model override scoped to this single spawn. Empty
	// keeps the resolved project/role default. The daemon validates that the
	// selected harness can honor the model before launching.
	Model string `json:"model,omitempty" maxLength:"256"`

	// DisplayName is the sidebar label for the session, capped at 20 characters.
	// `ao spawn --name` always sets it; other clients (e.g. the desktop new-task
	// dialog) may omit it and fall back to the session id in the read model.
	DisplayName string `json:"displayName,omitempty" maxLength:"20"`
	// Attachments are files pasted or dropped into the task brief. Each carries
	// its bytes as standard base64 (no data: URL prefix). The daemon writes them
	// into the session worktree and appends path references to the prompt.
	Attachments []AttachmentInput `json:"attachments,omitempty"`
}

// AttachmentInput is one file attached to a spawn, delegate, stage, or send
// request.
type AttachmentInput struct {
	// MimeType is the browser-reported content type (e.g. "image/png"). Used to
	// derive the on-disk file extension. Explicitly blocked types are rejected.
	MimeType string `json:"mimeType,omitempty"`
	// Data is the raw file bytes, standard base64-encoded, without any
	// "data:...;base64," prefix.
	Data string `json:"data"`
}

// SessionResponse is the { session } body shared by session reads and updates.
type SessionResponse struct {
	Session SessionView `json:"session"`
}

// SpawnSessionResponse includes ephemeral measurements of the final assembled
// prompt texts. The fields are required so a measured zero remains distinct
// from a response that never measured prompt sizes.
type SpawnSessionResponse struct {
	Session           SessionView `json:"session"`
	PromptBytes       int         `json:"promptBytes"`
	SystemPromptBytes int         `json:"systemPromptBytes"`
}

// SwitchAgentRequest is the body of POST /api/v1/sessions/{sessionId}/switch-agent.
type SwitchAgentRequest struct {
	TargetHarness  domain.AgentHarness `json:"targetHarness" enum:"claude-code,codex" description:"Agent harness to continue the logical AO session with."`
	Model          string              `json:"model,omitempty" maxLength:"256" description:"Optional model override for the target agent launch or resume."`
	IdempotencyKey string              `json:"idempotencyKey,omitempty" maxLength:"128" description:"Optional retry key. Reusing it with a different request is rejected."`
}

// AgentSwitchView is the deliberately small public projection of a durable
// switch saga. Provider context, local artifact paths, retry keys, generation
// fences, and raw failure details remain daemon-private.
type AgentSwitchView struct {
	ID                      domain.AgentSwitchID                     `json:"id"`
	SessionID               domain.SessionID                         `json:"sessionId"`
	FromHarness             domain.AgentHarness                      `json:"fromHarness"`
	TargetHarness           domain.AgentHarness                      `json:"targetHarness"`
	TargetStartMode         domain.AgentSwitchTargetStartMode        `json:"targetStartMode,omitempty" enum:"fresh,resumed"`
	State                   domain.AgentSwitchState                  `json:"state" enum:"preparing_handoff,stopping_source,source_stopped,starting_target,target_ready,delivering_context,completed,failed"`
	AgentHandoffStatus      domain.AgentHandoffStatus                `json:"agentHandoffStatus" enum:"not_attempted,requested,received,unavailable,timed_out,failed,rejected"`
	SemanticHandoffIncluded bool                                     `json:"semanticHandoffIncluded"`
	SourceTranscriptStatus  domain.AgentSwitchSourceTranscriptStatus `json:"sourceTranscriptStatus,omitempty" enum:"not_attempted,available,unavailable"`
	ErrorCode               domain.AgentSwitchErrorCode              `json:"errorCode,omitempty" enum:"daemon_restart_pre_stop,daemon_restart_post_stop,daemon_restart_unrecoverable_target,daemon_restart_before_delivery,delivery_unconfirmed,source_session_terminated,source_stop_unconfirmed,target_binary_missing,target_agent_unauthorized,target_start_unconfirmed,source_restore_unconfirmed,request_cancelled,source_blocked,failed_pre_stop,failed_post_stop,target_ready_failed,delivery_failed,switch_failed"`
	RequestedAt             time.Time                                `json:"requestedAt"`
	UpdatedAt               time.Time                                `json:"updatedAt"`
}

// AgentSwitchResponse is the body returned by switch creation, reads, and
// generation-fenced handoff submission.
type AgentSwitchResponse struct {
	Switch AgentSwitchView `json:"switch"`
}

// ListAgentSwitchesResponse is the body of
// GET /api/v1/sessions/{sessionId}/agent-switches.
type ListAgentSwitchesResponse struct {
	Switches []AgentSwitchView `json:"switches"`
}

// SubmitAgentHandoffRequest is the body of
// POST /api/v1/sessions/{sessionId}/agent-switches/{switchId}/handoff.
// Handoff remains provider-neutral JSON and is accepted only from the source
// generation recorded by the durable switch.
type SubmitAgentHandoffRequest struct {
	SourceGenerationID domain.AgentGenerationID `json:"sourceGenerationId" description:"Source invocation generation that authored this handoff."`
	// RawMessage deliberately preserves the source object's original token
	// stream. Decoding into a map here would silently collapse duplicate keys
	// before the semantic validator can reject them.
	Handoff json.RawMessage `json:"handoff" description:"Structured, source-agent-authored handoff enrichment."`
}

// StageSessionAttachmentsRequest attaches files to a session that is already
// running, for a caller that will name the returned paths in its next message.
type StageSessionAttachmentsRequest struct {
	// Attachments each carry their bytes as standard base64 (no data: URL prefix).
	// The same count, size, and blocked-type rules as spawn apply.
	Attachments []AttachmentInput `json:"attachments"`
}

// StageSessionAttachmentsResponse is where the files were written.
type StageSessionAttachmentsResponse struct {
	SessionID domain.SessionID `json:"sessionId"`
	// Paths are worktree-relative and forward-slashed, in the order submitted. They
	// are what the agent can actually open, so a client must send these verbatim
	// rather than a display form of them.
	Paths []string `json:"paths"`
}

// ListWorkspaceFilesResponse is the body of GET /api/v1/sessions/{sessionId}/workspace/files.
type ListWorkspaceFilesResponse struct {
	SessionID      domain.SessionID                `json:"sessionId"`
	CompareBaseSHA string                          `json:"compareBaseSha,omitempty"`
	CompareBaseRef string                          `json:"compareBaseRef,omitempty"`
	CompareMode    sessionsvc.WorkspaceCompareMode `json:"compareMode,omitempty" enum:"base,head_fallback"`
	Files          []WorkspaceFileSummary          `json:"files"`
	Truncated      bool                            `json:"truncated"`
	// Sections groups the same working tree into git-state sections. Only
	// populated for single-repo sessions; empty for workspace-project
	// (multi-repo) and scratch sessions.
	Sections WorkspaceFileSections `json:"sections"`
	// Commits are the commits between the compare base and HEAD, oldest first.
	Commits []WorkspaceCommitSummary `json:"commits"`
	Summary WorkspaceSummary         `json:"summary"`
	// Ahead and Behind are omitted when no push/pull data is available (no
	// upstream, detached HEAD).
	Ahead  *int `json:"ahead,omitempty"`
	Behind *int `json:"behind,omitempty"`
}

// WorkspaceFileSections groups a session workspace's changed files by git
// state: staged (index vs HEAD), unstaged (worktree vs index), untracked, and
// committed (HEAD vs the compare base). A partially staged file can appear in
// both staged and unstaged.
type WorkspaceFileSections struct {
	Staged    []WorkspaceFileSummary `json:"staged"`
	Unstaged  []WorkspaceFileSummary `json:"unstaged"`
	Untracked []WorkspaceFileSummary `json:"untracked"`
	Committed []WorkspaceFileSummary `json:"committed"`
}

// WorkspaceCommitSummary is one commit between the compare base and HEAD.
type WorkspaceCommitSummary struct {
	SHA       string    `json:"sha"`
	Subject   string    `json:"subject"`
	Author    string    `json:"author"`
	Timestamp time.Time `json:"timestamp"`
}

// WorkspaceSummary aggregates a session workspace's base..worktree diff into
// totals for the Files panel header.
type WorkspaceSummary struct {
	Files     int `json:"files"`
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
}

// WorkspaceFileSummary is one file row in the session workspace browser.
type WorkspaceFileSummary struct {
	Path         string                         `json:"path"`
	PreviousPath string                         `json:"previousPath,omitempty"`
	Status       sessionsvc.WorkspaceFileStatus `json:"status" enum:"unmodified,modified,added,deleted,renamed"`
	Additions    int                            `json:"additions"`
	Deletions    int                            `json:"deletions"`
	Size         int64                          `json:"size"`
	Binary       bool                           `json:"binary"`
}

// WorkspaceFileResponse is the body of GET /api/v1/sessions/{sessionId}/workspace/file.
type WorkspaceFileResponse struct {
	SessionID        domain.SessionID                `json:"sessionId"`
	Path             string                          `json:"path"`
	PreviousPath     string                          `json:"previousPath,omitempty"`
	Status           sessionsvc.WorkspaceFileStatus  `json:"status" enum:"unmodified,modified,added,deleted,renamed"`
	Additions        int                             `json:"additions"`
	Deletions        int                             `json:"deletions"`
	Size             int64                           `json:"size"`
	Binary           bool                            `json:"binary"`
	Deleted          bool                            `json:"deleted"`
	ImageMediaType   string                          `json:"imageMediaType,omitempty"`
	Content          string                          `json:"content"`
	ContentTruncated bool                            `json:"contentTruncated"`
	Diff             string                          `json:"diff"`
	DiffTruncated    bool                            `json:"diffTruncated"`
	CompareBaseSHA   string                          `json:"compareBaseSha,omitempty"`
	CompareBaseRef   string                          `json:"compareBaseRef,omitempty"`
	CompareMode      sessionsvc.WorkspaceCompareMode `json:"compareMode,omitempty" enum:"base,head_fallback"`
}

// DesktopWorkspaceLocationResponse is returned only by the LAN-blocked desktop
// handoff route. Electron main consumes the absolute path and never exposes it
// through the preload bridge.
type DesktopWorkspaceLocationResponse struct {
	SessionID     domain.SessionID `json:"sessionId"`
	WorkspacePath string           `json:"workspacePath"`
}

// SessionPreviewResponse is the body of GET /api/v1/sessions/{sessionId}/preview.
type SessionPreviewResponse struct {
	SessionID  domain.SessionID `json:"sessionId"`
	PreviewURL string           `json:"previewUrl,omitempty"`
	Entry      string           `json:"entry,omitempty"`
}

// RenameSessionRequest is the body of PATCH /api/v1/sessions/{sessionId}.
type RenameSessionRequest struct {
	DisplayName string `json:"displayName" minLength:"1"`
}

// SetSessionReviewerRequest sets the durable reviewer preference for a session.
// Empty clears the preference and falls back to project configuration.
type SetSessionReviewerRequest struct {
	Harness     domain.ReviewerHarness `json:"harness,omitempty" enum:"claude-code,codex,copilot,cursor,kilocode,opencode,kiro,pi,qwen,agy,continue,goose,vibe,devin,droid,kimi,kimchi,muse,amp,aider,grok,crush,auggie,cline,autohand"`
	AgentConfig domain.AgentConfig     `json:"agentConfig,omitempty"`
}

// SetSessionAutoReviewRequest configures daemon-side review automation.
type SetSessionAutoReviewRequest struct {
	Enabled        bool `json:"enabled"`
	enabledPresent bool
}

// UnmarshalJSON distinguishes an omitted required boolean from an explicit
// false without making the generated API schema nullable.
func (r *SetSessionAutoReviewRequest) UnmarshalJSON(data []byte) error {
	var wire struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	r.enabledPresent = wire.Enabled != nil
	if wire.Enabled != nil {
		r.Enabled = *wire.Enabled
	}
	return nil
}

// SetSessionPreviewRequest is the body of POST /api/v1/sessions/{sessionId}/preview.
// An empty url asks the daemon to autodetect a static entry point in the
// session workspace; a non-empty url is used verbatim as the preview target.
type SetSessionPreviewRequest struct {
	URL string `json:"url,omitempty" description:"Preview target URL. When empty, the daemon autodetects a static entry point in the session workspace."`
}

// StartPreviewServerRequest selects one named entry from .ao/launch.json. The
// name may be omitted when the file contains exactly one configuration.
type StartPreviewServerRequest struct {
	Configuration string `json:"configuration,omitempty" description:"Named preview configuration. Optional when exactly one configuration exists."`
}

// PreviewServerStatusResponse reports the deterministic server AO owns for one
// session. Logs are bounded to the latest lines and never contain global
// process or port discovery.
type PreviewServerStatusResponse struct {
	SessionID     domain.SessionID `json:"sessionId"`
	State         string           `json:"state" enum:"stopped,starting,ready,stopping,failed"`
	Configuration string           `json:"configuration,omitempty"`
	TargetKind    string           `json:"targetKind,omitempty" enum:"app,api"`
	URL           string           `json:"url,omitempty"`
	Port          int              `json:"port,omitempty"`
	StartedAt     time.Time        `json:"startedAt,omitempty"`
	Error         string           `json:"error,omitempty"`
	Logs          []string         `json:"logs"`
}

// BrowserStatusQuery selects the session whose logical browser is inspected.
type BrowserStatusQuery struct {
	SessionID domain.SessionID `query:"sessionId" description:"AO session identifier."`
}

// BrowserCapabilityHeader proves that the caller owns the target session.
type BrowserCapabilityHeader struct {
	Capability string `header:"X-AO-Browser-Capability" description:"Opaque browser capability injected into the owning AO worker."`
}

// BrowserStatusResponse reports whether the desktop-owned browser transport is
// ready. A connected runtime can create the session target while its panel is
// hidden; panel visibility is intentionally not part of this state.
type BrowserStatusResponse struct {
	SessionID   domain.SessionID `json:"sessionId"`
	Connected   bool             `json:"connected"`
	ConnectedAt time.Time        `json:"connectedAt,omitempty"`
	Transport   string           `json:"transport"`
}

// BrowserCommandRequest is the stable daemon-facing command envelope. Action
// arguments remain action-specific JSON so new target-scoped operations do not
// require a new transport or Electron IPC surface.
type BrowserCommandRequest struct {
	SessionID domain.SessionID       `json:"sessionId"`
	Action    string                 `json:"action"`
	Args      map[string]interface{} `json:"args,omitempty"`
}

// BrowserCommandResponse returns a correlated result from the browser runtime.
type BrowserCommandResponse struct {
	RequestID string           `json:"requestId"`
	SessionID domain.SessionID `json:"sessionId"`
	Action    string           `json:"action"`
	Result    interface{}      `json:"result"`
}

// SetSessionMergePolicyRequest is the body of PATCH /api/v1/sessions/{sessionId}/merge-policy.
type SetSessionMergePolicyRequest struct {
	TerminateOnPRMerge bool `json:"terminateOnPrMerge"`
}

// RenameSessionResponse is the body of PATCH /api/v1/sessions/{sessionId}.
type RenameSessionResponse struct {
	OK          bool             `json:"ok"`
	SessionID   domain.SessionID `json:"sessionId"`
	DisplayName string           `json:"displayName"`
}

// SetSessionMergePolicyResponse is the body of PATCH /api/v1/sessions/{sessionId}/merge-policy.
type SetSessionMergePolicyResponse struct {
	OK                 bool             `json:"ok"`
	SessionID          domain.SessionID `json:"sessionId"`
	TerminateOnPRMerge bool             `json:"terminateOnPrMerge"`
	Session            SessionView      `json:"session"`
}

// SetSessionAutoInjectReviewRequest is the body of PATCH /api/v1/sessions/{sessionId}/auto-inject-review.
type SetSessionAutoInjectReviewRequest struct {
	AutoInjectReview bool `json:"autoInjectReview"`
}

// SetSessionAutoInjectReviewResponse is the response from updating a session's automatic review-injection policy.
type SetSessionAutoInjectReviewResponse struct {
	OK               bool             `json:"ok"`
	SessionID        domain.SessionID `json:"sessionId"`
	AutoInjectReview bool             `json:"autoInjectReview"`
	Session          SessionView      `json:"session"`
}

// SetSessionAutoInjectCIRequest updates automatic CI delivery for a session
// and every PR currently owned by it.
type SetSessionAutoInjectCIRequest struct {
	AutoInjectCI bool `json:"autoInjectCI"`
}

// SetSessionAutoInjectCIResponse confirms the persisted session policy.
type SetSessionAutoInjectCIResponse struct {
	OK           bool             `json:"ok"`
	SessionID    domain.SessionID `json:"sessionId"`
	AutoInjectCI bool             `json:"autoInjectCI"`
	Session      SessionView      `json:"session"`
}

// RestoreSessionResponse is the body of POST /api/v1/sessions/{sessionId}/restore.
type RestoreSessionResponse struct {
	OK          bool                       `json:"ok"`
	SessionID   domain.SessionID           `json:"sessionId"`
	RestoreMode sessionsvc.RestoreModeView `json:"restoreMode" enum:"native,saved_prompt,fresh"`
	Session     SessionView                `json:"session"`
}

// ExitAgentResponse is the body of POST /api/v1/sessions/{sessionId}/exit-agent.
type ExitAgentResponse struct {
	OK        bool             `json:"ok"`
	SessionID domain.SessionID `json:"sessionId"`
	Session   SessionView      `json:"session"`
}

// ResumeAgentResponse is the body of POST /api/v1/sessions/{sessionId}/resume-agent.
type ResumeAgentResponse struct {
	OK         bool                       `json:"ok"`
	SessionID  domain.SessionID           `json:"sessionId"`
	ResumeMode sessionsvc.RestoreModeView `json:"resumeMode" enum:"native,saved_prompt,fresh"`
	Session    SessionView                `json:"session"`
}

// StartSessionInterfaceTransitionRequest is the body of POST
// /api/v1/sessions/{sessionId}/interface-transition.
type StartSessionInterfaceTransitionRequest struct {
	TargetMode domain.SessionMode                      `json:"targetMode" enum:"chat,tui"`
	Policy     domain.SessionInterfaceTransitionPolicy `json:"policy" enum:"drain,interrupt"`
}

// SessionInterfaceTransitionView is the client-facing progress record. The
// provider-native conversation id is intentionally not exposed: clients need
// controller state, not an adapter implementation detail.
type SessionInterfaceTransitionView struct {
	ID                   string                                  `json:"id"`
	SessionID            domain.SessionID                        `json:"sessionId"`
	SourceMode           domain.SessionMode                      `json:"sourceMode" enum:"chat,tui"`
	TargetMode           domain.SessionMode                      `json:"targetMode" enum:"chat,tui"`
	Policy               domain.SessionInterfaceTransitionPolicy `json:"policy" enum:"drain,interrupt"`
	Phase                domain.SessionInterfaceTransitionPhase  `json:"phase" enum:"requested,preflighting,draining,source_stopping,source_stopped,target_starting,activating,completed,failed,cancelled,recovery_required"`
	ErrorCode            string                                  `json:"errorCode,omitempty"`
	ErrorDetail          string                                  `json:"errorDetail,omitempty"`
	CreatedAt            time.Time                               `json:"createdAt"`
	UpdatedAt            time.Time                               `json:"updatedAt"`
	CompletedAt          *time.Time                              `json:"completedAt,omitempty"`
	NoticeAcknowledgedAt *time.Time                              `json:"noticeAcknowledgedAt,omitempty"`
}

// SessionInterfaceTransitionStatusResponse is the body of GET
// /api/v1/sessions/{sessionId}/interface-transition.
type SessionInterfaceTransitionStatusResponse struct {
	Supported  bool                            `json:"supported"`
	TargetMode domain.SessionMode              `json:"targetMode" enum:"chat,tui"`
	ReasonCode string                          `json:"reasonCode,omitempty"`
	Reason     string                          `json:"reason,omitempty"`
	Transition *SessionInterfaceTransitionView `json:"transition,omitempty"`
}

// StartSessionInterfaceTransitionResponse acknowledges an asynchronous handoff.
type StartSessionInterfaceTransitionResponse struct {
	OK         bool                           `json:"ok"`
	SessionID  domain.SessionID               `json:"sessionId"`
	Transition SessionInterfaceTransitionView `json:"transition"`
}

// CancelSessionInterfaceTransitionResponse acknowledges cancellation.
type CancelSessionInterfaceTransitionResponse struct {
	OK        bool             `json:"ok"`
	SessionID domain.SessionID `json:"sessionId"`
}

// InterfaceTransitionNoticeAckResponse returns the retained transition with
// its durable notice acknowledgement.
type InterfaceTransitionNoticeAckResponse struct {
	OK         bool                           `json:"ok"`
	SessionID  domain.SessionID               `json:"sessionId"`
	Transition SessionInterfaceTransitionView `json:"transition"`
}

// KillSessionResponse is the body of POST /api/v1/sessions/{sessionId}/kill.
type KillSessionResponse struct {
	OK        bool             `json:"ok"`
	SessionID domain.SessionID `json:"sessionId"`
	Freed     bool             `json:"freed,omitempty"`
}

// RollbackSessionResponse is the body of POST /api/v1/sessions/{sessionId}/rollback.
// Exactly one of Deleted/Killed is true on a successful rollback; both are
// false when the session was already absent or already terminated (benign).
type RollbackSessionResponse struct {
	OK        bool             `json:"ok"`
	SessionID domain.SessionID `json:"sessionId"`
	Deleted   bool             `json:"deleted,omitempty"`
	Killed    bool             `json:"killed,omitempty"`
}

// CleanupSkippedSession is one terminal session whose workspace cleanup
// preserved rather than reclaimed (a dirty worktree is never force-deleted),
// with the user-facing reason.
type CleanupSkippedSession struct {
	SessionID domain.SessionID `json:"sessionId"`
	Reason    string           `json:"reason"`
}

// CleanupSessionsResponse is the body of POST /api/v1/sessions/cleanup.
type CleanupSessionsResponse struct {
	OK bool `json:"ok"`
	// Cleaned lists sessions whose workspace was present and has been released.
	Cleaned []domain.SessionID `json:"cleaned"`
	// AlreadyGone lists sessions whose workspace directory was already missing,
	// so teardown completed without reclaiming anything.
	AlreadyGone []domain.SessionID      `json:"alreadyGone"`
	Skipped     []CleanupSkippedSession `json:"skipped"`
}

// SendSessionMessageRequest is the body of POST /api/v1/sessions/{sessionId}/send.
type SendSessionMessageRequest struct {
	Message string `json:"message" minLength:"1" maxLength:"4096"`
	// Attachment is an optional inline image (e.g. a browser-annotation
	// snapshot) delivered alongside the message. The daemon writes it into the
	// session worktree and appends a path reference to the message.
	Attachment *AttachmentInput `json:"attachment,omitempty"`
}

// SendSessionMessageResponse is the body of POST /api/v1/sessions/{sessionId}/send.
type SendSessionMessageResponse struct {
	OK        bool             `json:"ok"`
	SessionID domain.SessionID `json:"sessionId"`
	Message   string           `json:"message"`
}

// DelegateTaskRequest is the body of POST /api/v1/orchestrators/delegate.
// An omitted agent tells the orchestrator to use the project's worker default.
type DelegateTaskRequest struct {
	ProjectID domain.ProjectID    `json:"projectId"`
	Brief     string              `json:"brief" maxLength:"16384"`
	Agent     domain.AgentHarness `json:"agent,omitempty" enum:"claude-code,codex,aider,opencode,grok,droid,amp,agy,crush,cursor,qwen,copilot,goose,auggie,continue,devin,cline,kimi,muse,kiro,kilocode,vibe,pi,kimchi,omp,prime-agent,autohand,fake"`
	Model     string              `json:"model,omitempty" maxLength:"256"`
	// ApprovalMode is an optional per-session override. The UI uses the explicit
	// bypass value only after the user accepts an approval-less Chat fallback.
	ApprovalMode domain.PermissionMode `json:"approvalMode,omitempty" enum:"default,accept-edits,auto,bypass-permissions"`
	// Mode is omitted for the daemon-owned default. The UI sends tui only when
	// the user explicitly accepts the fallback after Chat preflight fails.
	Mode domain.SessionMode `json:"mode,omitempty" enum:"tui,chat"`
	// Attachments are files pasted, dropped, or picked into the delegated task
	// brief. Each carries bytes as standard base64 (no data: URL prefix). The
	// daemon writes them into the spawned worker worktree and appends path
	// references to the worker prompt.
	Attachments []AttachmentInput `json:"attachments,omitempty"`
}

// DelegateTaskResponse confirms which worker was spawned and, when available,
// which orchestrator received the follow-up title request.
type DelegateTaskResponse struct {
	OK             bool             `json:"ok"`
	WorkerID       domain.SessionID `json:"workerId"`
	OrchestratorID domain.SessionID `json:"orchestratorId,omitempty"`
}

// SessionPRFacts is the pull-request read shape returned under session PR routes.
type SessionPRFacts struct {
	URL            string                `json:"url"`
	Number         int                   `json:"number"`
	State          string                `json:"state" enum:"draft,open,merged,closed"`
	CI             domain.CIState        `json:"ci" enum:"unknown,pending,passing,failing"`
	Review         domain.ReviewDecision `json:"review" enum:"none,approved,changes_requested,review_required"`
	Mergeability   domain.Mergeability   `json:"mergeability" enum:"unknown,mergeable,conflicting,blocked,unstable"`
	ReviewComments bool                  `json:"reviewComments"`
	UpdatedAt      time.Time             `json:"updatedAt"`
}

// SessionPRSummary is the concise desktop SCM read model returned by GET
// /sessions/{sessionId}/pr. It intentionally omits CI log tails and review
// comment bodies.
type SessionPRSummary struct {
	URL              string                       `json:"url"`
	HTMLURL          string                       `json:"htmlUrl,omitempty"`
	Number           int                          `json:"number"`
	Title            string                       `json:"title"`
	State            domain.PRState               `json:"state" enum:"draft,open,merged,closed"`
	Provider         string                       `json:"provider" enum:"github,gitlab"`
	Repo             string                       `json:"repo"`
	Author           string                       `json:"author"`
	AuthorAvatarURL  string                       `json:"authorAvatarUrl,omitempty"`
	SourceBranch     string                       `json:"sourceBranch"`
	TargetBranch     string                       `json:"targetBranch"`
	HeadSHA          string                       `json:"headSha"`
	Additions        int                          `json:"additions"`
	Deletions        int                          `json:"deletions"`
	ChangedFiles     int                          `json:"changedFiles"`
	CI               SessionPRCISummary           `json:"ci"`
	Review           SessionPRReviewSummary       `json:"review"`
	Mergeability     SessionPRMergeabilitySummary `json:"mergeability"`
	StateChangedAt   *time.Time                   `json:"stateChangedAt,omitempty"`
	CreatedAt        *time.Time                   `json:"createdAt,omitempty"`
	UpdatedAt        time.Time                    `json:"updatedAt"`
	ObservedAt       time.Time                    `json:"observedAt,omitempty"`
	CIObservedAt     time.Time                    `json:"ciObservedAt,omitempty"`
	ReviewObservedAt time.Time                    `json:"reviewObservedAt,omitempty"`
}

// SessionPRCISummary is the CI status block for a session PR summary.
type SessionPRCISummary struct {
	State         domain.CIState          `json:"state" enum:"unknown,pending,passing,failing"`
	FailingChecks []SessionPRFailingCheck `json:"failingChecks"`
	AutoInjectCI  bool                    `json:"autoInjectCI"`
}

// SessionPRFailingCheck is one failed or cancelled CI check for a PR.
type SessionPRFailingCheck struct {
	Name       string               `json:"name"`
	Status     domain.PRCheckStatus `json:"status" enum:"failed,cancelled"`
	Conclusion string               `json:"conclusion"`
	URL        string               `json:"url,omitempty"`
}

// SessionPRReviewSummary is the review state block for a session PR summary.
type SessionPRReviewSummary struct {
	Decision                   domain.ReviewDecision         `json:"decision" enum:"none,approved,changes_requested,review_required"`
	HasUnresolvedHumanComments bool                          `json:"hasUnresolvedHumanComments"`
	UnresolvedBy               []SessionPRUnresolvedReviewer `json:"unresolvedBy"`
	ResolvedBy                 []SessionPRUnresolvedReviewer `json:"resolvedBy,omitempty"`
	Reviews                    []SessionPRReviewEntry        `json:"reviews,omitempty"`
}

// SessionPRReviewEntry is one submitted provider review summary: a reviewer's
// decisive verdict and the summary body they submitted with it.
type SessionPRReviewEntry struct {
	ReviewerID       string                `json:"reviewerId"`
	Verdict          domain.ReviewDecision `json:"verdict" enum:"none,approved,changes_requested,review_required"`
	Body             string                `json:"body,omitempty"`
	ReviewURL        string                `json:"reviewUrl,omitempty"`
	SubmittedAt      time.Time             `json:"submittedAt"`
	IsBot            bool                  `json:"isBot,omitempty"`
	AutoInjectReview bool                  `json:"autoInjectReview"`
}

// SessionPRUnresolvedReviewer groups review comments by reviewer.
type SessionPRUnresolvedReviewer struct {
	ReviewerID string                       `json:"reviewerId"`
	Count      int                          `json:"count"`
	Links      []SessionPRReviewCommentLink `json:"links"`
	ReviewURL  string                       `json:"reviewUrl,omitempty"`
	IsBot      bool                         `json:"isBot,omitempty"`
}

// SessionPRReviewCommentLink points to one review comment.
type SessionPRReviewCommentLink struct {
	URL              string `json:"url,omitempty"`
	ReviewID         string `json:"reviewId,omitempty"`
	File             string `json:"file,omitempty"`
	Line             int    `json:"line,omitempty"`
	Body             string `json:"body,omitempty"`
	AutoInjectReview bool   `json:"autoInjectReview"`
}

// SessionPRMergeabilitySummary is the mergeability block for a session PR summary.
type SessionPRMergeabilitySummary struct {
	State         domain.Mergeability     `json:"state" enum:"unknown,mergeable,conflicting,blocked,unstable"`
	Reasons       []string                `json:"reasons"`
	PRURL         string                  `json:"prUrl"`
	ConflictFiles []SessionPRConflictFile `json:"conflictFiles,omitempty"`
}

// SessionPRConflictFile is one file involved in a PR merge conflict.
type SessionPRConflictFile struct {
	Path string `json:"path"`
	URL  string `json:"url,omitempty"`
}

// ListSessionPRsResponse is the body of GET /sessions/{sessionId}/pr.
type ListSessionPRsResponse struct {
	SessionID domain.SessionID   `json:"sessionId"`
	PRs       []SessionPRSummary `json:"prs"`
}

// NewSessionPRSummary maps the service PR summary model to its HTTP DTO.
func NewSessionPRSummary(in sessionsvc.PRSummary) SessionPRSummary {
	return SessionPRSummary{
		URL:              in.URL,
		HTMLURL:          in.HTMLURL,
		Number:           in.Number,
		Title:            in.Title,
		State:            in.State,
		Provider:         in.Provider,
		Repo:             in.Repo,
		Author:           in.Author,
		AuthorAvatarURL:  in.AuthorAvatarURL,
		SourceBranch:     in.SourceBranch,
		TargetBranch:     in.TargetBranch,
		HeadSHA:          in.HeadSHA,
		Additions:        in.Additions,
		Deletions:        in.Deletions,
		ChangedFiles:     in.ChangedFiles,
		CI:               newSessionPRCISummary(in.CI),
		Review:           newSessionPRReviewSummary(in.Review),
		Mergeability:     newSessionPRMergeabilitySummary(in.Mergeability),
		StateChangedAt:   optionalTime(in.StateChangedAt),
		CreatedAt:        optionalTime(in.CreatedAt),
		UpdatedAt:        in.UpdatedAt,
		ObservedAt:       in.ObservedAt,
		CIObservedAt:     in.CIObservedAt,
		ReviewObservedAt: in.ReviewObservedAt,
	}
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func newSessionPRCISummary(in sessionsvc.PRCISummary) SessionPRCISummary {
	checks := make([]SessionPRFailingCheck, 0, len(in.FailingChecks))
	for _, ch := range in.FailingChecks {
		checks = append(checks, SessionPRFailingCheck{Name: ch.Name, Status: ch.Status, Conclusion: ch.Conclusion, URL: ch.URL})
	}
	return SessionPRCISummary{State: in.State, FailingChecks: checks, AutoInjectCI: in.AutoInjectCI}
}

func newSessionPRReviewSummary(in sessionsvc.PRReviewSummary) SessionPRReviewSummary {
	reviewers := newSessionPRCommentReviewers(in.UnresolvedBy)
	resolvedReviewers := newSessionPRCommentReviewers(in.ResolvedBy)
	entries := make([]SessionPRReviewEntry, 0, len(in.Reviews))
	for _, review := range in.Reviews {
		entries = append(entries, SessionPRReviewEntry{
			ReviewerID:       review.Reviewer,
			Verdict:          review.Verdict,
			Body:             review.Body,
			ReviewURL:        review.URL,
			SubmittedAt:      review.SubmittedAt,
			IsBot:            review.IsBot,
			AutoInjectReview: review.AutoInjectReview,
		})
	}
	return SessionPRReviewSummary{Decision: in.Decision, HasUnresolvedHumanComments: in.HasUnresolvedHumanComments, UnresolvedBy: reviewers, ResolvedBy: resolvedReviewers, Reviews: entries}
}

func newSessionPRCommentReviewers(in []sessionsvc.PRUnresolvedReviewer) []SessionPRUnresolvedReviewer {
	reviewers := make([]SessionPRUnresolvedReviewer, 0, len(in))
	for _, reviewer := range in {
		links := make([]SessionPRReviewCommentLink, 0, len(reviewer.Links))
		for _, link := range reviewer.Links {
			links = append(links, SessionPRReviewCommentLink{URL: link.URL, ReviewID: link.ReviewID, File: link.File, Line: link.Line, Body: link.Body, AutoInjectReview: link.AutoInjectReview})
		}
		reviewers = append(reviewers, SessionPRUnresolvedReviewer{ReviewerID: reviewer.ReviewerID, Count: reviewer.Count, Links: links, ReviewURL: reviewer.ReviewURL, IsBot: reviewer.IsBot})
	}
	return reviewers
}

func newSessionPRMergeabilitySummary(in sessionsvc.PRMergeabilitySummary) SessionPRMergeabilitySummary {
	files := make([]SessionPRConflictFile, 0, len(in.ConflictFiles))
	for _, file := range in.ConflictFiles {
		files = append(files, SessionPRConflictFile{Path: file.Path, URL: file.URL})
	}
	return SessionPRMergeabilitySummary{State: in.State, Reasons: in.Reasons, PRURL: in.PRURL, ConflictFiles: files}
}

// ClaimPRRequest is the body of POST /sessions/{sessionId}/pr/claim.
type ClaimPRRequest struct {
	PR            string `json:"pr" minLength:"1"`
	AllowTakeover *bool  `json:"allowTakeover,omitempty"`
}

// ClaimPRResponse is the body of POST /sessions/{sessionId}/pr/claim.
type ClaimPRResponse struct {
	OK            bool               `json:"ok"`
	SessionID     domain.SessionID   `json:"sessionId"`
	PRs           []SessionPRFacts   `json:"prs"`
	BranchChanged bool               `json:"branchChanged"`
	TakenOverFrom []domain.SessionID `json:"takenOverFrom"`
}

// SetActivityRequest is the body of POST /api/v1/sessions/{sessionId}/activity.
// Event/ToolName/ToolUseID are optional correlation facts: which AO hook
// sub-command produced the state and, for tool-use hooks, which tool call it
// concerns. Lifecycle uses them to clear a stale blocked state only when the
// specific approved tool finishes. Absent on old CLIs and on adapters whose
// payloads carry no tool identity — the signal then keeps its plain
// state-only semantics.
// AgentSessionID may arrive without State on metadata-only SessionStart hooks.
type SetActivityRequest struct {
	State                 string             `json:"state,omitempty" enum:"active,idle,waiting_input,blocked,exited" description:"Agent activity state reported by an agent hook. Optional for metadata-only hooks."`
	Event                 string             `json:"event,omitempty" description:"AO hook sub-command that produced this state (e.g. post-tool-use)."`
	ToolName              string             `json:"toolName,omitempty" description:"Native tool name, for tool-use hook events."`
	ToolUseID             string             `json:"toolUseId,omitempty" description:"Native tool-use id, for tool-use hook events."`
	AgentSessionID        string             `json:"agentSessionId,omitempty" description:"Native agent session identifier used to resume its transcript."`
	LatestUserPrompt      string             `json:"latestUserPrompt,omitempty" maxLength:"16384" description:"Latest real user prompt exposed by the provider hook."`
	LatestAssistantUpdate string             `json:"latestAssistantUpdate,omitempty" maxLength:"16384" description:"Latest assistant update exposed by the provider hook."`
	TranscriptPath        string             `json:"transcriptPath,omitempty" maxLength:"4096" description:"Read-only provider-native transcript path exposed by the hook."`
	LaunchID              string             `json:"launchId,omitempty" description:"AO process generation that produced the signal."`
	Usage                 *UsageHookMetadata `json:"usage,omitempty" description:"Provider transcript metadata used by the local usage pipeline."`
}

// UsageHookMetadata is the transcript metadata carried by supported Claude
// Code and Codex hooks. It contains paths and identifiers only, never prompt or
// response content.
type UsageHookMetadata struct {
	Harness                domain.AgentHarness `json:"harness" enum:"claude-code,codex"`
	ProviderID             string              `json:"providerId,omitempty" description:"Canonical provider routing hint derived by the trusted local Claude hook."`
	TranscriptPath         string              `json:"transcriptPath,omitempty"`
	ModelID                string              `json:"modelId,omitempty"`
	SubagentID             string              `json:"subagentId,omitempty"`
	SubagentTranscriptPath string              `json:"subagentTranscriptPath,omitempty"`
}

// SetActivityResponse is the body of POST /api/v1/sessions/{sessionId}/activity.
type SetActivityResponse struct {
	OK        bool             `json:"ok"`
	SessionID domain.SessionID `json:"sessionId"`
	State     string           `json:"state"`
}

// SetReviewActivityRequest is the body of POST /api/v1/reviews/{reviewSessionID}/activity.
// Reviewer activity does not currently feed worker/Kanban session state.
// AgentSessionID is the native reviewer conversation id used for reviewer
// restore.
type SetReviewActivityRequest struct {
	State          string `json:"state,omitempty" enum:"active,idle,waiting_input,blocked,exited" description:"Reviewer activity state reported by a hook. Accepted for forward compatibility, not used for session display state."`
	Event          string `json:"event,omitempty" description:"AO hook sub-command that produced this signal."`
	AgentSessionID string `json:"agentSessionId,omitempty" description:"Native reviewer session identifier used to resume its transcript."`
	LaunchID       string `json:"launchId,omitempty" description:"AO process generation that produced the signal."`
}

// SetReviewActivityResponse is the body of POST /api/v1/reviews/{reviewSessionID}/activity.
type SetReviewActivityResponse struct {
	OK              bool   `json:"ok"`
	ReviewSessionID string `json:"reviewSessionId"`
}

// OrchestratorIDParam is the {id} path parameter for orchestrator routes.
type OrchestratorIDParam struct {
	ID string `path:"id" description:"Orchestrator session identifier, e.g. project-orchestrator."`
}

// ReviewSessionIDParam is the {reviewSessionID} path parameter for reviewer-owned routes.
type ReviewSessionIDParam struct {
	ID string `path:"reviewSessionID" description:"Reviewer session identifier, currently the per-harness review row id."`
}

// SpawnOrchestratorRequest is the body of POST /api/v1/orchestrators.
type SpawnOrchestratorRequest struct {
	ProjectID domain.ProjectID `json:"projectId"`
	Clean     bool             `json:"clean,omitempty"`
	// Mode applies only when this request creates a project orchestrator. An
	// idempotent ensure returns the existing orchestrator unchanged, and a clean
	// replacement inherits the existing orchestrator's currently committed mode.
	Mode domain.SessionMode `json:"mode,omitempty" enum:"chat,tui"`
}

// SpawnOrchestratorResponse is the body of POST /api/v1/orchestrators.
type SpawnOrchestratorResponse struct {
	Orchestrator OrchestratorResponse `json:"orchestrator"`
}

// OrchestratorResponse is the minimal orchestrator read model returned after spawn.
type OrchestratorResponse struct {
	ID          domain.SessionID `json:"id"`
	ProjectID   domain.ProjectID `json:"projectId"`
	ProjectName string           `json:"projectName,omitempty"`
}

// ListAgentsResponse is the body of GET /api/v1/agents.
type ListAgentsResponse = agentsvc.Inventory

// RefreshAgentsResponse is the body of POST /api/v1/agents/refresh.
type RefreshAgentsResponse = agentsvc.Inventory

// ProbeAgentResponse is the body of POST /api/v1/agents/{agent}/probe.
type ProbeAgentResponse = agentsvc.ProbeResult

// AgentReadinessResponse is the normalized cached or ensured harness view.
type AgentReadinessResponse = agentsvc.Readiness

// EnsureAgentReadinessRequest selects harnesses and the daemon freshness policy.
// An omitted or empty agentIds list selects all supported harnesses.
type EnsureAgentReadinessRequest struct {
	AgentIDs []string                     `json:"agentIds,omitempty"`
	Purpose  domain.AgentReadinessPurpose `json:"purpose" enum:"display,launch"`
}

// CodexAccountsResponse is the controller-owned, redacted cached account view.
type CodexAccountsResponse struct {
	ActiveAccountID        string                               `json:"activeAccountId,omitempty"`
	AccountRevision        int64                                `json:"accountRevision"`
	Accounts               []CodexAccountResponse               `json:"accounts"`
	Capabilities           CodexAccountCapabilitiesResponse     `json:"capabilities"`
	UnmanagedGlobalAccount *CodexUnmanagedGlobalAccountResponse `json:"unmanagedGlobalAccount,omitempty"`
	ActiveLogin            *CodexActiveLoginResponse            `json:"activeLogin,omitempty"`
	CurrentSwitch          *CodexAccountSwitchResponse          `json:"currentSwitch,omitempty"`
}

// CodexAccountResponse contains UI account facts without provider or storage identity.
type CodexAccountResponse struct {
	ID             string                            `json:"id"`
	Label          string                            `json:"label"`
	Status         string                            `json:"status" enum:"valid,signed_out,broken"`
	ReasonCode     string                            `json:"reasonCode"`
	Reason         string                            `json:"reason"`
	Active         bool                              `json:"active"`
	Authentication CodexAuthenticationResponse       `json:"authentication"`
	AuthMethod     string                            `json:"authMethod" enum:"chatgpt,api_key,other,unknown"`
	AccountEmail   *string                           `json:"accountEmail,omitempty"`
	Capacity       CodexAccountCapacityResponse      `json:"capacity"`
	UsageSummary   *CodexAccountUsageSummaryResponse `json:"usageSummary,omitempty"`
	CreatedAt      time.Time                         `json:"createdAt"`
}

// CodexAuthenticationResponse is the normalized authentication observation.
type CodexAuthenticationResponse struct {
	State       string     `json:"state" enum:"authorized,unauthorized,unknown,not_applicable"`
	Freshness   string     `json:"freshness" enum:"fresh,stale,checking"`
	CheckedAt   *time.Time `json:"checkedAt"`
	AttemptedAt *time.Time `json:"attemptedAt"`
	ReasonCode  string     `json:"reasonCode"`
	Reason      string     `json:"reason"`
}

// CodexAccountCapacityResponse is the normalized capacity display projection.
type CodexAccountCapacityResponse struct {
	State             string                            `json:"state" enum:"available,near_limit,exhausted,unknown,unsupported"`
	Freshness         string                            `json:"freshness" enum:"fresh,stale,checking"`
	Plan              *string                           `json:"plan,omitempty"`
	UsedPercent       *float64                          `json:"usedPercent,omitempty" minimum:"0" maximum:"100"`
	RemainingPercent  *float64                          `json:"remainingPercent,omitempty" minimum:"0" maximum:"100"`
	ResetsAt          *time.Time                        `json:"resetsAt,omitempty"`
	ObservedAt        *time.Time                        `json:"observedAt,omitempty"`
	CheckedAt         *time.Time                        `json:"checkedAt,omitempty"`
	AttemptedAt       *time.Time                        `json:"attemptedAt,omitempty"`
	ReasonCode        string                            `json:"reasonCode"`
	Reason            string                            `json:"reason"`
	Overall           *CodexCapacityBucketResponse      `json:"overall,omitempty"`
	AdditionalBuckets []CodexCapacityBucketResponse     `json:"additionalBuckets"`
	ResetCredits      *CodexResetCreditsSummaryResponse `json:"resetCredits,omitempty"`
}

// CodexCapacityBucketResponse omits the provider limit identifier.
type CodexCapacityBucketResponse struct {
	DisplayName *string                      `json:"displayName,omitempty"`
	Primary     *CodexCapacityWindowResponse `json:"primary,omitempty"`
	Secondary   *CodexCapacityWindowResponse `json:"secondary,omitempty"`
	Reached     string                       `json:"reached" enum:"not_reached,reached,unknown"`
}

// CodexCapacityWindowResponse contains a normalized provider meter window.
type CodexCapacityWindowResponse struct {
	UsedPercent           float64    `json:"usedPercent" minimum:"0" maximum:"100"`
	WindowDurationMinutes *int64     `json:"windowDurationMinutes,omitempty"`
	ResetsAt              *time.Time `json:"resetsAt,omitempty"`
}

// CodexResetCreditsSummaryResponse contains no provider reset-credit identity.
type CodexResetCreditsSummaryResponse struct {
	AvailableCount   int64      `json:"availableCount" minimum:"0"`
	NearestExpiresAt *time.Time `json:"nearestExpiresAt,omitempty"`
}

// CodexAccountUsageSummaryResponse contains normalized aggregate usage metrics.
type CodexAccountUsageSummaryResponse struct {
	LatestDayTokens           *int64    `json:"latestDayTokens,omitempty"`
	LatestDayStartDate        *string   `json:"latestDayStartDate,omitempty"`
	LifetimeTokens            *int64    `json:"lifetimeTokens,omitempty"`
	PeakDailyTokens           *int64    `json:"peakDailyTokens,omitempty"`
	LongestRunningTurnSeconds *int64    `json:"longestRunningTurnSeconds,omitempty"`
	CurrentStreakDays         *int64    `json:"currentStreakDays,omitempty"`
	LongestStreakDays         *int64    `json:"longestStreakDays,omitempty"`
	ObservedAt                time.Time `json:"observedAt"`
}

// CodexCapabilityObservationResponse is one UI-safe capability result.
type CodexCapabilityObservationResponse struct {
	State      string `json:"state" enum:"supported,unsupported,unknown"`
	ReasonCode string `json:"reasonCode"`
	Reason     string `json:"reason"`
}

// CodexAccountCapabilitiesResponse is the renderer-consumed capability view.
type CodexAccountCapabilitiesResponse struct {
	NativeLogin        CodexCapabilityObservationResponse `json:"nativeLogin"`
	ResetCreditConsume CodexCapabilityObservationResponse `json:"resetCreditConsume"`
	GlobalSwitch       CodexCapabilityObservationResponse `json:"globalSwitch"`
}

// CodexUnmanagedGlobalAccountResponse explains a device identity AO cannot manage.
type CodexUnmanagedGlobalAccountResponse struct {
	Label        string  `json:"label"`
	AuthMethod   string  `json:"authMethod" enum:"chatgpt,api_key,other,unknown"`
	AccountEmail *string `json:"accountEmail,omitempty"`
	ReasonCode   string  `json:"reasonCode"`
	Reason       string  `json:"reason"`
}

// EnsureCodexAccountsRequest selects accounts for display reads.
type EnsureCodexAccountsRequest struct {
	AccountIDs   []string `json:"accountIds,omitempty"`
	IncludeUsage bool     `json:"includeUsage,omitempty"`
}

// ConsumeCodexAccountResetCreditRequest identifies one idempotent provider
// reset attempt. The provider selects the available reset credit.
type ConsumeCodexAccountResetCreditRequest struct {
	IdempotencyKey string `json:"idempotencyKey" minLength:"1" maxLength:"200"`
}

// OpenCodexAccountLoginTerminalResponse is the standalone terminal opened for
// one pending account's native Codex login flow.
type OpenCodexAccountLoginTerminalResponse struct {
	Operation     CodexAccountLoginResponse         `json:"operation"`
	ShellTerminal CodexAccountLoginTerminalResponse `json:"shellTerminal"`
}

// CodexAccountLoginResponse is the redacted login-operation projection.
type CodexAccountLoginResponse struct {
	OperationID string                `json:"operationId"`
	AccountID   string                `json:"accountId,omitempty"`
	Status      string                `json:"status" enum:"pending,verifying,unauthorized,unverified,completed,cancelled,failed,expired"`
	ReasonCode  string                `json:"reasonCode"`
	Reason      string                `json:"reason"`
	Account     *CodexAccountResponse `json:"account,omitempty"`
	ExpiresAt   time.Time             `json:"expiresAt"`
}

// CodexActiveLoginResponse lets a renderer remount reattach to a live login.
type CodexActiveLoginResponse struct {
	OperationID   string                            `json:"operationId"`
	AccountID     string                            `json:"accountId,omitempty"`
	Status        string                            `json:"status" enum:"pending,verifying,unauthorized,unverified,completed,cancelled,failed,expired"`
	ReasonCode    string                            `json:"reasonCode"`
	Reason        string                            `json:"reason"`
	ExpiresAt     time.Time                         `json:"expiresAt"`
	ShellTerminal CodexAccountLoginTerminalResponse `json:"shellTerminal"`
}

// CodexAccountLoginTerminalResponse contains only the mux identity and display
// fields needed by the inline Settings terminal. Its private credential-home
// working directory is deliberately excluded from the public API.
type CodexAccountLoginTerminalResponse struct {
	HandleID  string    `json:"handleId"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
}

// StartCodexAccountSwitchRequest requests an idempotent global account change.
type StartCodexAccountSwitchRequest struct {
	TargetAccountID         string `json:"targetAccountId" minLength:"1"`
	ExpectedAccountRevision int64  `json:"expectedAccountRevision" minimum:"0"`
	IdempotencyKey          string `json:"idempotencyKey" minLength:"1"`
}

// CodexAccountSwitchIDParam describes the durable switch path parameter.
type CodexAccountSwitchIDParam struct {
	SwitchID string `path:"switchId" description:"Durable Codex account switch identifier."`
}

// CodexAccountSwitchPhase is the retained public switch lifecycle.
type CodexAccountSwitchPhase string

// CodexAccountSwitchResponse contains only safe AO identifiers and progress.
type CodexAccountSwitchResponse struct {
	ID                     string                              `json:"id"`
	SourceAccountID        string                              `json:"sourceAccountId"`
	TargetAccountID        string                              `json:"targetAccountId"`
	Phase                  CodexAccountSwitchPhase             `json:"phase" enum:"requested,stopping_sessions,sessions_stopped,checkpointing_source,activating_target,verifying_target,restarting_sessions,rollback_required,recovery_required,completed,failed"`
	FailureCode            string                              `json:"failureCode,omitempty"`
	Sessions               []CodexAccountSwitchSessionResponse `json:"sessions"`
	CanRecover             bool                                `json:"canRecover"`
	CredentialsCommittedAt *time.Time                          `json:"credentialsCommittedAt,omitempty"`
	CreatedAt              time.Time                           `json:"createdAt"`
	UpdatedAt              time.Time                           `json:"updatedAt"`
	CompletedAt            *time.Time                          `json:"completedAt,omitempty"`
}

// CodexAccountSwitchSessionResponse is safe AO session progress for a switch.
type CodexAccountSwitchSessionResponse struct {
	SessionID     string     `json:"sessionId"`
	InterfaceMode string     `json:"interfaceMode" enum:"tui,chat"`
	WasRunning    bool       `json:"wasRunning"`
	StopState     string     `json:"stopState"`
	RestartState  string     `json:"restartState"`
	ErrorCode     string     `json:"errorCode,omitempty"`
	StoppedAt     *time.Time `json:"stoppedAt,omitempty"`
	RestartedAt   *time.Time `json:"restartedAt,omitempty"`
}

// AgentReadinessSnapshot is one normalized harness readiness view.
type AgentReadinessSnapshot = domain.AgentReadinessSnapshot

// AgentInstallationObservation is the normalized binary-presence observation.
type AgentInstallationObservation = domain.AgentInstallationObservation

// AgentAuthenticationObservation is the normalized authentication observation.
type AgentAuthenticationObservation = domain.AgentAuthenticationObservation

// AgentModelsQuery scopes a model catalog to a project where providers may be
// configured per workspace.
type AgentModelsQuery struct {
	ProjectID string `query:"projectId,omitempty" description:"Optional project identifier used as the model-catalog cache scope."`
}

// AgentModelsRefreshQuery controls forced refresh versus cheap background
// revalidation for a project-scoped model catalog.
type AgentModelsRefreshQuery struct {
	ProjectID  string `query:"projectId,omitempty" description:"Optional project identifier used as the model-catalog cache scope."`
	Revalidate bool   `query:"revalidate,omitempty" description:"When true, compare executable and config metadata before running discovery."`
}

// AgentModelsResponse is the normalized model picker for one agent.
type AgentModelsResponse = ports.AgentModelCatalog

// AgentModelInfo is one selectable model or agent-owned mode.
type AgentModelInfo = ports.AgentModelInfo

// AgentInfo is one supported or installed agent entry.
type AgentInfo = agentsvc.Info

// ListUsageSessionsQuery is the query string accepted by GET
// /api/v1/usage/sessions.
type ListUsageSessionsQuery struct {
	ProjectID domain.ProjectID `query:"projectId,omitempty" description:"Optional project id filter for dashboard cards."`
}

// EstimatedCostResponse is a nano-USD estimate reused at every usage summary
// scope. Coverage stays an API fact for aggregation, catalog backfills, and
// contextual disclosure; it is never a pricing label or a mathematical
// qualifier on the presented value.
type EstimatedCostResponse struct {
	TotalNanos          int64  `json:"totalNanos" minimum:"0" format:"int64"`
	InputNanos          *int64 `json:"inputNanos" minimum:"0" format:"int64" description:"Every non-cache-read input charge, cache writes included."`
	CachedInputNanos    *int64 `json:"cachedInputNanos" minimum:"0" format:"int64"`
	OutputNanos         *int64 `json:"outputNanos" minimum:"0" format:"int64"`
	Coverage            string `json:"coverage" enum:"complete,partial"`
	ProviderAttribution string `json:"providerAttribution" enum:"observed,inferred,mixed" description:"Whether contributing billing providers were detected, inferred from model ownership, or both."`
}

// CompactSessionUsageResponse is one session card's usage summary.
type CompactSessionUsageResponse struct {
	SessionID       domain.SessionID       `json:"sessionId"`
	ProcessedTokens *int64                 `json:"processedTokens" minimum:"0" description:"Canonical input plus output. Null when either component is unknown."`
	TotalTokens     int64                  `json:"totalTokens" minimum:"0" description:"Deprecated compatibility alias for processedTokens."`
	Incomplete      bool                   `json:"incomplete"`
	EstimatedCost   *EstimatedCostResponse `json:"estimatedCost"`
}

// ListCompactSessionUsageResponse is the batch dashboard usage response.
type ListCompactSessionUsageResponse struct {
	Sessions []CompactSessionUsageResponse `json:"sessions"`
}

// UsageTotalsResponse is the canonical telemetry aggregate for one scope.
//
// Provider-specific counters are no longer projected here: they live verbatim
// in each event's bounded provider usage object, where a field the provider
// adds later survives without a schema change on this boundary.
type UsageTotalsResponse struct {
	InputTokens         *int64                 `json:"inputTokens" minimum:"0" description:"Total input, including cached and uncached input."`
	CachedInputTokens   *int64                 `json:"cachedInputTokens" minimum:"0" description:"Input read from an existing provider cache. Cache hit percentage uses cachedInputTokens divided by inclusive inputTokens."`
	UncachedInputTokens *int64                 `json:"uncachedInputTokens" minimum:"0" description:"Input not read from an existing provider cache. Includes cache writes."`
	OutputTokens        *int64                 `json:"outputTokens" minimum:"0" description:"Total output, including provider-specific subsets such as reasoning output."`
	ProcessedTokens     *int64                 `json:"processedTokens" minimum:"0" description:"Canonical input plus output. Null when either component is unknown."`
	CacheReadTokens     *int64                 `json:"cacheReadTokens" minimum:"0" description:"Deprecated compatibility alias for cachedInputTokens."`
	EstimatedCost       *EstimatedCostResponse `json:"estimatedCost"`
}

// UsageModelResponse is telemetry grouped by model. The billing provider is a
// pricing input rather than a product distinction: each event was costed
// against its own provider's rates before reaching this aggregate, so one model
// stays one row even when more than one provider served it.
type UsageModelResponse struct {
	ModelID string              `json:"modelId"`
	Totals  UsageTotalsResponse `json:"totals"`
}

// UsageHarnessResponse groups model telemetry under one AO harness.
type UsageHarnessResponse struct {
	Harness string               `json:"harness"`
	Totals  UsageTotalsResponse  `json:"totals"`
	Models  []UsageModelResponse `json:"models"`
}

// SessionUsageResponse is detailed telemetry for the session inspector.
type SessionUsageResponse struct {
	SessionID  domain.SessionID       `json:"sessionId"`
	Incomplete bool                   `json:"incomplete"`
	Totals     UsageTotalsResponse    `json:"totals"`
	Harnesses  []UsageHarnessResponse `json:"harnesses"`
}

// SystemRequirementsResponse is the body of GET /api/v1/system/requirements.
type SystemRequirementsResponse = systemcheck.Report

// GitHubAuthRequirementResponse is the advisory GitHub credential probe.
type GitHubAuthRequirementResponse = systemcheck.Requirement

// InstallTargetParam is the {target} path parameter for /system/install routes.
type InstallTargetParam struct {
	Target string `path:"target" enum:"tmux,gh,claude,codex,opencode,copilot,cloudflared" description:"Install target identifier: tmux, gh, claude, codex, opencode, copilot, or cloudflared."`
}

// StartInstallResponse is the body of POST /api/v1/system/install/{target} (202).
type StartInstallResponse = systeminstall.Job

// InstallStatusResponse is the body of GET /api/v1/system/install/{target}.
type InstallStatusResponse = systeminstall.Job

// AgentInstallResponse is shared by the agent harness start and status routes.
type AgentInstallResponse = systeminstall.Job

// StartAgentInstallRequest selects one method returned by the installer
// catalog. The daemon still owns the argv behind the method id.
type StartAgentInstallRequest struct {
	Method    string                       `json:"method,omitempty" description:"Server-issued installation method id. Omit to use the recommended viable method."`
	Operation systeminstall.AgentOperation `json:"operation,omitempty" enum:"install,reinstall" description:"Requested operation. Defaults to install for older clients."`
}

// AgentInstallJobsResponse hydrates Settings with the latest durable job for
// every harness that has been installed or verified.
type AgentInstallJobsResponse struct {
	Jobs []systeminstall.Job `json:"jobs"`
}

// ListNotificationsQuery is the query string accepted by GET /api/v1/notifications.
type ListNotificationsQuery struct {
	Status string `query:"status,omitempty" enum:"unread,all,unresolved" description:"Notification filter. Defaults to unread (unseen); unresolved returns notifications whose underlying issue is still open; all includes read history."`
	Limit  int    `query:"limit,omitempty" minimum:"1" maximum:"100" description:"Maximum notifications to return. Defaults to 100."`
	Cursor string `query:"cursor,omitempty" description:"Opaque cursor returned by the previous page."`
}

// NotificationStreamQuery is the query string accepted by GET /api/v1/notifications/stream.
type NotificationStreamQuery struct {
	ProjectID string `query:"projectId,omitempty" description:"Optional project id filter for live notifications."`
}

// NotificationIDParam is the {id} path parameter shared by notification routes.
type NotificationIDParam struct {
	ID string `path:"id" description:"Notification identifier."`
}

// NotificationTarget is the dashboard navigation target for a notification.
type NotificationTarget struct {
	Kind      string `json:"kind" enum:"session,pr"`
	SessionID string `json:"sessionId"`
	PRURL     string `json:"prUrl,omitempty"`
}

// NotificationResponse is one stored notification returned by the API.
type NotificationResponse struct {
	ID        string    `json:"id"`
	SessionID string    `json:"sessionId"`
	ProjectID string    `json:"projectId"`
	PRURL     string    `json:"prUrl"`
	Type      string    `json:"type" enum:"needs_input,ready_to_merge,pr_merged,pr_closed_unmerged"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Status    string    `json:"status" enum:"unread,read" description:"Seen state. unread means the user has not opened the notification panel since it arrived."`
	CreatedAt time.Time `json:"createdAt"`
	// ResolvedAt is set by AO when the underlying issue goes away (the session
	// received its input, the PR stopped waiting on a merge). Absent means the
	// issue is still open. There is no user-facing action that sets it.
	ResolvedAt *time.Time         `json:"resolvedAt,omitempty"`
	Target     NotificationTarget `json:"target"`
}

// ListNotificationsResponse is one history page from GET /api/v1/notifications.
type ListNotificationsResponse struct {
	Notifications   []NotificationResponse `json:"notifications"`
	NextCursor      string                 `json:"nextCursor,omitempty"`
	UnreadCount     int                    `json:"unreadCount"`
	UnresolvedCount int                    `json:"unresolvedCount"`
}

// MarkNotificationReadRequest is the body of PATCH /api/v1/notifications/{id}.
type MarkNotificationReadRequest struct {
	Status string `json:"status" enum:"read" description:"V1 supports only marking an unread notification read."`
}

// NotificationEnvelope is the { notification } response body for notification mutations.
type NotificationEnvelope struct {
	Notification NotificationResponse `json:"notification"`
}

// ShellTerminalHandleIDParam is the {handleId} path parameter for shell
// terminal routes. It is the runtime handle the terminal mux attaches to, not
// a session id.
type ShellTerminalHandleIDParam struct {
	HandleID string `path:"handleId" description:"Shell terminal runtime handle identifier."`
}

// OpenShellTerminalRequest is the body of POST /api/v1/shell-terminals.
type OpenShellTerminalRequest struct {
	ProjectID string `json:"projectId,omitempty" description:"Project whose root the shell starts in. Omitted opens the shell in the daemon data dir."`
	SessionID string `json:"sessionId,omitempty" description:"Agent session the shell is scoped to, so it appears only in that session's tab strip. Omitted makes it a standalone shell."`
	Shell     string `json:"shell,omitempty" description:"Windows shell selector: auto, git-bash, pwsh, powershell, cmd, or a custom executable path. Ignored on macOS and Linux."`
}

// UpdateShellTerminalRequest is the body of PATCH /api/v1/shell-terminals/{handleId}.
type UpdateShellTerminalRequest struct {
	Title string `json:"title" description:"New tab title for the shell terminal. Trimmed; must be non-empty."`
}

// ShellTerminalResponse is one standalone shell terminal. HandleID is what the
// client opens on the terminal mux, exactly as it would a session's pane.
type ShellTerminalResponse struct {
	HandleID   string    `json:"handleId"`
	ProjectID  string    `json:"projectId,omitempty"`
	SessionID  string    `json:"sessionId,omitempty"`
	WorkingDir string    `json:"workingDir"`
	Title      string    `json:"title"`
	CreatedAt  time.Time `json:"createdAt"`
}

// ListShellTerminalsResponse is the body of GET /api/v1/shell-terminals.
type ListShellTerminalsResponse struct {
	ShellTerminals []ShellTerminalResponse `json:"shellTerminals"`
}

// ShellTerminalEnvelope is the { shellTerminal } response body for shell
// terminal mutations.
type ShellTerminalEnvelope struct {
	ShellTerminal ShellTerminalResponse `json:"shellTerminal"`
}

// MarkAllNotificationsReadRequest is the optional body of
// POST /api/v1/notifications/read-all.
type MarkAllNotificationsReadRequest struct {
	IDs []string `json:"ids,omitempty" description:"Acknowledge exactly these notifications. Omit to acknowledge every unread notification; paginating clients should send the ids they actually rendered so later pages stay unread."`
}

// MarkAllNotificationsReadResponse is the body of POST /api/v1/notifications/read-all.
type MarkAllNotificationsReadResponse struct {
	Notifications []NotificationResponse `json:"notifications" description:"Deprecated compatibility field. Always empty so mark-all responses stay bounded."`
	UpdatedCount  int64                  `json:"updatedCount" description:"Number of notifications changed from unread to read."`
}

// ImportStatusResponse is the body of GET /api/v1/import: whether a legacy AO
// install is available to import, and the root the daemon would read from.
type ImportStatusResponse struct {
	Available  bool   `json:"available"`
	LegacyRoot string `json:"legacyRoot"`
}

// ImportRunResponse is the body of POST /api/v1/import: the structured outcome
// of the import run (counts + notes), reused verbatim from the import engine.
type ImportRunResponse struct {
	Report legacyimport.Report `json:"report"`
}

// DevImportProjectsRequest is the body of POST /api/v1/dev/import-projects.
type DevImportProjectsRequest struct {
	SourceDataDir string `json:"sourceDataDir" minLength:"1"`
	DryRun        bool   `json:"dryRun"`
}

// DevImportProjectsResponse is the body of POST /api/v1/dev/import-projects.
type DevImportProjectsResponse struct {
	Report devimport.Report `json:"report"`
}

// PRIDParam is the {id} path parameter shared by the /prs/{id} routes.
type PRIDParam struct {
	ID string `path:"id" description:"PR number."`
}

// MergePRRequest is the body of POST /api/v1/prs/{id}/merge.
type MergePRRequest struct {
	PRURL           string `json:"prUrl" minLength:"1"`
	ExpectedHeadSHA string `json:"expectedHeadSha" minLength:"40"`
}

// MergePRResponse is the body of POST /api/v1/prs/{id}/merge (200).
type MergePRResponse struct {
	OK       bool   `json:"ok"`
	PRNumber int    `json:"prNumber"`
	Method   string `json:"method"`
}

// ResolveCommentsRequest is the optional body of POST /api/v1/prs/{id}/resolve-comments.
type ResolveCommentsRequest struct {
	CommentIDs []string `json:"commentIds,omitempty"`
}

// ResolveCommentsResponse is the body of POST /api/v1/prs/{id}/resolve-comments (200).
type ResolveCommentsResponse struct {
	OK       bool `json:"ok"`
	Resolved int  `json:"resolved"`
}

// EndpointsResponse is the body of GET /api/v1/endpoints. The phone re-reads
// it after every successful connect, so a rotated tunnel hostname or a changed
// LAN address is picked up without re-pairing.
type EndpointsResponse struct {
	Endpoints []mobilebridge.Endpoint `json:"endpoints"`
}

// IdentityResponse is the body of the unauthenticated GET /api/v1/identity
// probe. It is deliberately minimal: the route is reachable without the
// connection password, so it must carry nothing but an opaque host id and the
// mobile contract version.
type IdentityResponse struct {
	HostID     string `json:"hostId"`
	APIVersion int    `json:"apiVersion"`
}

// MobileStatusResponse is the body of the Connect Mobile status/enable/disable/
// regenerate endpoints. Password is populated only transiently, on enable and
// regenerate responses (empty otherwise) — it is never persisted in plaintext.
type MobileStatusResponse struct {
	Enabled bool `json:"enabled"`
	// Endpoints is every way the phone can reach this daemon, in the client's
	// preference order. The phone races them; Host/TailscaleHost below are the
	// head of each kind, kept for the existing renderer.
	Endpoints []mobilebridge.Endpoint `json:"endpoints"`
	// HostID is this machine's stable identity, echoed into the pairing code.
	// The phone checks every endpoint it races against this value.
	HostID string `json:"hostId"`
	// Tunnel is the managed remote-access connector's state, so the desktop can
	// show progress during the tens of seconds before it is advertisable.
	Tunnel mobilebridge.TunnelStatus `json:"tunnel"`
	Host   string                    `json:"host"`
	// TailscaleHost is this machine's 100.64.0.0/10 Tailscale address, or "" when
	// Tailscale is not up. The renderer encodes it into the pairing QR when the
	// user selects the Tailscale tab, and shows a hint instead when it is empty.
	TailscaleHost string              `json:"tailscaleHost"`
	Port          int                 `json:"port"`
	Password      string              `json:"password"`
	Warning       string              `json:"warning"`
	SecurePairing SecurePairingStatus `json:"securePairing"`
}

// SecurePairingStatus describes the optional TLS-over-Tailscale pairing mode,
// in which `tailscale serve` fronts the bridge with a real certificate so iOS
// can pair by scanning (App Transport Security blocks cleartext to Tailscale's
// 100.64.0.0/10 range).
type SecurePairingStatus struct {
	Enabled   bool   `json:"enabled"`   // the user turned the mode on
	Available bool   `json:"available"` // CLI present, MagicDNS name known, certs enabled
	Active    bool   `json:"active"`    // proxy verified pointing at the live bridge port
	Host      string `json:"host"`      // MagicDNS name; "" when unknown
	Port      int    `json:"port"`      // 443 when active, else 0
	// Reason is a fixed enum the renderer maps to localized setup steps:
	// no_cli, no_magicdns, no_certs, serve_failed, port_mismatch, clear_failed.
	// Empty when Available. Never a raw error string — those are untranslated
	// and can leak paths.
	Reason string `json:"reason"`
}

// SetSecurePairingRequest is the body of POST /api/v1/mobile/secure-pairing.
type SetSecurePairingRequest struct {
	Enabled bool `json:"enabled"`
}

// PushPairingIDParam is the {id} path parameter for the unpair route. It accepts
// either the phone's install ID or, from builds that predate install IDs, its
// push token.
type PushPairingIDParam struct {
	ID string `path:"id" description:"The phone's install id, or its push token for older builds."`
}

// PushDeviceTokenParam is the {token} path parameter for push-device routes.
type PushDeviceTokenParam struct {
	Token string `path:"token" description:"Expo push token (URL-encoded) identifying the device."`
}

// RegisterPushDeviceRequest is the body of POST /api/v1/push/devices. The phone
// sends its Expo push token plus a bit of descriptive metadata; the daemon keys
// the registry on the install ID (the token is an attribute and is now optional)
// and re-registering is an idempotent upsert.
type RegisterPushDeviceRequest struct {
	// Optional so the published contract matches what the daemon actually
	// accepts: app builds predating install IDs send none, and the handler
	// synthesizes a legacy one rather than rejecting them. Marking it required
	// would generate clients unable to express a request the server handles.
	InstallID string `json:"installId,omitempty" description:"Stable per-install device id, keying the registry so a rotated push token updates the same row. Optional: older app builds omit it and the daemon synthesizes one."`
	// Optional: a row represents a paired phone, not a push registration. Omitted
	// (or empty) when the phone is only announcing its identity — permission not
	// yet granted, or a build that can't mint a token. When present it must still
	// be a well-formed Expo push token.
	Token      string `json:"token,omitempty" description:"Expo push token, e.g. ExponentPushToken[...]. Optional: omitted when the phone has no push token yet."`
	Platform   string `json:"platform,omitempty" enum:"ios,android" description:"Device platform."`
	DeviceName string `json:"deviceName,omitempty" description:"Human-friendly device label."`
}

// PushDeviceResponse is the stored view of a registered push device.
type PushDeviceResponse struct {
	Token      string    `json:"token,omitempty"`
	Platform   string    `json:"platform,omitempty"`
	DeviceName string    `json:"deviceName,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
}

// PushDeviceEnvelope is the { device } response body for a registered push device.
type PushDeviceEnvelope struct {
	Device PushDeviceResponse `json:"device"`
}

// UnregisterPushDeviceResponse is the body of DELETE /api/v1/push/devices/{token} (200).
type UnregisterPushDeviceResponse struct {
	Token   string `json:"token"`
	Deleted bool   `json:"deleted"`
}

/* ---- chat conversations ------------------------------------------------ */

// SendConversationMessageRequest is a message for a Chat session's agent.
type SendConversationMessageRequest struct {
	Text string `json:"text"`
	// ClientMessageID makes delivery idempotent. A retry carrying the same value
	// must not produce a second provider turn.
	ClientMessageID string                               `json:"clientMessageId,omitempty"`
	Attachments     []ConversationImageContentRequest    `json:"attachments,omitempty"`
	Resources       []ConversationResourceContentRequest `json:"resources,omitempty"`
}

// ConversationImageContentRequest is a native raster image prompt block.
type ConversationImageContentRequest struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

// EditQueuedConversationMessageRequest changes an undispatched prompt. Omitted
// retainedContent preserves the stored blocks; an empty list removes attachments.
type EditQueuedConversationMessageRequest struct {
	Text        string                            `json:"text"`
	Attachments []ConversationImageContentRequest `json:"attachments,omitempty"`
	// Indices in the message's public content summary, in their original order.
	RetainedContent *[]int `json:"retainedContent,omitempty"`
	// Reject a stale editor before interpreting attachment indices.
	ExpectedRevision *int64 `json:"expectedRevision,omitempty"`
}

// ConversationResourceContentRequest is a resource link, or embedded text when
// Text is present and the provider negotiated embedded context.
type ConversationResourceContentRequest struct {
	URI      string  `json:"uri"`
	Name     string  `json:"name"`
	MIMEType string  `json:"mimeType,omitempty"`
	Text     *string `json:"text,omitempty"`
}

// SendConversationMessageResponse reports what the send did.
type SendConversationMessageResponse struct {
	TurnID         string `json:"turnId,omitempty"`
	ProviderTurnID string `json:"providerTurnId,omitempty"`
	// State is `running` when the agent picked the message up immediately and
	// `queued` when it arrived mid-turn and will be sent once the turn ends. A
	// client that only reads turnId cannot tell those apart, and "accepted" is not
	// the same claim as "delivered".
	State domain.TurnState `json:"state,omitempty" enum:"queued,running,completed,recovered,interrupted,failed"`
	// Duplicate is true when this client message id was already delivered, so a
	// retrying client can stop instead of assuming a new turn began.
	Duplicate bool `json:"duplicate"`
}

// SteerConversationRequest is guidance for a turn that is already running.
type SteerConversationRequest struct {
	// Text is the correction to hand the agent mid-turn.
	Text string `json:"text"`
	// Attachments are native image prompt blocks delivered with the correction.
	Attachments []ConversationImageContentRequest `json:"attachments,omitempty"`
	// ClientMessageID makes a retry idempotent: the same handle updates the recorded
	// guidance instead of adding a second copy of it, and the provider echoes it back
	// on the item it replays so a client can recognize its own steer.
	ClientMessageID string `json:"clientMessageId,omitempty"`
}

// SteerConversationResponse reports the turn the guidance joined.
type SteerConversationResponse struct {
	// ProviderTurnID is the turn that absorbed it. Against Codex this is the turn
	// that was already running — steering does not open a new one — so a client
	// matches it against the turn it is already rendering.
	ProviderTurnID string `json:"providerTurnId"`
	// ActivityID is the timeline row recording the guidance, so an optimistic bubble
	// can be reconciled with the durable one rather than shown twice.
	ActivityID string `json:"activityId,omitempty"`
}

// EditConversationMessageRequest changes the readable text of one durable human
// prompt. Structured content is intentionally absent: the service reuses the
// server-side blocks recorded with the original message.
type EditConversationMessageRequest struct {
	Text            string `json:"text"`
	ClientMessageID string `json:"clientMessageId,omitempty"`
}

// ConversationContentSummaryResponse is a lightweight attachment/resource chip.
// Image bytes and embedded resource text never leave the durable server record.
type ConversationContentSummaryResponse struct {
	Type     string `json:"type"`
	MIMEType string `json:"mimeType,omitempty"`
	URI      string `json:"uri,omitempty"`
	Name     string `json:"name,omitempty"`
}

// EditConversationMessageResponse identifies the newly selected branch and its
// replacement turn.
type EditConversationMessageResponse struct {
	SourceBranchID string           `json:"sourceBranchId"`
	ActiveBranchID string           `json:"activeBranchId"`
	TurnID         string           `json:"turnId,omitempty"`
	ProviderTurnID string           `json:"providerTurnId,omitempty"`
	State          domain.TurnState `json:"state,omitempty" enum:"queued,running,completed,recovered,interrupted,failed"`
}

// RetryTurnResponse reports the new turn a retry dispatched. The original failed
// turn is not referenced here: it stays failed and unchanged, and both attempts
// remain separately visible in history.
type RetryTurnResponse struct {
	TurnID         string           `json:"turnId,omitempty"`
	ProviderTurnID string           `json:"providerTurnId,omitempty"`
	State          domain.TurnState `json:"state,omitempty" enum:"queued,running,completed,recovered,interrupted,failed"`
}

// ActivateConversationBranchResponse reports the durable head after switching.
type ActivateConversationBranchResponse struct {
	ActiveBranchID string `json:"activeBranchId"`
}

// ConversationModelsResponse is the provider's model catalog plus what is selected.
type ConversationModelsResponse struct {
	Models   []ConversationModelResponse     `json:"models"`
	Selected ConversationTurnSettingsPayload `json:"selected"`
}

// ConversationConfigOptionsResponse is the provider's complete live session
// configuration catalog. Clients replace their cached list after a mutation:
// model changes can add or remove dependent controls.
type ConversationConfigOptionsResponse struct {
	Options []ConversationConfigOptionResponse `json:"options"`
}

// ConversationConfigOptionResponse is one provider-advertised session control.
type ConversationConfigOptionResponse struct {
	ID             string                             `json:"id"`
	Name           string                             `json:"name"`
	Description    string                             `json:"description,omitempty"`
	Category       string                             `json:"category,omitempty"`
	Type           string                             `json:"type" enum:"select,boolean"`
	CurrentValue   string                             `json:"currentValue,omitempty"`
	CurrentBoolean *bool                              `json:"currentBoolean,omitempty"`
	Choices        []ConversationConfigChoiceResponse `json:"choices"`
}

// ConversationConfigChoiceResponse is one value in a provider select.
type ConversationConfigChoiceResponse struct {
	PermissionMode domain.PermissionMode `json:"permissionMode,omitempty" enum:"default,accept-edits,auto,bypass-permissions"`
	Value          string                `json:"value"`
	Name           string                `json:"name"`
	Description    string                `json:"description,omitempty"`
	Group          string                `json:"group,omitempty"`
	GroupName      string                `json:"groupName,omitempty"`
}

// SetConversationConfigOptionRequest selects one provider-advertised value.
// Selects use Value; booleans use Enabled. Exactly one must be present.
type SetConversationConfigOptionRequest struct {
	Value   string `json:"value,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
}

// ConversationModelResponse is one model the provider offers.
type ConversationModelResponse struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Description string `json:"description,omitempty"`
	// Default marks the model the provider would pick on its own, so a client can
	// label it rather than inventing its own idea of a default.
	Default bool `json:"default"`
	// Efforts are the reasoning levels this model supports, in the provider's
	// order. Empty means the model does not take one.
	Efforts       []string `json:"efforts,omitempty"`
	DefaultEffort string   `json:"defaultEffort,omitempty"`
}

// ConversationSkillsResponse is the named skills the provider will let this
// session invoke.
//
// An empty list is a real answer, not a failure: it means this agent offers no
// skills, and a client must render that as "no commands" rather than as an error.
type ConversationSkillsResponse struct {
	Skills []ConversationSkillResponse `json:"skills"`
}

// ConversationSkillResponse is one skill a user can invoke by name.
type ConversationSkillResponse struct {
	// Name is the invocable identifier. It is what a client puts in the message
	// text; DisplayName is only a label.
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Description string `json:"description,omitempty"`
	// InputHint is the provider's short placeholder for command arguments.
	InputHint string `json:"inputHint,omitempty"`
	// Source is where the skill came from (the provider's scope: user, repo,
	// system, admin), so a user can tell a repo skill from one of their own.
	Source string `json:"source,omitempty"`
}

// ConversationTurnSettingsPayload is the provider choices for the next turn. It is
// both the request body for changing them and the echo of what is now stored.
//
// Every field is optional and an empty value means "use the provider's default",
// so clearing a choice and never making one are the same thing.
type ConversationTurnSettingsPayload struct {
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	ApprovalMode    string `json:"approvalMode,omitempty" enum:"default,accept-edits,auto,bypass-permissions"`
}

// ResolveConversationApprovalRequest answers a pending approval. DecisionID must
// be one the provider offered for that request; AO does not invent options.
type ResolveConversationApprovalRequest struct {
	DecisionID string `json:"decisionId"`
}

// ResolveConversationInputRequest answers a structured form or URL-consent
// request. Content is meaningful only for accept.
type ResolveConversationInputRequest struct {
	Action  string         `json:"action" enum:"accept,decline,cancel"`
	Content map[string]any `json:"content,omitempty"`
}

// CompactConversationResponse reports a compaction the provider accepted.
//
// Accepted, not finished. The provider takes the request and does the work as its
// own turn over the following seconds, so this says what is about to be reclaimed
// and the settled figures arrive on the timeline as a compaction entry. A client
// that wants the outcome reads the timeline, which is where it belongs anyway:
// the reclaim is durable history, not the answer to one request.
type CompactConversationResponse struct {
	// TokensBefore is the conversation's context position when compaction was
	// requested. Zero means the provider has not reported one yet, in which case
	// AO deliberately claims no figure rather than guessing at one.
	TokensBefore int64 `json:"tokensBefore,omitempty"`
	// TokensAfter is only set by a provider that compacts synchronously. Zero means
	// the reclaim is still in flight.
	TokensAfter int64 `json:"tokensAfter,omitempty"`
}

// ConversationTurnResponse is one request and the work that followed it.
type ConversationTurnResponse struct {
	ID             string `json:"id"`
	State          string `json:"state" enum:"queued,running,completed,recovered,interrupted,failed,cancelled"`
	ProviderTurnID string `json:"providerTurnId,omitempty"`
	// RetryOfTurnID is the failed source whose durable prompt created this turn.
	RetryOfTurnID string `json:"retryOfTurnId,omitempty"`
	// HasRetryAttempt remains true when the attempt is outside the active branch.
	HasRetryAttempt bool    `json:"hasRetryAttempt,omitempty"`
	ErrorMessage    string  `json:"errorMessage,omitempty"`
	RequestedAt     string  `json:"requestedAt"`
	StartedAt       *string `json:"startedAt,omitempty"`
	CompletedAt     *string `json:"completedAt,omitempty"`
	// RolledBack marks a turn an undo discarded. Its messages and activities are
	// absent from this snapshot because the agent no longer remembers them; the turn
	// is still reported so a client can say what was taken back rather than letting
	// the timeline quietly shrink.
	RolledBack bool `json:"rolledBack,omitempty"`
	// Diff is what this turn has changed on disk. Absent when the provider has
	// reported nothing, which is not a claim that nothing changed: an agent
	// without diff support never reports at all.
	//
	// Carried on the snapshot the client already polls rather than behind its own
	// route. A dedicated route would be a second request, on the same cadence, for
	// data this read has already loaded -- the diff belongs to a turn, and the turn
	// list is right here. It also keeps the changed-file view and the timeline from
	// disagreeing, which two independently-timed fetches would eventually do.
	Diff *ConversationTurnDiffResponse `json:"diff,omitempty"`
	// Plan is the agent's plan for this turn, or absent when it made none. The
	// provider re-sends the whole plan on every change, so this is the current answer
	// rather than a history of one: the earlier versions are the same plan with fewer
	// steps ticked off.
	Plan *ConversationPlanResponse `json:"plan,omitempty"`
}

// ConversationPlanResponse is the agent's plan for one turn.
type ConversationPlanResponse struct {
	// Explanation is the agent's note about the plan as a whole, when it gives one.
	Explanation string                         `json:"explanation,omitempty"`
	Steps       []ConversationPlanStepResponse `json:"steps"`
}

// ConversationPlanStepResponse is one step of a plan.
//
// Structured, not prose. The per-step status is the whole point -- it is where the
// agent is up to -- and a client that wants a sentence can join the steps, while one
// that wants checkboxes cannot recover them from a sentence.
type ConversationPlanStepResponse struct {
	Text   string `json:"text"`
	Status string `json:"status" enum:"pending,in_progress,completed"`
}

// ConversationTurnDiffResponse is a turn's changed-file summary.
type ConversationTurnDiffResponse struct {
	Files []ConversationDiffFileResponse `json:"files"`
	// Truncated reports that the file list was cut at the daemon's cap, so a client
	// does not present a partial list as the whole change.
	Truncated bool `json:"truncated,omitempty"`
}

// ConversationDiffFileResponse is one changed path.
//
// No patch text. The turn view answers "what did this touch, and by how much";
// carrying every hunk would put the full diff into a body polled once a second,
// and AO already has a diff surface for reading the change itself.
type ConversationDiffFileResponse struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Status    string `json:"status" enum:"added,modified,deleted,renamed"`
	// OldPath is set only for a rename.
	OldPath string `json:"oldPath,omitempty"`
	// RolledBack marks a turn an undo discarded. Its messages and activities are
	// absent from this snapshot because the agent no longer remembers them; the turn
	// is still reported so a client can say what was taken back rather than letting
	// the timeline quietly shrink.
	RolledBack bool `json:"rolledBack,omitempty"`
}

// ConversationMessageResponse is one readable block of text.
type ConversationMessageResponse struct {
	Kind          string                               `json:"kind" enum:"message"`
	ID            string                               `json:"id"`
	TurnID        string                               `json:"turnId,omitempty"`
	Sequence      int64                                `json:"sequence"`
	Revision      int64                                `json:"revision"`
	Role          string                               `json:"role" enum:"user,assistant"`
	Origin        string                               `json:"origin" enum:"human,automation,daemon,provider"`
	Text          string                               `json:"text"`
	Content       []ConversationContentSummaryResponse `json:"content,omitempty"`
	EditAvailable bool                                 `json:"editAvailable"`
	// Streaming is true while more deltas are expected for this message.
	Streaming bool   `json:"streaming"`
	CreatedAt string `json:"createdAt"`
}

// ConversationActivityResponse is one non-message timeline entry.
type ConversationActivityResponse struct {
	Kind     string `json:"kind" enum:"activity"`
	ID       string `json:"id"`
	TurnID   string `json:"turnId,omitempty"`
	Sequence int64  `json:"sequence"`
	Revision int64  `json:"revision"`
	// ActivityKind discriminates the payload in Detail.
	//
	// mcp_tool is not command: an MCP call has a server, a tool name, structured
	// arguments and a structured result, and rendering it as a shell command claimed
	// the agent had run something in the worktree. auto_review is not approval: an
	// approval is a question waiting on a person, while an auto-review is a decision
	// the provider already made on their behalf, and those are opposites.
	ActivityKind string `json:"activityKind" enum:"command,file_change,plan,reasoning,approval,usage,error,system,mcp_tool,auto_review,user_input"`
	Status       string `json:"status" enum:"running,completed,recovered,failed,cancelled,pending,resolved"`
	Summary      string `json:"summary"`
	// Detail is the provider-neutral typed payload for this kind. For an approval
	// it carries the provider's own offered decisions, which is what the client
	// renders buttons from.
	//
	// The keys that depend on the kind:
	//
	//   command      command, rawCommand, cwd, exitCode, durationMs, processId,
	//                output (+ outputSource, outputMayBePartial, outputTruncated),
	//                terminalInput -- the keystrokes the agent sent to the PTY, kept
	//                out of output because the PTY echoes them
	//   file_change  files[] with path, oldPath, status, additions, deletions, patch
	//   reasoning    text -- streamed while the model works, replaced by the
	//                provider's settled summary when the item completes
	//   mcp_tool     server, toolName, namespace, arguments, result, error, success,
	//                progress
	//   plan         event "plan", explanation, steps[] with text and status
	//   auto_review  reviewId, targetItemId, actionType, command, riskLevel,
	//                rationale, decisionSource, status, durationMs
	//   user_input   inputMode, message, schema, url, elicitationId
	//   system       event -- "compaction", "model.rerouted" or
	//                "auth.reauth_required" -- plus that event's own fields
	Detail    map[string]any `json:"detail,omitempty"`
	RequestID string         `json:"requestId,omitempty"`
	// ProviderItemID is the stable parent key used by nested ACP transcripts.
	ProviderItemID string `json:"providerItemId,omitempty"`
	CreatedAt      string `json:"createdAt"`
}

// ConversationSnapshotResponse is the durable read model a client bootstraps from.
type ConversationSnapshotResponse struct {
	ConversationID             string `json:"conversationId"`
	ActiveBranchID             string `json:"activeBranchId,omitempty"`
	BranchedFromEarlierMessage bool   `json:"branchedFromEarlierMessage"`
	SessionID                  string `json:"sessionId"`
	Harness                    string `json:"harness,omitempty"`
	Mode                       string `json:"mode" enum:"chat,tui"`
	// Controller is reported separately from history so a client can tell "no
	// messages yet" apart from "the agent is not running".
	Controller     string `json:"controller" enum:"connecting,ready,busy,recovering,stopped"`
	LatestSequence int64  `json:"latestSequence"`
	OldestSequence int64  `json:"oldestSequence,omitempty"`
	HasMoreBefore  bool   `json:"hasMoreBefore"`
	// NativeForkAvailableAfterSequence is the first provider-backed human prompt
	// in the active provider scope. It keeps edit gating exact across bounded pages.
	NativeForkAvailableAfterSequence int64                             `json:"nativeForkAvailableAfterSequence"`
	Turns                            []ConversationTurnResponse        `json:"turns"`
	Messages                         []ConversationMessageResponse     `json:"messages"`
	Activities                       []ConversationActivityResponse    `json:"activities"`
	BranchPoints                     []ConversationBranchPointResponse `json:"branchPoints,omitempty"`
	// BranchMaterialization says whether the selected provider branch preserved
	// native history or was rebuilt from AO's bounded text transcript. Omitted for
	// conversations that have no durable branch metadata yet.
	BranchMaterialization *ConversationBranchMaterializationResponse `json:"branchMaterialization,omitempty"`
	// Settings are the provider choices for the next turn. Carried on the snapshot
	// the client already polls so the composer can label itself without a second
	// request, and so a choice made on another client shows up here.
	Settings ConversationTurnSettingsPayload `json:"settings"`
	// Title is the name the provider currently gives this thread. Empty means it has
	// not named one, which is not the same as an empty name.
	Title string `json:"title,omitempty"`
	// Usage is how full this conversation is. Omitted until the provider reports,
	// so a client can tell "not known yet" from a conversation using nothing.
	Usage *ConversationUsagePayload `json:"usage,omitempty"`
	// RateLimits is where the account stands. Omitted until the provider reports.
	RateLimits *ConversationRateLimitsPayload `json:"rateLimits,omitempty"`
	// CompactedAt is when history was last summarized to reclaim context, or absent
	// if never. On the snapshot rather than derived from the timeline so a client can
	// label the control without scanning every activity.
	CompactedAt *string `json:"compactedAt,omitempty"`
	// ModelReroute is present when the provider answered with a model other than the
	// one that was asked for. A client MUST prefer this over the selected model when
	// naming what produced the answers: without it the composer keeps advertising a
	// model that is not replying.
	ModelReroute *ConversationModelReroutePayload `json:"modelReroute,omitempty"`
	// Account is the provider account this conversation runs under. Omitted until the
	// provider says anything about it.
	Account *ConversationAccountPayload `json:"account,omitempty"`
	// ThreadState is the provider's own lifecycle view of the thread. It is NOT the
	// session's status, which stays derived from durable facts and is served on the
	// session resource; this is one more such fact.
	ThreadState *ConversationThreadStatePayload `json:"threadState,omitempty"`
	// MCPServers is the startup state of the tool servers this conversation can
	// reach. Empty means none are configured or none has reported. It answers a
	// question the timeline cannot: a tool call that never happened because its
	// server failed to start reads, from the timeline alone, as the agent choosing
	// not to use it.
	MCPServers []ConversationMCPServerPayload `json:"mcpServers,omitempty"`
	// Capabilities names what this session's provider can do, so a client gates a
	// control before drawing it. Sorted, and only the abilities the provider
	// actually has are listed.
	//
	// An open list rather than a fixed set of booleans: drivers gain abilities, and
	// a client that checks for membership keeps working against a daemon that knows
	// about more of them than it does. Absent until a controller is live, because an
	// unstarted session's abilities are not yet known — and a client must treat
	// absent as "do not offer yet" rather than as "cannot".
	Capabilities []string `json:"capabilities,omitempty"`
}

// ConversationBranchMaterializationResponse describes the fidelity of the
// active branch's provider context without exposing provider-owned identifiers.
type ConversationBranchMaterializationResponse struct {
	Strategy        string `json:"strategy" enum:"native,approximate_context"`
	ReplayTruncated bool   `json:"replayTruncated"`
}

// ConversationBranchPointResponse describes sibling continuations at one prompt.
type ConversationBranchPointResponse struct {
	TurnID           string `json:"turnId"`
	Position         int    `json:"position"`
	Total            int    `json:"total"`
	PreviousBranchID string `json:"previousBranchId,omitempty"`
	NextBranchID     string `json:"nextBranchId,omitempty"`
}

// ConversationModelReroutePayload is the provider answering with a model other than
// the one that was asked for.
type ConversationModelReroutePayload struct {
	FromModel string `json:"fromModel,omitempty"`
	ToModel   string `json:"toModel"`
	// Reason is the provider's own word for why, carried verbatim rather than
	// translated: AO cannot improve on the provider's account of its own policy.
	Reason string `json:"reason,omitempty"`
	// ProviderTurnID is the turn it happened on, so a client can point at the
	// exchange rather than only at the conversation.
	ProviderTurnID string `json:"providerTurnId,omitempty"`
	At             string `json:"at"`
}

// ConversationAccountPayload is what the provider says about the account behind a
// conversation.
type ConversationAccountPayload struct {
	AuthMode  string `json:"authMode,omitempty"`
	PlanLabel string `json:"planLabel,omitempty"`
	// ReauthRequiredAt is when the provider last asked for credentials the daemon
	// does not hold. Present means the session has stopped working for a reason no
	// retry will fix and the user has to sign in again.
	ReauthRequiredAt *string `json:"reauthRequiredAt,omitempty"`
	ReauthReason     string  `json:"reauthReason,omitempty"`
}

// ConversationThreadStatePayload is the provider's lifecycle view of the thread.
type ConversationThreadStatePayload struct {
	Status string `json:"status,omitempty" enum:"active,idle,not_loaded,system_error,closed"`
	// WaitingOn are the provider's active flags. A thread can be active AND blocked
	// on a person, and those are different states.
	WaitingOn []string `json:"waitingOn,omitempty"`
	// ArchivedAt is present while the provider considers the thread archived.
	// Archiving is reversible, so this returns to absent on unarchive.
	ArchivedAt *string `json:"archivedAt,omitempty"`
	// ClosedAt is when the provider dropped the thread. Recorded rather than acted
	// on: the daemon has never observed this, so tearing a controller down on the
	// strength of it would be a guess.
	ClosedAt *string `json:"closedAt,omitempty"`
}

// ConversationMCPServerPayload is one tool server's startup state.
type ConversationMCPServerPayload struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	// Error is the provider's failure text; FailureReason is its classification,
	// which is actionable in a way a message is not.
	Error         string `json:"error,omitempty"`
	FailureReason string `json:"failureReason,omitempty"`
}

// ReloadConversationMCPServersResponse reports the tool servers after a reload.
//
// The list is what the provider reported when asked. Its own startup notifications
// remain the authoritative account and land on the conversation regardless, so a
// client that polls the snapshot will converge on the same answer.
type ReloadConversationMCPServersResponse struct {
	Servers []ConversationMCPServerPayload `json:"servers"`
}

// ConversationUsagePayload is the conversation's token position.
//
// Current state on the snapshot rather than timeline entries: the provider reports
// this after every tool call, and one row per report is what buried the
// conversation before.
type ConversationUsagePayload struct {
	// ContextUsed and ContextWindow are what let a client draw a meter instead of
	// printing a bare number. ContextWindow is 0 when the provider would not state
	// one, and a client must then show the tokens without a fullness claim.
	ContextUsed   int64 `json:"contextUsed"`
	ContextWindow int64 `json:"contextWindow"`
	// The conversation's cumulative spend, which is a different question from
	// fullness: it grows without bound while context rises and falls.
	InputTokens  int64    `json:"inputTokens"`
	OutputTokens int64    `json:"outputTokens"`
	CachedTokens int64    `json:"cachedTokens"`
	TotalTokens  int64    `json:"totalTokens"`
	Cost         *float64 `json:"cost,omitempty"`
	Currency     string   `json:"currency,omitempty"`
}

// ConversationRateLimitsPayload is the account's quota position, which is why a
// turn can fail for reasons that have nothing to do with the request.
type ConversationRateLimitsPayload struct {
	// Percentages in 0..100. Negative means the provider did not report that
	// window, which is not the same as reporting it empty.
	PrimaryUsedPercent   float64 `json:"primaryUsedPercent"`
	SecondaryUsedPercent float64 `json:"secondaryUsedPercent"`
	// Seconds remaining, not the absolute reset instant: a duration cannot read as
	// already-refilled once the snapshot is a few minutes old.
	PrimaryResetsInSeconds   int64  `json:"primaryResetsInSeconds,omitempty"`
	SecondaryResetsInSeconds int64  `json:"secondaryResetsInSeconds,omitempty"`
	PlanLabel                string `json:"planLabel,omitempty"`
	// Title is the name the provider currently gives this thread. Empty means it has
	// none, which is the normal state until something names it.
	Title string `json:"title,omitempty"`
}

// ConversationRequestIDParam is the provider's approval request id. Resolving
// matches on it, so a card left on screen cannot answer a newer request.
type ConversationRequestIDParam struct {
	RequestID string `path:"requestId" description:"Provider approval request identifier. Zero is a legitimate value."`
}

// ConversationConfigIDParam names one provider-advertised session option.
type ConversationConfigIDParam struct {
	ConfigID string `path:"configId" description:"Provider session configuration option identifier."`
}

// ConversationTurnIDParam names one turn in a session's conversation.
type ConversationTurnIDParam struct {
	TurnID string `path:"turnId" description:"AO conversation turn identifier, from the snapshot's turns array."`
}

// ConversationBranchIDParam names one durable provider-thread branch.
type ConversationBranchIDParam struct {
	BranchID string `path:"branchId" description:"Conversation branch identifier, from a snapshot branch navigation point."`
}

// RollbackConversationResponse reports what an undo discarded.
type RollbackConversationResponse struct {
	// TurnsDiscarded counts the turns the agent no longer remembers, including the
	// one the caller named. A client can say how much was taken back instead of
	// leaving the user to notice the timeline is shorter.
	TurnsDiscarded int `json:"turnsDiscarded"`
}

// SetConversationTitleRequest names the provider's thread.
type SetConversationTitleRequest struct {
	Title string `json:"title"`
}

// SetConversationTitleResponse echoes the normalized title.
//
// Accepted rather than applied: the provider confirms the name and then reports it
// back on its own event, and that report is what updates AO's rows. So this is the
// title AO asked for, which is not yet proof the session label has moved.
type SetConversationTitleResponse struct {
	Title string `json:"title"`
}

/* ---- settings ---------------------------------------------------------- */

// SettingsResponse is the daemon-owned preference set.
type SettingsResponse struct {
	// DefaultSessionMode applies to sessions created from now on. Changing it
	// never alters an existing session; only an explicit interface transition can.
	DefaultSessionMode string `json:"defaultSessionMode" enum:"chat,tui"`
	// ChatHarnesses are the agents that can run in chat mode today. Empty means
	// chat cannot be used yet, which a client should say plainly.
	ChatHarnesses []string `json:"chatHarnesses"`
	// Client is the deployment's client identity (AO_CLIENT); empty when unset.
	Client string `json:"client"`
	// LocalEnabled reports whether the local offering is available.
	LocalEnabled bool `json:"localEnabled"`
	// CloudOffering is the user's persisted cloud toggle (Settings, Developer
	// Mode). Distinct from CloudEnabled, which is the effective gate.
	CloudOffering bool `json:"cloudOffering"`
	// CloudEnabled reports whether the cloud offering is effectively available:
	// the user's toggle (or the env override) plus a configured control plane.
	CloudEnabled bool `json:"cloudEnabled"`
	// CloudControlPlaneURL is the cloud control plane base URL; empty when no
	// control plane is configured.
	CloudControlPlaneURL string `json:"cloudControlPlaneUrl"`
}

// AgentInstallerCatalogResponse is the body of GET /api/v1/agents/installers.
type AgentInstallerCatalogResponse struct {
	Agents []systeminstall.AgentPlan `json:"agents"`
}

// UpdateSessionInterfaceRequest changes the default interface for new sessions.
type UpdateSessionInterfaceRequest struct {
	DefaultSessionMode string `json:"defaultSessionMode" enum:"chat,tui"`
}

// UpdateCloudOfferingRequest flips the user's cloud toggle.
type UpdateCloudOfferingRequest struct {
	// Enabled turns the cloud offering on or off for this machine's user.
	Enabled *bool `json:"enabled"`
}

// capabilityNames lists the abilities a provider has, sorted so a client sees a
// stable list rather than Go's map order. Only true entries are named: a
// capability the driver reports as false is one it cannot do, which is the same
// answer as not naming it, and listing both states would invite a client to read
// presence rather than value.
func capabilityNames(caps ports.ChatCapabilities) []string {
	if len(caps) == 0 {
		return nil
	}
	names := make([]string, 0, len(caps))
	for name, has := range caps {
		if has {
			names = append(names, string(name))
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return names
}

// TriggerReviewRequest is the optional body of the review trigger route. An
// empty harness keeps the project's configured reviewer; setting one overrides
// it for this pass only, without editing project config, so one session's choice
// cannot change what another session in the project runs.
type TriggerReviewRequest struct {
	Harness     domain.ReviewerHarness `json:"harness,omitempty" enum:"claude-code,codex,copilot,cursor,kilocode,opencode,kiro,pi,qwen,agy,continue,goose,vibe,devin,droid,kimi,kimchi,muse,amp,aider,grok,crush,auggie,cline,autohand"`
	AgentConfig domain.AgentConfig     `json:"agentConfig,omitempty"`
}

// ResolveReviewCommentRequest is the body of POST /api/v1/sessions/{sessionId}/reviews/comments/resolve.
type ResolveReviewCommentRequest struct {
	PullRequestURL string `json:"pullRequestUrl,omitempty" description:"Tracked pull request URL. Required when the session has multiple PRs."`
	CommentURL     string `json:"commentUrl" description:"Provider URL of the unresolved review comment to resolve."`
}

// ResolveReviewCommentResponse is returned after AO resolves a provider review thread.
type ResolveReviewCommentResponse struct {
	OK bool `json:"ok"`
}

// RequestRereviewRequest is the body of POST /api/v1/sessions/{sessionId}/reviews/rerequest.
type RequestRereviewRequest struct {
	PullRequestURL string `json:"pullRequestUrl,omitempty" description:"Tracked pull request URL. Required when the session has multiple PRs."`
	ReviewerID     string `json:"reviewerId" description:"Provider login of the reviewer to ask for another review."`
}

// RequestRereviewResponse is returned after AO asks the SCM provider for another review.
type RequestRereviewResponse struct {
	OK bool `json:"ok"`
}

// MobileDeviceResponse is one row of the desktop's mobile-device roster: the
// stored registration plus whether that phone is running the app right now.
type MobileDeviceResponse struct {
	InstallID            string    `json:"installId"`
	Token                string    `json:"token,omitempty"`
	Platform             string    `json:"platform,omitempty" enum:"ios,android"`
	DeviceName           string    `json:"deviceName,omitempty"`
	Muted                bool      `json:"muted"`
	Live                 bool      `json:"live" description:"True when the phone's app is open and polling."`
	NotificationsEnabled bool      `json:"notificationsEnabled" description:"True when this device has a push token registered."`
	CreatedAt            time.Time `json:"createdAt"`
	LastSeenAt           time.Time `json:"lastSeenAt"`
}

// MobileDevicesResponse is the { devices } envelope for the roster.
type MobileDevicesResponse struct {
	Devices []MobileDeviceResponse `json:"devices"`
}

// MuteDeviceRequest is the body of PATCH /api/v1/mobile/devices/{installId}.
type MuteDeviceRequest struct {
	Muted bool `json:"muted" description:"True to stop sending push notifications to this device."`
}

// InstallIDParam is the {installId} path parameter for mobile-device roster
// routes.
type InstallIDParam struct {
	InstallID string `path:"installId" description:"The device's stable install id."`
}
