package integration

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// terminateWithoutTeardown simulates the legacy leak paths (#2811, failed
// crash finalization): the row goes terminal but no teardown runs, so the
// worktree stays on disk.
func terminateWithoutTeardown(t *testing.T, st *stack, id domain.SessionID) {
	t.Helper()
	ctx := context.Background()
	rec, ok, err := st.store.GetSession(ctx, id)
	if err != nil || !ok {
		t.Fatalf("get session %s: ok=%v err=%v", id, ok, err)
	}
	rec.IsTerminated = true
	rec.Activity = domain.Activity{State: domain.ActivityExited, LastActivityAt: time.Now()}
	if err := st.store.UpdateSession(ctx, rec); err != nil {
		t.Fatal(err)
	}
}

// TestCleanupIsIdempotentAndRecordsFacts covers #3402: every cleanup attempt
// must persist a durable disposition, and an already-reclaimed session must
// not be re-torn-down (or re-listed) by the next run.
func TestCleanupIsIdempotentAndRecordsFacts(t *testing.T) {
	ctx := context.Background()
	st := newStack(t)
	st.ws.root = t.TempDir()

	sess, _, _, err := st.sm.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, Branch: "b", Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	terminateWithoutTeardown(t, st, sess.ID)

	first, err := st.mgr.Cleanup(ctx, "mer")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Cleaned) != 1 || first.Cleaned[0] != sess.ID {
		t.Fatalf("first cleanup = %+v, want cleaned [%s]", first, sess.ID)
	}
	facts, ok, err := st.store.GetSessionCleanupFacts(ctx, sess.ID)
	if err != nil || !ok {
		t.Fatalf("cleanup facts missing after success: ok=%v err=%v", ok, err)
	}
	if facts.WorkspaceDisposition != domain.DispositionRemoved || facts.RuntimeReleasedAt.IsZero() {
		t.Fatalf("cleanup facts = %+v, want removed disposition with runtime released", facts)
	}

	destroysAfterFirst := st.ws.destroyed
	second, err := st.mgr.Cleanup(ctx, "mer")
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Cleaned) != 0 || len(second.Skipped) != 0 {
		t.Fatalf("second cleanup re-listed a reclaimed session: %+v", second)
	}
	if st.ws.destroyed != destroysAfterFirst {
		t.Fatalf("second cleanup re-destroyed the workspace: %d -> %d", destroysAfterFirst, st.ws.destroyed)
	}
}

// TestTerminalResourceGCReclaimsLeakedWorktrees covers the GC loop: a session
// terminated without teardown is discovered via the durable candidate scan,
// its worktree reclaimed, and the pass converges (no candidates left).
func TestTerminalResourceGCReclaimsLeakedWorktrees(t *testing.T) {
	ctx := context.Background()
	st := newStack(t)
	st.ws.root = t.TempDir()

	sess, _, _, err := st.sm.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, Branch: "b", Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	rec, _, _ := st.store.GetSession(ctx, sess.ID)
	worktree := rec.Metadata.WorkspacePath
	terminateWithoutTeardown(t, st, sess.ID)

	result, err := st.mgr.RunTerminalResourceGC(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Cleaned) != 1 || result.Cleaned[0] != sess.ID {
		t.Fatalf("gc pass = %+v, want cleaned [%s]", result, sess.ID)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("leaked worktree survived gc: stat err=%v", err)
	}

	again, err := st.mgr.RunTerminalResourceGC(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Cleaned) != 0 || len(again.Skipped) != 0 {
		t.Fatalf("gc did not converge: %+v", again)
	}
}

// TestTerminalResourceGCPreservesDirtyWorktrees: a dirty worktree is never
// force-deleted by the GC. The refusal is recorded as preserved_dirty, which
// pauses auto-retry; a manual cleanup is the user-triggered retry.
func TestTerminalResourceGCPreservesDirtyWorktrees(t *testing.T) {
	ctx := context.Background()
	st := newStack(t)
	st.ws.root = t.TempDir()

	sess, _, _, err := st.sm.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, Branch: "b", Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	terminateWithoutTeardown(t, st, sess.ID)
	st.ws.destroyErr = ports.ErrWorkspaceDirty

	result, err := st.mgr.RunTerminalResourceGC(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].Reason != "workspace has uncommitted changes" {
		t.Fatalf("gc pass = %+v, want one dirty skip", result)
	}
	facts, ok, _ := st.store.GetSessionCleanupFacts(ctx, sess.ID)
	if !ok || facts.WorkspaceDisposition != domain.DispositionPreservedDirty || facts.FailureCode != "workspace_dirty" {
		t.Fatalf("cleanup facts = %+v, want preserved_dirty/workspace_dirty", facts)
	}

	// preserved_dirty pauses the GC: the session is no longer a candidate.
	again, err := st.mgr.RunTerminalResourceGC(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Cleaned) != 0 || len(again.Skipped) != 0 {
		t.Fatalf("gc revisited a preserved-dirty session: %+v", again)
	}

	// A manual cleanup is the user-triggered retry and reclaims it once the
	// worktree is clean again.
	st.ws.destroyErr = nil
	retried, err := st.mgr.Cleanup(ctx, "mer")
	if err != nil {
		t.Fatal(err)
	}
	if len(retried.Cleaned) != 1 || retried.Cleaned[0] != sess.ID {
		t.Fatalf("manual retry = %+v, want cleaned [%s]", retried, sess.ID)
	}
	facts, _, _ = st.store.GetSessionCleanupFacts(ctx, sess.ID)
	if facts.WorkspaceDisposition != domain.DispositionRemoved {
		t.Fatalf("facts after manual retry = %+v, want removed", facts)
	}
}

// TestCleanupFactsGoStaleWhenSessionRevives: restoring a session bumps its
// cleanup generation, so facts recorded for the previous terminal phase no
// longer shield a later re-termination from the GC.
func TestCleanupFactsGoStaleWhenSessionRevives(t *testing.T) {
	ctx := context.Background()
	st := newStack(t)
	st.ws.root = t.TempDir()

	sess, _, _, err := st.sm.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, Branch: "b", Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	terminateWithoutTeardown(t, st, sess.ID)
	if result, err := st.mgr.RunTerminalResourceGC(ctx); err != nil || len(result.Cleaned) != 1 {
		t.Fatalf("initial gc = %+v err=%v", result, err)
	}

	if _, err := st.mgr.RestoreWithMode(ctx, sess.ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	rec, _, _ := st.store.GetSession(ctx, sess.ID)
	if rec.IsTerminated {
		t.Fatal("session still terminated after restore")
	}
	if rec.CleanupGeneration == 0 {
		t.Fatalf("restore did not bump cleanup generation: %+v", rec)
	}

	terminateWithoutTeardown(t, st, sess.ID)
	result, err := st.mgr.RunTerminalResourceGC(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Cleaned) != 1 || result.Cleaned[0] != sess.ID {
		t.Fatalf("gc after revive = %+v, want cleaned [%s] (stale facts must not shield it)", result, sess.ID)
	}
}

func TestCrashFinalizationDoesNotCreateBootRestoreMarker(t *testing.T) {
	ctx := context.Background()
	st := newStack(t)
	st.ws.root = t.TempDir()

	sess, _, _, err := st.sm.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, Branch: "b", Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	rec, ok, err := st.store.GetSession(ctx, sess.ID)
	if err != nil || !ok {
		t.Fatalf("get session: ok=%v err=%v", ok, err)
	}
	rec.Activity.LastActivityAt = time.Now().Add(-2 * time.Minute)
	if err := st.store.UpdateSession(ctx, rec); err != nil {
		t.Fatal(err)
	}

	if err := st.lcm.ApplyRuntimeObservation(ctx, sess.ID, ports.RuntimeFacts{
		Runtime:  ports.ProbeAlive,
		Workload: ports.ProbeDead,
		LaunchID: rec.Metadata.RuntimeLaunchID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.lcm.ApplyRuntimeObservation(ctx, sess.ID, ports.RuntimeFacts{
		Runtime:    ports.ProbeDead,
		Workload:   ports.ProbeFailed,
		LaunchID:   rec.Metadata.RuntimeLaunchID,
		ObservedAt: time.Now().Add(2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.store.ListSessionWorktrees(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("crash finalization created %d boot restore markers, want 0", len(rows))
	}
	got, _, _ := st.store.GetSession(ctx, sess.ID)
	if !got.IsTerminated {
		t.Fatal("crashed session was not terminated")
	}
}

func TestCleanupFactsTrackRuntimeReleaseIndependently(t *testing.T) {
	ctx := context.Background()
	st := newStack(t)
	st.ws.root = t.TempDir()

	sess, _, _, err := st.sm.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, Branch: "b", Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	terminateWithoutTeardown(t, st, sess.ID)
	st.rt.destroyErr = errors.New("runtime unavailable")
	if _, err := st.mgr.RunTerminalResourceGC(ctx); err != nil {
		t.Fatal(err)
	}
	facts, ok, err := st.store.GetSessionCleanupFacts(ctx, sess.ID)
	if err != nil || !ok {
		t.Fatalf("cleanup facts: ok=%v err=%v", ok, err)
	}
	if !facts.RuntimeReleasedAt.IsZero() || facts.FailureCode != "runtime_release_failed" {
		t.Fatalf("facts after runtime failure = %+v", facts)
	}

	st.rt.destroyErr = nil
	if _, err := st.mgr.RunTerminalResourceGC(ctx); err != nil {
		t.Fatal(err)
	}
	facts, _, _ = st.store.GetSessionCleanupFacts(ctx, sess.ID)
	if facts.RuntimeReleasedAt.IsZero() {
		t.Fatalf("runtime release was not recorded after retry: %+v", facts)
	}
}
