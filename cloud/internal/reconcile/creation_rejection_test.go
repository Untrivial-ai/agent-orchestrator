package reconcile

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
)

type cleanupRecoveryProvider struct {
	*recoveryProvider
	cleanups   int
	cleanupErr error
}

func (p *cleanupRecoveryProvider) CleanupSession(context.Context, string, string) error {
	p.cleanups++
	return p.cleanupErr
}

func TestRejectedCreationAllowsRetryAndCancellation(t *testing.T) {
	for _, cancelAttempt := range []bool{false, true} {
		store := &lifecycleStore{}
		provider := &cleanupRecoveryProvider{recoveryProvider: &recoveryProvider{
			lateCreateProvider: &lateCreateProvider{lifecycleProvider: &lifecycleProvider{}},
			createErr:          errors.Join(sandbox.ErrCreateRejected, errors.New("missing image")),
		}}
		record := domain.Sandbox{OrgID: "org", SessionID: "session", Provider: sandbox.ProviderDocker, DesiredState: domain.SandboxDesiredRunning}
		r := testReconciler(store, provider)
		ctx, cancel := context.WithCancel(context.Background())
		if cancelAttempt {
			provider.cancel = cancel
		}
		_ = r.provision(ctx, record, provider)
		cancel()
		if len(store.creations) != 0 {
			t.Fatal("rejected attempt blocks reconciliation")
		}
		if handled, err := r.reconcileCreations(context.Background(), record, provider); handled || err != nil {
			t.Fatalf("handled=%v err=%v", handled, err)
		}
		if !cancelAttempt {
			provider.createErr = nil
			provider.environment = sandbox.Environment{ID: "retry", State: sandbox.StateRunning}
			if err := r.provision(context.Background(), record, provider); err != nil {
				t.Fatal(err)
			}
			if len(store.creations) != 1 || store.creations[0].EnvironmentID != "retry" || provider.cleanups != 0 {
				t.Fatal("retry lost its identity or workspace")
			}
			continue
		}
		record.DesiredState = domain.SandboxDesiredDeleted
		provider.cleanupErr = errors.New("storage unavailable")
		_ = r.reconcileSandbox(context.Background(), record)
		if store.completedDeletions != 0 || provider.cleanups != 1 {
			t.Fatal("deletion completed before storage cleanup")
		}
		provider.cleanupErr = nil
		if err := r.reconcileSandbox(context.Background(), record); err != nil {
			t.Fatal(err)
		}
		if store.completedDeletions != 1 || provider.cleanups != 2 {
			t.Fatal("cancellation did not retry cleanup")
		}
	}
}

func TestUncertainVolumeCreationDoesNotRunCleanup(t *testing.T) {
	store := &lifecycleStore{}
	provider := &cleanupRecoveryProvider{recoveryProvider: &recoveryProvider{
		lateCreateProvider: &lateCreateProvider{lifecycleProvider: &lifecycleProvider{}}, createErr: context.DeadlineExceeded,
	}}
	record := domain.Sandbox{OrgID: "org", SessionID: "session", Provider: sandbox.ProviderDocker, DesiredState: domain.SandboxDesiredRunning}
	r := testReconciler(store, provider)
	_ = r.provision(context.Background(), record, provider)
	record.DesiredState = domain.SandboxDesiredDeleted
	if err := r.reconcileSandbox(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if len(store.creations) != 1 || provider.cleanups != 0 || store.completedDeletions != 0 {
		t.Fatal("uncertain creation was allowed to resurrect after cleanup")
	}
}
