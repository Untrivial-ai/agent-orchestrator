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
		ID: "switch-a", SourceAccountID: "account-a", TargetAccountID: "account-b",
		IdempotencyKey: "request-a", RequestFingerprint: "v1:first", ExpectedAccountRevision: 1,
		Phase: domain.CodexAccountSwitchRequested, CreatedAt: now, UpdatedAt: now,
	}

	created, inserted, err := st.CreateCodexAccountSwitch(ctx, first)
	if err != nil || !inserted || created.ID != first.ID {
		t.Fatalf("create switch: got=%+v inserted=%v err=%v", created, inserted, err)
	}
	replayed, inserted, err := st.CreateCodexAccountSwitch(ctx, first)
	if err != nil || inserted || replayed.ID != first.ID {
		t.Fatalf("replay switch: got=%+v inserted=%v err=%v", replayed, inserted, err)
	}
	conflict := first
	conflict.ID = "switch-b"
	conflict.RequestFingerprint = "v1:different"
	if _, _, err := st.CreateCodexAccountSwitch(ctx, conflict); !errors.Is(err, ports.ErrCodexAccountSwitchIdempotencyConflict) {
		t.Fatalf("idempotency conflict error = %v", err)
	}
	other := first
	other.ID = "switch-c"
	other.IdempotencyKey = "request-c"
	other.RequestFingerprint = "v1:other"
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
				IdempotencyKey: "request-" + phase, RequestFingerprint: "v1:" + phase, ExpectedAccountRevision: 1,
				Phase: domain.CodexAccountSwitchPhase(phase), CreatedAt: now, UpdatedAt: now,
			}

			if _, _, err := st.CreateCodexAccountSwitch(context.Background(), switchRecord); err == nil {
				t.Fatalf("create switch with obsolete phase %q succeeded", phase)
			}
		})
	}
}

func TestCodexAccountSwitchAndSessionTransitionsAreCompareAndSwap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newTestStore(t)
	seedProject(t, st, "codex-switch")
	rec := sampleRecord("codex-switch")
	rec.Harness = domain.HarnessCodex
	session, err := st.CreateSession(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	sw := domain.CodexAccountSwitch{
		ID: "switch-cas", SourceAccountID: "account-a", TargetAccountID: "account-b",
		IdempotencyKey: "request-cas", RequestFingerprint: "v1:cas", ExpectedAccountRevision: 1,
		Phase: domain.CodexAccountSwitchRequested, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := st.CreateCodexAccountSwitch(ctx, sw); err != nil {
		t.Fatal(err)
	}
	switchSession := domain.CodexAccountSwitchSession{
		SessionID: session.ID, NativeSessionID: "native-a", InterfaceMode: domain.SessionModeTUI,
		WasRunning: true, StopState: "pending", RestartState: "pending",
		ReviewerStopState: "skipped", ReviewerRestartState: "skipped",
	}
	if err := st.InsertCodexAccountSwitchSession(ctx, sw.ID, switchSession); err != nil {
		t.Fatal(err)
	}
	switchSession.StopState = "stopped"
	stopped := now.Add(time.Second)
	switchSession.StoppedAt = &stopped
	if ok, err := st.UpdateCodexAccountSwitchSession(ctx, sw.ID, switchSession, "pending", "pending"); err != nil || !ok {
		t.Fatalf("session transition: ok=%v err=%v", ok, err)
	}
	if ok, err := st.UpdateCodexAccountSwitchSession(ctx, sw.ID, switchSession, "pending", "pending"); err != nil || ok {
		t.Fatalf("stale session transition: ok=%v err=%v", ok, err)
	}
	sw.Phase = domain.CodexAccountSwitchStoppingSessions
	sw.UpdatedAt = stopped
	if ok, err := st.UpdateCodexAccountSwitch(ctx, sw, domain.CodexAccountSwitchRequested); err != nil || !ok {
		t.Fatalf("switch transition: ok=%v err=%v", ok, err)
	}
	if ok, err := st.UpdateCodexAccountSwitch(ctx, sw, domain.CodexAccountSwitchRequested); err != nil || ok {
		t.Fatalf("stale switch transition: ok=%v err=%v", ok, err)
	}
}

func TestCreateCodexAccountSwitchAtomicallyPersistsCompleteSessionSnapshot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newTestStore(t)
	seedProject(t, st, "codex-snapshot")
	rec := sampleRecord("codex-snapshot")
	rec.Harness = domain.HarnessCodex
	session, err := st.CreateSession(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	sw := domain.CodexAccountSwitch{
		ID: "switch-snapshot", SourceAccountID: "account-a", TargetAccountID: "account-b",
		IdempotencyKey: "request-snapshot", RequestFingerprint: "v1:snapshot", ExpectedAccountRevision: 1,
		Phase: domain.CodexAccountSwitchRequested, CreatedAt: now, UpdatedAt: now,
		Sessions: []domain.CodexAccountSwitchSession{{
			SessionID: session.ID, NativeSessionID: "native-worker", InterfaceMode: domain.SessionModeTUI,
			SourceHandleID: "worker-handle", SourceGeneration: "worker-generation",
			WasRunning: true, StopState: "pending", RestartState: "pending",
			ReviewerWasRunning: true, ReviewerSourceHandleID: "reviewer-handle",
			ReviewerNativeSessionID: "native-reviewer", ReviewerStopState: "pending", ReviewerRestartState: "pending",
		}},
	}

	created, inserted, err := st.CreateCodexAccountSwitch(ctx, sw)
	if err != nil || !inserted || len(created.Sessions) != 1 {
		t.Fatalf("CreateCodexAccountSwitch = %+v, inserted=%v, err=%v", created, inserted, err)
	}
	got, err := st.ListCodexAccountSwitchSessions(ctx, sw.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("snapshot rows = %d, want 1", len(got))
	}
	if got[0].SourceHandleID != "worker-handle" || got[0].ReviewerSourceHandleID != "reviewer-handle" ||
		got[0].NativeSessionID != "native-worker" || got[0].ReviewerNativeSessionID != "native-reviewer" {
		t.Fatalf("private snapshot identities = %+v", got[0])
	}
}

func TestCreateCodexAccountSwitchRollsBackWhenAnySnapshotRowFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := newTestStore(t)
	seedProject(t, st, "codex-rollback")
	rec := sampleRecord("codex-rollback")
	rec.Harness = domain.HarnessCodex
	session, err := st.CreateSession(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	sw := domain.CodexAccountSwitch{
		ID: "switch-rollback", SourceAccountID: "account-a", TargetAccountID: "account-b",
		IdempotencyKey: "request-rollback", RequestFingerprint: "v1:rollback", ExpectedAccountRevision: 1,
		Phase: domain.CodexAccountSwitchRequested, CreatedAt: now, UpdatedAt: now,
		Sessions: []domain.CodexAccountSwitchSession{
			{SessionID: session.ID, InterfaceMode: domain.SessionModeTUI, WasRunning: true, StopState: "pending", RestartState: "pending", ReviewerStopState: "skipped", ReviewerRestartState: "skipped"},
			{SessionID: domain.SessionID("missing-session"), InterfaceMode: domain.SessionModeTUI, WasRunning: true, StopState: "pending", RestartState: "pending", ReviewerStopState: "skipped", ReviewerRestartState: "skipped"},
		},
	}

	if _, _, err := st.CreateCodexAccountSwitch(ctx, sw); err == nil {
		t.Fatal("CreateCodexAccountSwitch succeeded with invalid snapshot row")
	}
	if _, ok, err := st.GetCodexAccountSwitch(ctx, sw.ID); err != nil || ok {
		t.Fatalf("switch survived rollback: ok=%v err=%v", ok, err)
	}
	if got, err := st.ListCodexAccountSwitchSessions(ctx, sw.ID); err != nil || len(got) != 0 {
		t.Fatalf("snapshot rows survived rollback: rows=%+v err=%v", got, err)
	}
}
