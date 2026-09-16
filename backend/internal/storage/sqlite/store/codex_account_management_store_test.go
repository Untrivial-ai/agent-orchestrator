package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestCodexAccountSwitchIdempotencyAndSingleActiveConstraint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	first := domain.CodexAccountSwitch{
		ID: "switch-a", SourceKind: domain.CodexAccountSwitchSourceDevice, TargetAccountID: "account-b",
		IdempotencyKey: "request-a",
		Phase:          domain.CodexAccountSwitchActivatingAccount, CreatedAt: now, UpdatedAt: now,
	}

	created, inserted, err := st.CreateCodexAccountSwitch(ctx, first)
	if err != nil || !inserted || created.ID != first.ID || created.SourceKind != domain.CodexAccountSwitchSourceDevice || created.SourceAccountID != "" {
		t.Fatalf("create switch: got=%+v inserted=%v err=%v", created, inserted, err)
	}
	replayed, inserted, err := st.CreateCodexAccountSwitch(ctx, first)
	if err != nil || inserted || replayed.ID != first.ID {
		t.Fatalf("replay switch: got=%+v inserted=%v err=%v", replayed, inserted, err)
	}
	conflict := first
	conflict.ID = "switch-b"
	conflict.TargetAccountID = "account-c"
	if _, _, err := st.CreateCodexAccountSwitch(ctx, conflict); !errors.Is(err, ports.ErrCodexAccountSwitchIdempotencyConflict) {
		t.Fatalf("idempotency conflict error = %v", err)
	}
	other := first
	other.ID = "switch-c"
	other.IdempotencyKey = "request-c"
	if _, _, err := st.CreateCodexAccountSwitch(ctx, other); !errors.Is(err, ports.ErrCodexAccountSwitchInProgress) {
		t.Fatalf("active switch conflict error = %v", err)
	}
}

func TestCodexAccountSwitchRejectsObsoletePhases(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"waiting_for_safe_boundary", "cancelled"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			st := newTestStore(t)
			now := time.Now().UTC().Truncate(time.Second)
			switchRecord := domain.CodexAccountSwitch{
				ID: "switch-" + phase, SourceAccountID: "account-a", TargetAccountID: "account-b",
				IdempotencyKey: "request-" + phase,
				Phase:          domain.CodexAccountSwitchPhase(phase), CreatedAt: now, UpdatedAt: now,
			}

			if _, _, err := st.CreateCodexAccountSwitch(context.Background(), switchRecord); err == nil {
				t.Fatalf("create switch with obsolete phase %q succeeded", phase)
			}
		})
	}
}

func TestCodexAccountSwitchTransitionsAreCompareAndSwap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	sw := domain.CodexAccountSwitch{
		ID: "switch-cas", SourceAccountID: "account-a", TargetAccountID: "account-b",
		IdempotencyKey: "request-cas",
		Phase:          domain.CodexAccountSwitchActivatingAccount, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := st.CreateCodexAccountSwitch(ctx, sw); err != nil {
		t.Fatal(err)
	}
	sw.Phase = domain.CodexAccountSwitchCompleted
	sw.UpdatedAt = now.Add(time.Second)
	if ok, err := st.UpdateCodexAccountSwitch(ctx, sw, domain.CodexAccountSwitchActivatingAccount); err != nil || !ok {
		t.Fatalf("switch transition: ok=%v err=%v", ok, err)
	}
	if ok, err := st.UpdateCodexAccountSwitch(ctx, sw, domain.CodexAccountSwitchActivatingAccount); err != nil || ok {
		t.Fatalf("stale switch transition: ok=%v err=%v", ok, err)
	}
}
