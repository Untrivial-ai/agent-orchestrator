package ports

import (
	"errors"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// ErrSessionNotFound reports an observation for an unknown session id.
var ErrSessionNotFound = errors.New("session not found")

// SpawnConfig is the request to start a new session: which project/issue, which
// agent harness, and the branch/prompt the agent launches with.
type SpawnConfig struct {
	ProjectID domain.ProjectID
	IssueID   domain.IssueID
	// TrackerProvider is the issue-tracker provider hint from the CLI's
	// --tracker-provider flag (defaults to "github"). It is used as a fallback
	// when the project's SCM origin cannot be classified by the configured
	// SCM provider. When the SCM origin resolves successfully, the resolved
	// provider takes precedence over this hint.
	TrackerProvider domain.TrackerProvider
	// IssueContext is optional pre-fetched tracker context for the task prompt.
	// Standing rules stay in SystemPrompt; issue facts belong to the user task.
	IssueContext string
	Kind         domain.SessionKind
	Harness      domain.AgentHarness
	Branch       string
	Prompt       string
	// AgentConfig overrides the resolved project/role agent config for this
	// single spawn. Empty fields keep the project defaults.
	AgentConfig AgentConfig

	// RequestedMode is the caller's explicit session mode, or empty to let the
	// daemon resolve its default. It is validated and persisted before any
	// controller launches. A later explicit interface transition may replace that
	// controller while preserving the AO session. An unsupported explicit request
	// fails the spawn rather than falling back to the other mode.
	RequestedMode domain.SessionMode

	// DisplayName is the user-facing sidebar label. Empty falls back to the
	// session id in the read model (e.g. orchestrator sessions).
	DisplayName string
	// Attachments are files pasted or dropped into the task brief. They are
	// written into the session worktree and referenced by path in the prompt so
	// the agent can read them (CLI agents receive the prompt as text and cannot
	// consume inline binary data). Any file type is accepted except for
	// explicitly blocked types (e.g., SVG for security reasons).
	Attachments []SpawnAttachment
}

// SpawnAttachment is a single file attached to a spawn request. Data holds the
// already-decoded bytes; the manager derives the on-disk filename from the
// MIME type.
type SpawnAttachment struct {
	// Ext is the file extension (including the leading dot, e.g. ".png")
	// inferred from the attachment's declared MIME type, or ".bin" for unknown types.
	Ext  string
	Data []byte
}
