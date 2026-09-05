package userconfig

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Store is the durable user-scope persistence surface required by Service.
// *sqlite.Store satisfies it structurally (GetUserConfig / UpsertUserConfig).
type Store interface {
	GetUserConfig(ctx context.Context) (domain.AgentConfig, bool, error)
	UpsertUserConfig(ctx context.Context, cfg domain.AgentConfig) error
}
