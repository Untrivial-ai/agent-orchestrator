package ports

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

// ImportIdentity identifies a provider conversation within one local state root.
type ImportIdentity struct {
	Provider        domain.AgentHarness
	NativeSessionID string
	ConfigDir       string
}
