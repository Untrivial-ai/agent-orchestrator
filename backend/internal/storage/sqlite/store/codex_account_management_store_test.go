package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestCodexActiveAccountUsesRevisionCAS(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	if _, ok, err := st.GetCodexActiveAccount(ctx); err != nil || ok {
		t.Fatalf("initial active account: ok=%v err=%v", ok, err)
	}
	first, err := st.SetCodexActiveAccount(ctx, "account-a", 0, now)
	if err != nil || first.AccountID != "account-a" || first.Revision != 1 {
		t.Fatalf("create active account: got=%+v err=%v", first, err)
	}
	if _, err := st.SetCodexActiveAccount(ctx, "account-b", 0, now); !errors.Is(err, ports.ErrCodexAccountRevisionConflict) {
		t.Fatalf("duplicate initial revision error = %v", err)
	}
	second, err := st.SetCodexActiveAccount(ctx, "account-b", 1, now.Add(time.Second))
	if err != nil || second.AccountID != "account-b" || second.Revision != 2 {
		t.Fatalf("advance active account: got=%+v err=%v", second, err)
	}
	if _, err := st.SetCodexActiveAccount(ctx, "account-c", 1, now.Add(2*time.Second)); !errors.Is(err, ports.ErrCodexAccountRevisionConflict) {
		t.Fatalf("stale revision error = %v", err)
	}
	signedOut, err := st.SetCodexActiveAccount(ctx, "", 2, now.Add(3*time.Second))
	if err != nil || signedOut.AccountID != "" || signedOut.Revision != 3 {
		t.Fatalf("clear active account: got=%+v err=%v", signedOut, err)
	}
}

func TestCodexAccountSwitchIdempotencyAndSingleActiveConstraint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	first := domain.CodexAccountSwitch{
		ID: "switch-a", SourceKind: domain.CodexAccountSwitchSourceDevice, TargetAccountID: "account-b",
		IdempotencyKey: "request-a", RequestFingerprint: "v3:first", ExpectedAccountRevision: 1,
		RestartIdleSessions: true, Phase: domain.CodexAccountSwitchRequested, CreatedAt: now, UpdatedAt: now,
	}

	created, inserted, err := st.CreateCodexAccountSwitch(ctx, first)
	if err != nil || !inserted || created.ID != first.ID || created.SourceKind != domain.CodexAccountSwitchSourceDevice || created.SourceAccountID != "" || !created.RestartIdleSessions {
		t.Fatalf("create switch: got=%+v inserted=%v err=%v", created, inserted, err)
	}
	replayed, inserted, err := st.CreateCodexAccountSwitch(ctx, first)
	if err != nil || inserted || replayed.ID != first.ID || !replayed.RestartIdleSessions {
		t.Fatalf("replay switch: got=%+v inserted=%v err=%v", replayed, inserted, err)
	}
	conflict := first
	conflict.ID = "switch-b"
	conflict.RequestFingerprint = "v3:different"
	if _, _, err := st.CreateCodexAccountSwitch(ctx, conflict); !errors.Is(err, ports.ErrCodexAccountSwitchIdempotencyConflict) {
		t.Fatalf("idempotency conflict error = %v", err)
	}
	other := first
	other.ID = "switch-c"
	other.IdempotencyKey = "request-c"
	other.RequestFingerprint = "v3:other"
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
				IdempotencyKey: "request-" + phase, RequestFingerprint: "v3:" + phase, ExpectedAccountRevision: 1,
				Phase: domain.CodexAccountSwitchPhase(phase), CreatedAt: now, UpdatedAt: now,
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
		IdempotencyKey: "request-cas", RequestFingerprint: "v3:cas", ExpectedAccountRevision: 1,
		Phase: domain.CodexAccountSwitchRequested, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := st.CreateCodexAccountSwitch(ctx, sw); err != nil {
		t.Fatal(err)
	}
	sw.Phase = domain.CodexAccountSwitchCheckpointCredential
	sw.UpdatedAt = now.Add(time.Second)
	if ok, err := st.UpdateCodexAccountSwitch(ctx, sw, domain.CodexAccountSwitchRequested); err != nil || !ok {
		t.Fatalf("switch transition: ok=%v err=%v", ok, err)
	}
	if ok, err := st.UpdateCodexAccountSwitch(ctx, sw, domain.CodexAccountSwitchRequested); err != nil || ok {
		t.Fatalf("stale switch transition: ok=%v err=%v", ok, err)
	}
}
