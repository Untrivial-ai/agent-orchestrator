package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// CreateAgentRoleInput describes a new agent role.
type CreateAgentRoleInput struct {
	Name                   string
	DisplayName            string
	Description            string
	SystemPrompt           string
	DefaultProviderID      domain.ProviderID
	DefaultProviderModelID domain.ProviderModelID
}

// UpdateAgentRoleInput describes fields to patch on an agent role.
// Nil pointers are left unchanged (PATCH merge semantics).
type UpdateAgentRoleInput struct {
	DisplayName            *string
	Description            *string
	SystemPrompt           *string
	DefaultProviderID      *domain.ProviderID
	DefaultProviderModelID *domain.ProviderModelID
}

// CreateAgentRole creates a new agent role.
func (s *Service) CreateAgentRole(ctx context.Context, in CreateAgentRoleInput) (domain.AgentRole, error) {
	if strings.TrimSpace(in.Name) == "" {
		return domain.AgentRole{}, ErrInvalidInput
	}

	pp := pairOrEmpty(in.DefaultProviderID, in.DefaultProviderModelID)
	if pp.partial {
		return domain.AgentRole{}, ErrInvalidInput
	}
	if pp.complete {
		if err := s.validateProviderPair(ctx, in.DefaultProviderID, in.DefaultProviderModelID); err != nil {
			return domain.AgentRole{}, err
		}
	}

	now := s.now()
	role := domain.AgentRole{
		ID:                     s.newID(),
		Name:                   strings.TrimSpace(in.Name),
		DisplayName:            strings.TrimSpace(in.DisplayName),
		Description:            strings.TrimSpace(in.Description),
		SystemPrompt:           in.SystemPrompt,
		DefaultProviderID:      in.DefaultProviderID,
		DefaultProviderModelID: in.DefaultProviderModelID,
		Enabled:                true,
		CreatedAt:              now,
		UpdatedAt:              now,
	}

	if err := s.store.CreateAgentRole(ctx, role); err != nil {
		return domain.AgentRole{}, err
	}
	return role, nil
}

// GetAgentRole retrieves an agent role by ID.
func (s *Service) GetAgentRole(ctx context.Context, id domain.AgentRoleID) (domain.AgentRole, error) {
	role, ok, err := s.store.GetAgentRole(ctx, id)
	if err != nil {
		return domain.AgentRole{}, err
	}
	if !ok {
		return domain.AgentRole{}, ErrNotFound
	}
	return role, nil
}

// ListAgentRoles returns all agent roles.
func (s *Service) ListAgentRoles(ctx context.Context) ([]domain.AgentRole, error) {
	return s.store.ListAgentRoles(ctx)
}

// UpdateAgentRole patches mutable fields of an agent role (PATCH merge semantics).
func (s *Service) UpdateAgentRole(ctx context.Context, id domain.AgentRoleID, in UpdateAgentRoleInput) (domain.AgentRole, error) {
	role, ok, err := s.store.GetAgentRole(ctx, id)
	if err != nil {
		return domain.AgentRole{}, err
	}
	if !ok {
		return domain.AgentRole{}, ErrNotFound
	}

	if in.DisplayName != nil {
		role.DisplayName = strings.TrimSpace(*in.DisplayName)
	}
	if in.Description != nil {
		role.Description = strings.TrimSpace(*in.Description)
	}
	if in.SystemPrompt != nil {
		role.SystemPrompt = *in.SystemPrompt
	}
	if in.DefaultProviderID != nil {
		role.DefaultProviderID = *in.DefaultProviderID
	}
	if in.DefaultProviderModelID != nil {
		role.DefaultProviderModelID = *in.DefaultProviderModelID
	}

	// Validate the merged pair
	pp := pairOrEmpty(role.DefaultProviderID, role.DefaultProviderModelID)
	if pp.partial {
		return domain.AgentRole{}, ErrInvalidInput
	}
	if pp.complete {
		if err := s.validateProviderPair(ctx, role.DefaultProviderID, role.DefaultProviderModelID); err != nil {
			return domain.AgentRole{}, err
		}
	}

	role.UpdatedAt = s.now()
	if err := s.store.UpdateAgentRole(ctx, role); err != nil {
		return domain.AgentRole{}, err
	}
	return role, nil
}

// SetAgentRoleEnabled enables or disables an agent role.
func (s *Service) SetAgentRoleEnabled(ctx context.Context, id domain.AgentRoleID, enabled bool) error {
	_, ok, err := s.store.GetAgentRole(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return s.store.SetAgentRoleEnabled(ctx, id, enabled, s.now())
}

// ---- Provider/Model validation ----

// providerPair describes the configuration state of a ProviderID/ProviderModelID pair.
type providerPair struct {
	empty    bool
	partial  bool
	complete bool
}

func pairOrEmpty(pid domain.ProviderID, mid domain.ProviderModelID) providerPair {
	hasPID := pid != ""
	hasMID := mid != ""
	if hasPID == hasMID {
		if hasPID {
			return providerPair{complete: true}
		}
		return providerPair{empty: true}
	}
	return providerPair{partial: true}
}

// validateProviderPair checks that provider and model exist, model belongs to
// provider, and both are enabled. Uses read-only metadata queries (no secret access).
func (s *Service) validateProviderPair(ctx context.Context, pid domain.ProviderID, mid domain.ProviderModelID) error {
	p, pok, perr := s.store.GetProvider(ctx, pid)
	if perr != nil {
		return fmt.Errorf("get provider: %w", perr)
	}
	if !pok {
		return fmt.Errorf("%w: provider %q not found", ErrNotFound, pid)
	}
	if !p.Enabled {
		return fmt.Errorf("%w: provider %q is disabled", ErrInvalidTransition, pid)
	}

	m, mok, merr := s.store.GetProviderModel(ctx, mid)
	if merr != nil {
		return fmt.Errorf("get provider model: %w", merr)
	}
	if !mok || m.ProviderID != pid {
		return fmt.Errorf("%w: model %q does not belong to provider %q", ErrInvalidInput, mid, pid)
	}
	if !m.Enabled {
		return fmt.Errorf("%w: model %q is disabled", ErrInvalidTransition, mid)
	}
	return nil
}

// ---- Resolution (used by StartRun) ----

// resolveRole resolves the AgentRole for a task.
// Returns (nil, nil) if task has no AgentRoleID.
// Returns error if role is missing/disabled/store-error (fail-first, no silent fallback).
func (s *Service) resolveRole(ctx context.Context, task domain.DevelopmentTask) (*domain.AgentRole, error) {
	if task.AgentRoleID == "" {
		return nil, nil
	}
	role, found, err := s.store.GetAgentRole(ctx, task.AgentRoleID)
	if err != nil {
		return nil, fmt.Errorf("get agent role: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("%w: agent role %q not found", ErrNotFound, task.AgentRoleID)
	}
	if !role.Enabled {
		return nil, fmt.Errorf("%w: agent role %q is disabled", ErrInvalidTransition, task.AgentRoleID)
	}
	return &role, nil
}

// resolveProvider resolves the Provider/Model pair using the three-level chain:
// Task explicit > AgentRole default > System default.
// Returns (providerID, modelID) or error. Partial pairs at any level are errors.
func (s *Service) resolveProvider(ctx context.Context, task domain.DevelopmentTask, role *domain.AgentRole) (domain.ProviderID, domain.ProviderModelID, error) {
	// Priority 1: Task explicit
	taskPair := pairOrEmpty(task.ProviderID, task.ProviderModelID)
	if taskPair.partial {
		return "", "", fmt.Errorf("%w: task provider partial pair", ErrInvalidInput)
	}
	if taskPair.complete {
		if err := s.validateProviderPair(ctx, task.ProviderID, task.ProviderModelID); err != nil {
			return "", "", err
		}
		return task.ProviderID, task.ProviderModelID, nil
	}

	// Priority 2: AgentRole default
	if role != nil {
		rolePair := pairOrEmpty(role.DefaultProviderID, role.DefaultProviderModelID)
		if rolePair.partial {
			return "", "", fmt.Errorf("%w: agent role provider partial pair", ErrInvalidInput)
		}
		if rolePair.complete {
			if err := s.validateProviderPair(ctx, role.DefaultProviderID, role.DefaultProviderModelID); err != nil {
				return "", "", err
			}
			return role.DefaultProviderID, role.DefaultProviderModelID, nil
		}
	}

	// Priority 3: System default (empty → SessionManager handles)
	return "", "", nil
}
