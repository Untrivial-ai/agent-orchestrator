package reconcile

import (
	"context"
	"errors"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
)

func (r *Reconciler) reconcileCreations(ctx context.Context, record domain.Sandbox, provider sandbox.Provider) (bool, error) {
	creations, err := r.store.ListSandboxCreations(ctx, record.OrgID, record.SessionID)
	if err != nil {
		return true, err
	}
	for _, creation := range creations {
		if creation.EnvironmentID == "" {
			environment, found, err := provider.FindBySession(ctx, record.SessionID)
			if err != nil {
				return true, err
			}
			if !found {
				// Absence is inconclusive while the provider may still finish Create.
				return true, r.waitForCreation(ctx, record, "Waiting for the provider creation outcome.")
			}
			creation.EnvironmentID = string(environment.ID)
			if err := r.store.RecordSandboxCreationResult(ctx, record.OrgID, record.SessionID, creation.ID, creation.EnvironmentID); err != nil {
				return true, err
			}
		}
		if record.DesiredState != domain.SandboxDesiredDeleted && record.ProviderEnvironmentID == creation.EnvironmentID {
			if err := r.store.ResolveSandboxCreation(ctx, record.OrgID, record.SessionID, creation.ID, "adopted"); err != nil {
				return true, err
			}
			continue
		}
		if record.DesiredState != domain.SandboxDesiredDeleted && record.ProviderEnvironmentID == "" && record.PreparationGeneration == creation.Generation {
			if err := r.observe(ctx, record, creation.EnvironmentID, domain.SandboxObservedProvisioning, "", time.Second); err != nil {
				return true, err
			}
			return true, r.store.ResolveSandboxCreation(ctx, record.OrgID, record.SessionID, creation.ID, "adopted")
		}
		environment, err := provider.Get(ctx, sandbox.ID(creation.EnvironmentID))
		if errors.Is(err, sandbox.ErrNotFound) || (err == nil && environment.State == sandbox.StateDeleted) {
			if err := r.store.ResolveSandboxCreation(ctx, record.OrgID, record.SessionID, creation.ID, "deleted"); err != nil {
				return true, err
			}
			continue
		}
		if err != nil {
			return true, err
		}
		if environment.State != sandbox.StateDeleting {
			if err := provider.Delete(ctx, sandbox.ID(creation.EnvironmentID)); err != nil && !errors.Is(err, sandbox.ErrNotFound) {
				return true, r.fail(ctx, record, err)
			}
		}
		return true, r.waitForCreation(ctx, record, "Waiting for late sandbox deletion.")
	}
	return false, nil
}

func (r *Reconciler) waitForCreation(ctx context.Context, record domain.Sandbox, message string) error {
	state := record.ObservedState
	if record.DesiredState == domain.SandboxDesiredDeleted {
		state = domain.SandboxObservedDeleting
	}
	if state == "" {
		state = domain.SandboxObservedProvisioning
	}
	return r.observe(ctx, record, record.ProviderEnvironmentID, state, message, 5*time.Second)
}

func (r *Reconciler) completeSandboxDeletion(ctx context.Context, record domain.Sandbox, provider sandbox.Provider) error {
	if cleaner, ok := provider.(sandbox.SessionCleaner); ok {
		if err := cleaner.CleanupSession(ctx, record.OrgID, record.SessionID); err != nil {
			return r.fail(ctx, record, err)
		}
	}
	return r.store.CompleteSandboxDeletion(ctx, r.owner, record.OrgID, record.SessionID)
}
