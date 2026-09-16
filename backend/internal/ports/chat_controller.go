package ports

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// ChatHistoryMode makes required replay and deferred import mutually exclusive.
type ChatHistoryMode uint8

// Controller replay modes.
const (
	ChatHistoryImport ChatHistoryMode = iota
	ChatHistoryRequired
	// ChatHistoryDeferred is used when agent switching owns later projection.
	ChatHistoryDeferred
)

// ChatControllerStart is the resolved launch contract shared by the coordinator and Chat service.
type ChatControllerStart struct {
	SessionID             domain.SessionID
	ProjectID             domain.ProjectID
	Kind                  domain.SessionKind
	Harness               domain.AgentHarness
	DataDir               string
	WorkspacePath         string
	Env                   map[string]string
	Model                 string
	Effort                string
	Permissions           PermissionMode
	SystemPrompt          string
	AdditionalDirectories []string
	MCPServers            []ChatMCPServerConfig
	// ExpectedControllerOwner is the durable controller identity observed before
	// this launch. PrepareControllerEnv uses it as a compare-and-swap fence.
	ExpectedControllerOwner domain.SessionControllerOwner
	// PrepareControllerEnv rotates launch-only credentials inside the per-session
	// controller gate. The returned environment is never retained in startConfigs.
	PrepareControllerEnv func(context.Context, domain.SessionControllerOwner) (map[string]string, error)
	// ProviderConversationID resumes an existing provider conversation when set.
	ProviderConversationID string
	// ProviderScopeID reserves the opaque-id namespace for a provider boundary
	// that ControllerReady will commit. Empty derives the namespace from the
	// active branch, which is the ordinary initial-start and resume path.
	ProviderScopeID string
	ProviderHandoff *domain.ChatProviderHandoff
	// ControllerGeneration is supplied by a durable replacement saga that must
	// fence the target before starting it. Ordinary starts leave it empty.
	ControllerGeneration string
	// HistoryMode chooses ordinary replay, required replay, or deferred import.
	HistoryMode ChatHistoryMode
	// HistoryPolicy carries explicit, attempt-scoped consent to ignore only
	// legacy/untrusted hook text during a TUI-to-Chat replay. Trusted checkpoints
	// and AO high-water facts remain mandatory.
	HistoryPolicy domain.SessionInterfaceTransitionHistoryPolicy
	// ControllerReady commits the controller's durable generation before event
	// consumption starts. A controller that exits immediately must report after
	// the launch has been marked live, so its exited signal cannot be overwritten
	// by a later launch-completion write.
	ControllerReady func(ChatControllerStarted) (ChatControllerCommit, error)
}

// ChatControllerCommit returns the ownership published by the launch callback.
type ChatControllerCommit struct {
	Conversation    domain.ConversationRecord
	ControllerOwner domain.SessionControllerOwner
}

// ChatControllerStarted supplies the provider identity and pending atomic history commit.
type ChatControllerStarted struct {
	// LiveReconnect is true only when the driver attached to the same running
	// provider process. A native-history resume in a new process is a spawn.
	LiveReconnect          bool
	ProviderConversationID string
	ControllerGeneration   string
	Conversation           domain.ConversationRecord
	// ProviderBoundary is non-nil when this launch owns a provider namespace
	// that is not active yet. ControllerReady must commit it atomically with the
	// session's provider handle and controller generation.
	ProviderBoundary *domain.ConversationBranch
	// CommitProviderHistory projects a reconciled native replay inside the
	// provider-boundary lifecycle transaction. It must never be invoked outside
	// that transaction: the pending branch and generation do not exist yet.
	CommitProviderHistory func(context.Context) error
}
