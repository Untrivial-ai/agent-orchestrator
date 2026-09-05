package userconfig

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// Manager is the controller-facing contract for the /api/v1/user-config surface.
type Manager interface {
	// Get returns the user-scope agent config. A missing row (no user has set a
	// config yet) returns a zero AgentConfig, never an error — callers resolve to
	// today's behavior.
	Get(ctx context.Context) (domain.AgentConfig, error)

	// Set replaces the user-scope config wholesale, returning the stored value.
	Set(ctx context.Context, in SetUserConfigInput) (domain.AgentConfig, error)
}

// Service implements user-scope config get/set use-cases for controllers.
type Service struct {
	store Store
}

var _ Manager = (*Service)(nil)

// Deps captures collaborators for user-scope use-cases.
type Deps struct {
	Store Store
}

// New returns a user-scope service backed by the given durable store.
func New(store Store) *Service {
	return NewWithDeps(Deps{Store: store})
}

// NewWithDeps returns a user-scope service with explicit dependencies.
func NewWithDeps(d Deps) *Service {
	return &Service{store: d.Store}
}

// Get returns the stored user-scope config. Absent row → zero config, no error.
// The store's "found" bool is intentionally discarded here: callers resolve to a
// zero config whether the row is absent or explicitly cleared, so the distinction
// (kept on the store for the Phase-2 merge layer) is not part of the read surface.
func (s *Service) Get(ctx context.Context) (domain.AgentConfig, error) {
	cfg, _, err := s.store.GetUserConfig(ctx)
	if err != nil {
		return domain.AgentConfig{}, err
	}
	return cfg, nil
}

// Set validates the incoming config and replaces the singleton row wholesale.
func (s *Service) Set(ctx context.Context, in SetUserConfigInput) (domain.AgentConfig, error) {
	if err := in.AgentConfig.Validate(); err != nil {
		return domain.AgentConfig{}, apierr.Invalid("INVALID_USER_CONFIG", err.Error(), nil)
	}
	if err := s.store.UpsertUserConfig(ctx, in.AgentConfig); err != nil {
		return domain.AgentConfig{}, err
	}
	return in.AgentConfig, nil
}
