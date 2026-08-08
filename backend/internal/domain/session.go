package domain

import "time"

// These ID types are distinct string types so they can't be swapped at a call
// site by accident.
type (
	// SessionID identifies a session.
	SessionID string
	// ProjectID identifies a project.
	ProjectID string
	// IssueID identifies a tracker issue.
	IssueID string
)

// SessionKind distinguishes a worker session from an orchestrator session.
type SessionKind string

// Session kinds.
const (
	KindWorker       SessionKind = "worker"
	KindOrchestrator SessionKind = "orchestrator"
)

// SessionMetadata is the typed, off-status metadata for a session: operational
// handles and seed inputs used by Session Manager and reaper.
type SessionMetadata struct {
	Branch            string `json:"branch,omitempty"`
	WorkspacePath     string `json:"workspacePath,omitempty"`
	WorkspaceRepoPath string `json:"workspaceRepoPath,omitempty"`
	DiffBaseSHA       string `json:"diffBaseSha,omitempty"`
	DiffBaseRef       string `json:"diffBaseRef,omitempty"`
	RuntimeHandleID   string `json:"runtimeHandleId,omitempty"`
	RuntimeLaunchID   string `json:"runtimeLaunchId,omitempty"`
	AgentSessionID    string `json:"agentSessionId,omitempty"`
	Prompt            string `json:"prompt,omitempty"`
	// ProviderConversationID is the opaque handle a Chat driver needs to resume
	// this session's provider conversation after a restart (a Codex thread id
	// today). Normally empty for TUI sessions. It remains a distinct field from
	// AgentSessionID because most harnesses do not prove those protocol identities
	// interchangeable; the interface-transition coordinator copies one value into
	// both only after the adapter explicitly declares that equivalence.
	ProviderConversationID string `json:"providerConversationId,omitempty"`
	// ControllerGeneration is rotated each time a Chat controller is started for
	// this session. Events carrying an older generation are rejected, so a
	// controller that is dying cannot mutate the session that replaced it. Not
	// the same fence as RuntimeLaunchID, which covers terminal runtimes.
	ControllerGeneration string `json:"controllerGeneration,omitempty"`
	// PreviewURL is the browser preview target the desktop app opens for this
	// session. Set via `ao preview` (POST /sessions/{id}/preview); persisted so
	// it survives a daemon restart. Empty means no preview has been requested.
	PreviewURL string `json:"previewUrl,omitempty"`
	// PreviewRevision is a monotonic counter bumped on every `ao preview` call,
	// even when PreviewURL is unchanged. The desktop browser panel keys
	// navigation on it so a repeated `ao preview <same-url>` still refreshes.
	PreviewRevision int64 `json:"previewRevision,omitempty"`
}

// SessionRecord is the persistence shape. It intentionally stores only durable
// facts: identity, agent harness, activity_state, is_terminated, and operational
// metadata. The user-facing Status is derived from these facts plus PR facts.
type SessionRecord struct {
	ID          SessionID    `json:"id"`
	ProjectID   ProjectID    `json:"projectId"`
	IssueID     IssueID      `json:"issueId,omitempty"`
	Kind        SessionKind  `json:"kind"`
	Harness     AgentHarness `json:"harness,omitempty"`
	DisplayName string       `json:"displayName,omitempty"`
	// Model is the model the session resolved to at spawn (e.g. claude-opus-4-5).
	// Set when `ao spawn --model` (or the spawn API's model field) overrides the
	// project config, or when the project's resolved model is pinned at spawn so
	// `ao session get` can report what the session actually launched with. Empty
	// means the session used the agent's default model.
	Model string `json:"model,omitempty"`
	// ReviewerHarness is this session's preferred reviewer. Empty delegates to
	// the project configuration.
	ReviewerHarness ReviewerHarness `json:"reviewerHarness,omitempty" enum:"claude-code,codex,copilot,cursor,kilocode,opencode,kiro,pi,qwen,agy,continue,goose,vibe,devin,droid,kimi,muse,amp,aider,grok,crush,auggie,cline,autohand"`
	// Mode is the session's currently committed conversation controller. Every
	// send, restore, kill, and reaper decision dispatches from it. Only the
	// durable interface-transition coordinator may change it; the daemon default
	// never changes an existing session. Rows written before Chat mode existed
	// read back as SessionModeTUI.
	Mode     SessionMode `json:"mode" enum:"chat,tui"`
	Activity Activity    `json:"activity"`
	// FirstSignalAt is when the FIRST agent hook callback arrived for the
	// current spawn/restore: raw signal receipt, independent of the derived
	// activity state. Zero means no hook has ever reported, which deriveStatus
	// surfaces as StatusNoSignal after a grace period. Internal fact, not part
	// of the API read model.
	FirstSignalAt time.Time `json:"-"`
	IsTerminated  bool      `json:"isTerminated"`
	// TerminateOnPRMerge is a user-controlled lifecycle policy. When enabled,
	// completing the session's PR set through a merge tears down the session.
	TerminateOnPRMerge bool            `json:"terminateOnPrMerge"`
	Metadata           SessionMetadata `json:"-"`
	// CleanupGeneration is a monotonic counter bumped each time the session is
	// un-terminated (spawn/restore). The terminal-resource reconciler stamps its
	// durable cleanup facts with the generation they were written for so a
	// finalize started under an earlier terminal episode cannot satisfy a later
	// one. Internal fact, not part of the API read model.
	CleanupGeneration int64      `json:"-"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
	IsPinned          bool       `json:"isPinned"`
	PinnedAt          *time.Time `json:"pinnedAt,omitempty"`
}

// Session is the read-model returned across the API boundary: a SessionRecord
// plus derived display facts. Neither Status nor SCMStatus is persisted.
type Session struct {
	SessionRecord
	Status           SessionStatus `json:"status" enum:"working,pr_open,draft,ci_failed,review_pending,changes_requested,approved,mergeable,merged,needs_input,exited,idle,terminated,no_signal"`
	SCMStatus        SessionStatus `json:"scmStatus,omitempty" enum:"pr_open,draft,ci_failed,review_pending,changes_requested,approved,mergeable,merged"`
	TerminalHandleID string        `json:"terminalHandleId,omitempty"`
	// PRs are the session's attributed pull requests (one session can own many).
	// They feed status derivation and are surfaced on the API read model. Not
	// serialized here: the HTTP boundary maps them to the curated wire shape.
	PRs []PRFacts `json:"-"`
}
