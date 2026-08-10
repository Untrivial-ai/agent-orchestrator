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
	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
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
}

// SessionView is the session wire shape: the domain read model plus the
// display-safe branch name and the session's attributed pull requests in the
// curated SessionPRFacts shape. One session can own many PRs (e.g. a stack), so
// prs is a list. The embedded domain.Session.Metadata and domain.Session.PRs
// fields are json:"-"; these curated fields are what serialize.
type SessionView struct {
	domain.Session
	Branch string `json:"branch,omitempty"`
	// PreviewURL is the browser preview target the desktop app opens for this
	// session, set via POST /sessions/{sessionId}/preview. Empty (omitted) when
	// no preview has been requested. Pulled from the json:"-" domain Metadata.
	PreviewURL string `json:"previewUrl,omitempty"`
	// PreviewRevision bumps on every `ao preview` call (even when previewUrl is
	// unchanged) so the desktop browser panel can re-navigate / refresh on a
	// repeated preview of the same target. Pulled from the json:"-" domain
	// Metadata.
	PreviewRevision int64            `json:"previewRevision,omitempty"`
	PRs             []SessionPRFacts `json:"prs"`
}

// ListSessionsResponse is the body of GET /api/v1/sessions.
type ListSessionsResponse struct {
	Sessions []SessionView `json:"sessions"`
}

// SpawnSessionRequest is the body of POST /api/v1/sessions.
type SpawnSessionRequest struct {
	ProjectID domain.ProjectID    `json:"projectId"`
	IssueID   domain.IssueID      `json:"issueId,omitempty"`
	Kind      domain.SessionKind  `json:"kind,omitempty" enum:"worker,orchestrator"`
	Harness   domain.AgentHarness `json:"harness,omitempty" enum:"claude-code,codex,aider,opencode,grok,droid,amp,agy,crush,cursor,qwen,copilot,goose,auggie,continue,devin,cline,kimi,muse,kiro,kilocode,vibe,pi,kimchi,prime-agent,autohand"`
	Branch    string              `json:"branch,omitempty"`
	Prompt    string              `json:"prompt,omitempty" maxLength:"4096"`
	// Model is a per-session model override scoped to this spawn only. It wins
	// over the project's resolved agent config model and has no effect on the
	// project config.
	Model string `json:"model,omitempty" maxLength:"256"`
	// Mode picks the conversation controller: chat talks to the agent over a
	// structured connection, tui opens the agent's native terminal interface.
	// Omitted resolves to the daemon default (tui), which is why an upgrade
	// changes nothing. Compatible sessions may later switch through the durable
	// interface-transition endpoint; the default never mutates existing sessions
	// automatically. An unsupported explicit request fails rather than quietly
	// producing the other kind of session.
	Mode domain.SessionMode `json:"mode,omitempty" enum:"chat,tui"`
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
	Note           string              `json:"note,omitempty" maxLength:"4096" description:"Optional user guidance included in the bounded handoff context."`
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
	ErrorCode               domain.AgentSwitchErrorCode              `json:"errorCode,omitempty" enum:"daemon_restart_pre_stop,daemon_restart_post_stop,daemon_restart_unrecoverable_target,daemon_restart_before_delivery,delivery_unconfirmed,source_session_terminated,source_stop_unconfirmed,target_binary_missing,target_agent_unauthorized,target_start_unconfirmed,request_cancelled,source_blocked,failed_pre_stop,failed_post_stop,target_ready_failed,delivery_failed,switch_failed"`
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
	Content          string                          `json:"content"`
	ContentTruncated bool                            `json:"contentTruncated"`
	Diff             string                          `json:"diff"`
	DiffTruncated    bool                            `json:"diffTruncated"`
	CompareBaseSHA   string                          `json:"compareBaseSha,omitempty"`
	CompareBaseRef   string                          `json:"compareBaseRef,omitempty"`
	CompareMode      sessionsvc.WorkspaceCompareMode `json:"compareMode,omitempty" enum:"base,head_fallback"`
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
	Harness domain.ReviewerHarness `json:"harness,omitempty" enum:"claude-code,codex,copilot,cursor,kilocode,opencode,kiro,pi,qwen,agy,continue,goose,vibe,devin,droid,kimi,kimchi,muse,amp,aider,grok,crush,auggie,cline,autohand"`
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

// RestoreSessionResponse is the body of POST /api/v1/sessions/{sessionId}/restore.
type RestoreSessionResponse struct {
	OK          bool                       `json:"ok"`
	SessionID   domain.SessionID           `json:"sessionId"`
	RestoreMode sessionsvc.RestoreModeView `json:"restoreMode" enum:"native,saved_prompt,fresh"`
	Session     SessionView                `json:"session"`
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
	ID          string                                  `json:"id"`
	SessionID   domain.SessionID                        `json:"sessionId"`
	SourceMode  domain.SessionMode                      `json:"sourceMode" enum:"chat,tui"`
	TargetMode  domain.SessionMode                      `json:"targetMode" enum:"chat,tui"`
	Policy      domain.SessionInterfaceTransitionPolicy `json:"policy" enum:"drain,interrupt"`
	Phase       domain.SessionInterfaceTransitionPhase  `json:"phase" enum:"requested,preflighting,draining,source_stopping,source_stopped,target_starting,activating,completed,failed,cancelled,recovery_required"`
	ErrorCode   string                                  `json:"errorCode,omitempty"`
	ErrorDetail string                                  `json:"errorDetail,omitempty"`
	CreatedAt   time.Time                               `json:"createdAt"`
	UpdatedAt   time.Time                               `json:"updatedAt"`
	CompletedAt *time.Time                              `json:"completedAt,omitempty"`
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
	OK      bool                    `json:"ok"`
	Cleaned []domain.SessionID      `json:"cleaned"`
	Skipped []CleanupSkippedSession `json:"skipped"`
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
	Brief     string              `json:"brief" maxLength:"4096"`
	Agent     domain.AgentHarness `json:"agent,omitempty" enum:"claude-code,codex,aider,opencode,grok,droid,amp,agy,crush,cursor,qwen,copilot,goose,auggie,continue,devin,cline,kimi,muse,kiro,kilocode,vibe,pi,kimchi,prime-agent,autohand,fake"`
	Model     string              `json:"model,omitempty" maxLength:"256"`
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
	Provider         string                       `json:"provider" enum:"github"`
	Repo             string                       `json:"repo"`
	Author           string                       `json:"author"`
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

// SessionPRUnresolvedReviewer groups unresolved human comments by reviewer.
type SessionPRUnresolvedReviewer struct {
	ReviewerID string                       `json:"reviewerId"`
	Count      int                          `json:"count"`
	Links      []SessionPRReviewCommentLink `json:"links"`
	ReviewURL  string                       `json:"reviewUrl,omitempty"`
	IsBot      bool                         `json:"isBot,omitempty"`
}

// SessionPRReviewCommentLink points to one unresolved review comment.
type SessionPRReviewCommentLink struct {
	URL              string `json:"url,omitempty"`
	File             string `json:"file,omitempty"`
	Line             int    `json:"line,omitempty"`
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
	return SessionPRCISummary{State: in.State, FailingChecks: checks}
}

func newSessionPRReviewSummary(in sessionsvc.PRReviewSummary) SessionPRReviewSummary {
	reviewers := make([]SessionPRUnresolvedReviewer, 0, len(in.UnresolvedBy))
	for _, reviewer := range in.UnresolvedBy {
		links := make([]SessionPRReviewCommentLink, 0, len(reviewer.Links))
		for _, link := range reviewer.Links {
			links = append(links, SessionPRReviewCommentLink{URL: link.URL, File: link.File, Line: link.Line, AutoInjectReview: link.AutoInjectReview})
		}
		reviewers = append(reviewers, SessionPRUnresolvedReviewer{ReviewerID: reviewer.ReviewerID, Count: reviewer.Count, Links: links, ReviewURL: reviewer.ReviewURL, IsBot: reviewer.IsBot})
	}
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
	return SessionPRReviewSummary{Decision: in.Decision, HasUnresolvedHumanComments: in.HasUnresolvedHumanComments, UnresolvedBy: reviewers, Reviews: entries}
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

// CompactSessionUsageResponse is one session card's token-only usage summary.
type CompactSessionUsageResponse struct {
	SessionID   domain.SessionID `json:"sessionId"`
	TotalTokens int64            `json:"totalTokens" minimum:"0"`
	Incomplete  bool             `json:"incomplete"`
}

// ListCompactSessionUsageResponse is the batch dashboard usage response.
type ListCompactSessionUsageResponse struct {
	Sessions []CompactSessionUsageResponse `json:"sessions"`
}

// UsageTotalsResponse is the normalized telemetry aggregate for one scope.
type UsageTotalsResponse struct {
	InputTokens         *int64 `json:"inputTokens"`
	UncachedInputTokens *int64 `json:"uncachedInputTokens"`
	CacheReadTokens     *int64 `json:"cacheReadTokens"`
	CacheWriteTokens    *int64 `json:"cacheWriteTokens"`
	OutputTokens        *int64 `json:"outputTokens"`
	ReasoningTokens     *int64 `json:"reasoningTokens"`
}

// UsageModelResponse is telemetry grouped by exact model id.
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

// MobileStatusResponse is the body of the Connect Mobile status/enable/disable/
// regenerate endpoints. Password is populated only transiently, on enable and
// regenerate responses (empty otherwise) — it is never persisted in plaintext.
type MobileStatusResponse struct {
	Enabled  bool   `json:"enabled"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Password string `json:"password"`
	Warning  string `json:"warning"`
}

// PushDeviceTokenParam is the {token} path parameter for push-device routes.
type PushDeviceTokenParam struct {
	Token string `path:"token" description:"Expo push token (URL-encoded) identifying the device."`
}

// RegisterPushDeviceRequest is the body of POST /api/v1/push/devices. The phone
// sends its Expo push token plus a bit of descriptive metadata; the daemon keys
// the registry on the token and re-registering is an idempotent upsert.
type RegisterPushDeviceRequest struct {
	Token      string `json:"token" description:"Expo push token, e.g. ExponentPushToken[...]."`
	Platform   string `json:"platform,omitempty" enum:"ios,android" description:"Device platform."`
	DeviceName string `json:"deviceName,omitempty" description:"Human-friendly device label."`
}

// PushDeviceResponse is the stored view of a registered push device.
type PushDeviceResponse struct {
	Token      string    `json:"token"`
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
	State domain.TurnState `json:"state,omitempty" enum:"queued,running,completed,interrupted,failed"`
	// Duplicate is true when this client message id was already delivered, so a
	// retrying client can stop instead of assuming a new turn began.
	Duplicate bool `json:"duplicate"`
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
	Choices        []ConversationConfigChoiceResponse `json:"choices,omitempty"`
}

// ConversationConfigChoiceResponse is one value in a provider select.
type ConversationConfigChoiceResponse struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Group       string `json:"group,omitempty"`
	GroupName   string `json:"groupName,omitempty"`
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
	ID             string  `json:"id"`
	State          string  `json:"state" enum:"queued,running,completed,interrupted,failed"`
	ProviderTurnID string  `json:"providerTurnId,omitempty"`
	ErrorMessage   string  `json:"errorMessage,omitempty"`
	RequestedAt    string  `json:"requestedAt"`
	StartedAt      *string `json:"startedAt,omitempty"`
	CompletedAt    *string `json:"completedAt,omitempty"`
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
	Kind     string `json:"kind" enum:"message"`
	ID       string `json:"id"`
	TurnID   string `json:"turnId,omitempty"`
	Sequence int64  `json:"sequence"`
	Revision int64  `json:"revision"`
	Role     string `json:"role" enum:"user,assistant"`
	Origin   string `json:"origin" enum:"human,automation,daemon,provider"`
	Text     string `json:"text"`
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
	Status       string `json:"status" enum:"running,completed,failed,cancelled,pending,resolved"`
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
	ConversationID string `json:"conversationId"`
	SessionID      string `json:"sessionId"`
	Harness        string `json:"harness,omitempty"`
	Mode           string `json:"mode" enum:"chat,tui"`
	// Controller is reported separately from history so a client can tell "no
	// messages yet" apart from "the agent is not running".
	Controller     string                         `json:"controller" enum:"connecting,ready,busy,recovering,stopped"`
	LatestSequence int64                          `json:"latestSequence"`
	OldestSequence int64                          `json:"oldestSequence,omitempty"`
	HasMoreBefore  bool                           `json:"hasMoreBefore"`
	Turns          []ConversationTurnResponse     `json:"turns"`
	Messages       []ConversationMessageResponse  `json:"messages"`
	Activities     []ConversationActivityResponse `json:"activities"`
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
}

// UpdateSessionInterfaceRequest changes the default interface for new sessions.
type UpdateSessionInterfaceRequest struct {
	DefaultSessionMode string `json:"defaultSessionMode" enum:"chat,tui"`
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
	Harness domain.ReviewerHarness `json:"harness,omitempty" enum:"claude-code,codex,copilot,cursor,kilocode,opencode,kiro,pi,qwen,agy,continue,goose,vibe,devin,droid,kimi,kimchi,muse,amp,aider,grok,crush,auggie,cline,autohand"`
}
