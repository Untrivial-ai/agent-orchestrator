package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// fakeReviewer stands in for the review service's ReviewerTerminator seam.
type fakeReviewer struct {
	terminateCalls int
	terminateErr   error
}

func (f *fakeReviewer) TerminateReviewer(context.Context, domain.SessionID, string) error {
	f.terminateCalls++
	return f.terminateErr
}
func (f *fakeReviewer) TeardownReviewerTerminal(context.Context, domain.SessionID) error {
	return nil
}
func (f *fakeReviewer) RestoreReviewer(context.Context, domain.SessionID) error { return nil }

// finalizeManager builds a Manager wired for finalizer tests, with a fixed clock
// (so backoff/next_attempt_at are deterministic) and an optional reviewer.
func finalizeManager(rv ReviewerTerminator) (*Manager, *fakeStore, *fakeRuntime, *fakeWorkspace) {
	st := newFakeStore()
	rt := &fakeRuntime{}
	ws := &fakeWorkspace{}
	m := New(Deps{
		Runtime:   rt,
		Agents:    fakeAgents{},
		Workspace: ws,
		Store:     st,
		Messenger: &fakeMessenger{},
		Lifecycle: &fakeLCM{store: st},
		Clock:     func() time.Time { return time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC) },
		LookPath:  func(string) (string, error) { return "/bin/true", nil },
	})
	m.SetReviewerTerminator(rv)
	return m, st, rt, ws
}

func TestFinalize_CleanTeardownReleasesRuntimeAndWorkspace(t *testing.T) {
	m, st, rt, ws := finalizeManager(nil)
	seedTerminal(st, "mer-1", domain.SessionMetadata{WorkspacePath: "/ws/mer-1", RuntimeHandleID: "h1"})

	if err := m.FinalizeTerminalSession(ctx, "mer-1"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if rt.destroyed != 1 || ws.destroyed != 1 {
		t.Fatalf("runtime destroyed=%d workspace destroyed=%d, want 1/1", rt.destroyed, ws.destroyed)
	}
	facts, ok := st.cleanup["mer-1"]
	if !ok {
		t.Fatal("no cleanup facts written")
	}
	if facts.WorkspaceDisposition != domain.DispositionRemoved {
		t.Fatalf("disposition = %q, want removed", facts.WorkspaceDisposition)
	}
	if facts.RuntimeReleasedAt.IsZero() {
		t.Fatal("runtime_released_at should be stamped on a genuine release")
	}
}

func TestFinalize_DirtyWorkspaceIsPreservedDirty(t *testing.T) {
	m, st, _, ws := finalizeManager(nil)
	ws.destroyErr = fmt.Errorf("gitworktree: refusing to remove: %w", ports.ErrWorkspaceDirty)
	seedTerminal(st, "mer-1", domain.SessionMetadata{WorkspacePath: "/ws/mer-1", RuntimeHandleID: "h1"})

	if err := m.FinalizeTerminalSession(ctx, "mer-1"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	facts := st.cleanup["mer-1"]
	if facts.WorkspaceDisposition != domain.DispositionPreservedDirty {
		t.Fatalf("disposition = %q, want preserved_dirty", facts.WorkspaceDisposition)
	}
	// A preserved-dirty session is terminal for auto-retry: no next_attempt_at.
	if !facts.NextAttemptAt.IsZero() {
		t.Fatalf("next_attempt_at = %v, want none for preserved_dirty", facts.NextAttemptAt)
	}
}

func TestFinalize_RuntimeFailLeavesWorkspaceAndSchedulesRetry(t *testing.T) {
	m, st, rt, ws := finalizeManager(nil)
	rt.destroyErr = errors.New("tmux transient")
	seedTerminal(st, "mer-1", domain.SessionMetadata{WorkspacePath: "/ws/mer-1", RuntimeHandleID: "h1"})

	if err := m.FinalizeTerminalSession(ctx, "mer-1"); err != nil {
		t.Fatalf("finalize should not surface a terminated session's teardown error: %v", err)
	}
	if ws.destroyed != 0 {
		t.Fatal("runtime-first: workspace must be left untouched when runtime destroy fails")
	}
	facts := st.cleanup["mer-1"]
	if facts.WorkspaceDisposition != domain.DispositionPending {
		t.Fatalf("disposition = %q, want pending (retry scheduled)", facts.WorkspaceDisposition)
	}
	if facts.AttemptCount != 1 || facts.NextAttemptAt.IsZero() {
		t.Fatalf("attempt=%d next=%v, want attempt 1 with a scheduled retry", facts.AttemptCount, facts.NextAttemptAt)
	}
	if facts.FailureCode != failRuntimeDestroy {
		t.Fatalf("failure code = %q, want %q", facts.FailureCode, failRuntimeDestroy)
	}
	if !facts.RuntimeReleasedAt.IsZero() {
		t.Fatal("runtime must NOT be recorded released when its destroy failed")
	}
}

// TestFinalize_UnresolvedRuntimeHandleFailsClosed pins review gap #1 on #2931:
// a terminal session with an empty/unrouted RuntimeHandleID (a legacy/backlog
// row, e.g. #1458) must not let releaseTerminalResources proceed to destroy
// its workspace — an unrouted runtime could still be live, and deleting its
// cwd out from under it can't be undone. The release must fail closed
// (pending/retryable) instead of false-succeeding.
func TestFinalize_UnresolvedRuntimeHandleFailsClosed(t *testing.T) {
	m, st, _, ws := finalizeManager(nil)
	seedTerminal(st, "mer-1", domain.SessionMetadata{WorkspacePath: "/ws/mer-1"}) // no RuntimeHandleID

	if err := m.FinalizeTerminalSession(ctx, "mer-1"); err != nil {
		t.Fatalf("finalize should not surface a terminated session's teardown error: %v", err)
	}
	if ws.destroyed != 0 {
		t.Fatal("an unresolved runtime handle must block workspace release, not just skip the runtime step")
	}
	facts := st.cleanup["mer-1"]
	if facts.WorkspaceDisposition != domain.DispositionPending {
		t.Fatalf("disposition = %q, want pending (retry scheduled)", facts.WorkspaceDisposition)
	}
	if facts.FailureCode != failRuntimeUnresolved {
		t.Fatalf("failure code = %q, want %q", facts.FailureCode, failRuntimeUnresolved)
	}
	if !facts.RuntimeReleasedAt.IsZero() {
		t.Fatal("runtime must NOT be recorded released when its handle was never resolved")
	}
}

func TestFinalize_RetryExhaustionMarksFailed(t *testing.T) {
	m, st, rt, _ := finalizeManager(nil)
	rt.destroyErr = errors.New("permanently wedged")
	seedTerminal(st, "mer-1", domain.SessionMetadata{WorkspacePath: "/ws/mer-1", RuntimeHandleID: "h1"})

	// Each attempt fails transiently and increments the count; the cap flips it to
	// a terminal `failed` disposition that stops auto-retry.
	for i := 0; i < maxCleanupAttempts; i++ {
		if err := m.FinalizeTerminalSession(ctx, "mer-1"); err != nil {
			t.Fatalf("finalize attempt %d: %v", i, err)
		}
	}
	facts := st.cleanup["mer-1"]
	if facts.WorkspaceDisposition != domain.DispositionFailed {
		t.Fatalf("disposition = %q, want failed after %d attempts", facts.WorkspaceDisposition, maxCleanupAttempts)
	}
	if facts.AttemptCount != int64(maxCleanupAttempts) {
		t.Fatalf("attempt count = %d, want %d", facts.AttemptCount, maxCleanupAttempts)
	}
	if !facts.NextAttemptAt.IsZero() {
		t.Fatal("a failed (exhausted) cleanup must not schedule further auto-retry")
	}
}

func TestFinalize_IdempotentReRunAfterRemoved(t *testing.T) {
	m, st, rt, ws := finalizeManager(nil)
	seedTerminal(st, "mer-1", domain.SessionMetadata{WorkspacePath: "/ws/mer-1", RuntimeHandleID: "h1"})

	if err := m.FinalizeTerminalSession(ctx, "mer-1"); err != nil {
		t.Fatalf("finalize 1: %v", err)
	}
	if err := m.FinalizeTerminalSession(ctx, "mer-1"); err != nil {
		t.Fatalf("finalize 2: %v", err)
	}
	// Second run is a no-op: facts already record `removed` for this generation.
	if rt.destroyed != 1 || ws.destroyed != 1 {
		t.Fatalf("re-run should be a no-op: runtime=%d workspace=%d, want 1/1", rt.destroyed, ws.destroyed)
	}
}

func TestFinalize_NotTerminalIsNoop(t *testing.T) {
	m, st, rt, ws := finalizeManager(nil)
	st.sessions["mer-1"] = mkLive("mer-1") // not terminated

	if err := m.FinalizeTerminalSession(ctx, "mer-1"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if rt.destroyed != 0 || ws.destroyed != 0 || len(st.cleanup) != 0 {
		t.Fatal("finalize must not touch a live (non-terminal) session")
	}
}

func TestFinalize_RestorePendingSessionIsSkipped(t *testing.T) {
	m, st, rt, ws := finalizeManager(nil)
	seedTerminal(st, "mer-1", domain.SessionMetadata{WorkspacePath: "/ws/mer-1", RuntimeHandleID: "h1"})
	// A "removed"-state row is a shutdown-restore marker: RestoreAll owns it.
	st.worktrees["mer-1"] = []domain.SessionWorktreeRecord{
		{SessionID: "mer-1", RepoName: domain.RootWorkspaceRepoName, State: "removed", WorktreePath: "/ws/mer-1"},
	}

	if err := m.FinalizeTerminalSession(ctx, "mer-1"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if rt.destroyed != 0 || ws.destroyed != 0 || len(st.cleanup) != 0 {
		t.Fatal("finalize must never release a restore-pending session")
	}
}

func TestFinalize_NoWorkspaceIsNotApplicable(t *testing.T) {
	m, st, _, _ := finalizeManager(nil)
	// Terminal session with no workspace and no handle (spawn-failed / orchestrator).
	st.sessions["mer-1"] = domain.SessionRecord{ID: "mer-1", ProjectID: "mer", IsTerminated: true}

	if err := m.FinalizeTerminalSession(ctx, "mer-1"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	facts, ok := st.cleanup["mer-1"]
	if !ok || facts.WorkspaceDisposition != domain.DispositionNotApplicable {
		t.Fatalf("facts = %+v, want a not_applicable row so the boot scan won't re-enqueue it forever", facts)
	}
}

// TestFinalize_GenerationGuardDropsStaleResult pins critique #17: a finalize
// begun under generation N must not persist after a Restore→Terminate cycle has
// advanced the counter to N+1.
func TestFinalize_GenerationGuardDropsStaleResult(t *testing.T) {
	m, st, rt, _ := finalizeManager(nil)
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", IsTerminated: true, CleanupGeneration: 1,
		Metadata: domain.SessionMetadata{WorkspacePath: "/ws/mer-1", RuntimeHandleID: "h1"},
	}
	// Simulate a Restore→Terminate cycle advancing the generation while the
	// release core runs (during runtime Destroy).
	rt.destroyHook = func() {
		rec := st.sessions["mer-1"]
		rec.CleanupGeneration = 2
		st.sessions["mer-1"] = rec
	}

	if err := m.FinalizeTerminalSession(ctx, "mer-1"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if _, ok := st.cleanup["mer-1"]; ok {
		t.Fatal("a stale-generation finalize result must not be persisted")
	}
}

// TestFinalize_ReviewerTerminateFailsDefersRemoval pins critique #24 / M1: a
// worktree whose reviewer pane cannot be confirmed torn down is never removed;
// the workspace becomes retryable.
func TestFinalize_ReviewerTerminateFailsDefersRemoval(t *testing.T) {
	rv := &fakeReviewer{terminateErr: errors.New("reviewer pane still live")}
	m, st, _, ws := finalizeManager(rv)
	seedTerminal(st, "mer-1", domain.SessionMetadata{WorkspacePath: "/ws/mer-1", RuntimeHandleID: "h1"})

	if err := m.FinalizeTerminalSession(ctx, "mer-1"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if rv.terminateCalls != 1 {
		t.Fatalf("reviewer terminate calls = %d, want 1", rv.terminateCalls)
	}
	if ws.destroyed != 0 {
		t.Fatal("worktree must not be removed while the reviewer pane can't be confirmed torn down")
	}
	facts := st.cleanup["mer-1"]
	if facts.WorkspaceDisposition != domain.DispositionPending || facts.FailureCode != failReviewerTerminator {
		t.Fatalf("facts = %+v, want pending/reviewer_terminator so it retries", facts)
	}
}

func TestFinalize_ReviewerTerminatedThenWorktreeRemoved(t *testing.T) {
	rv := &fakeReviewer{} // TerminateReviewer succeeds: pane confirmed gone
	m, st, _, ws := finalizeManager(rv)
	seedTerminal(st, "mer-1", domain.SessionMetadata{WorkspacePath: "/ws/mer-1", RuntimeHandleID: "h1"})

	if err := m.FinalizeTerminalSession(ctx, "mer-1"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if rv.terminateCalls != 1 || ws.destroyed != 1 {
		t.Fatalf("terminate=%d workspace destroyed=%d, want 1/1", rv.terminateCalls, ws.destroyed)
	}
	if st.cleanup["mer-1"].WorkspaceDisposition != domain.DispositionRemoved {
		t.Fatalf("disposition = %q, want removed", st.cleanup["mer-1"].WorkspaceDisposition)
	}
}

// TestFinalize_WorkspaceProjectMixedCleanDirty pins critique #16 / M2: a dirty
// child no longer strands its clean siblings — clean children are removed, the
// dirty one is preserved, and the session rolls up to preserved_dirty.
func TestFinalize_WorkspaceProjectMixedCleanDirty(t *testing.T) {
	m, st, _, ws := finalizeManager(nil)
	ws.destroyErrByRepo = map[string]error{
		"web": fmt.Errorf("dirty: %w", ports.ErrWorkspaceDirty),
	}
	st.projects["mer"] = domain.ProjectRecord{ID: "mer", Path: "/repo/mer", Kind: domain.ProjectKindWorkspace, Config: testRoleAgents()}
	st.workspaceRepo["mer"] = []domain.WorkspaceRepoRecord{
		{Name: "api", RelativePath: "api"},
		{Name: "web", RelativePath: "web"},
	}
	seedTerminal(st, "mer-1", domain.SessionMetadata{WorkspacePath: "/ws/mer-1", Branch: "ao/mer-1", RuntimeHandleID: "h1"})
	st.worktrees["mer-1"] = []domain.SessionWorktreeRecord{
		{SessionID: "mer-1", RepoName: domain.RootWorkspaceRepoName, Branch: "ao/mer-1", WorktreePath: "/ws/mer-1", State: "active"},
		{SessionID: "mer-1", RepoName: "api", Branch: "ao/mer-1", WorktreePath: "/ws/mer-1/api", State: "active"},
		{SessionID: "mer-1", RepoName: "web", Branch: "ao/mer-1", WorktreePath: "/ws/mer-1/web", State: "active"},
	}

	if err := m.FinalizeTerminalSession(ctx, "mer-1"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	states := map[string]string{}
	for _, row := range st.worktrees["mer-1"] {
		states[row.RepoName] = row.State
	}
	if states["api"] != "unavailable" || states[domain.RootWorkspaceRepoName] != "unavailable" {
		t.Fatalf("clean children not reclaimed: states = %v", states)
	}
	if states["web"] != "active" {
		t.Fatalf("dirty child web state = %q, want preserved (unchanged)", states["web"])
	}
	if st.cleanup["mer-1"].WorkspaceDisposition != domain.DispositionPreservedDirty {
		t.Fatalf("rollup = %q, want preserved_dirty", st.cleanup["mer-1"].WorkspaceDisposition)
	}
}

// TestFinalize_WorkspaceProjectDirtyPlusFailedSiblingStaysRetryable pins review
// gap #2 on #2931: when one child is dirty (preserved) and another, non-dirty
// child's teardown *fails*, the session must not roll up to the terminal
// preserved_dirty disposition — that would silently swallow the failed
// child's error and stop it from ever being retried. The failed child's
// retry_remove marker must get another attempt on a later sweep.
func TestFinalize_WorkspaceProjectDirtyPlusFailedSiblingStaysRetryable(t *testing.T) {
	m, st, _, ws := finalizeManager(nil)
	ws.destroyErrByRepo = map[string]error{
		"web":    fmt.Errorf("dirty: %w", ports.ErrWorkspaceDirty),
		"broken": errors.New("locked"),
	}
	st.projects["mer"] = domain.ProjectRecord{ID: "mer", Path: "/repo/mer", Kind: domain.ProjectKindWorkspace, Config: testRoleAgents()}
	st.workspaceRepo["mer"] = []domain.WorkspaceRepoRecord{
		{Name: "api", RelativePath: "api"},
		{Name: "web", RelativePath: "web"},
		{Name: "broken", RelativePath: "broken"},
	}
	seedTerminal(st, "mer-1", domain.SessionMetadata{WorkspacePath: "/ws/mer-1", Branch: "ao/mer-1", RuntimeHandleID: "h1"})
	st.worktrees["mer-1"] = []domain.SessionWorktreeRecord{
		{SessionID: "mer-1", RepoName: domain.RootWorkspaceRepoName, Branch: "ao/mer-1", WorktreePath: "/ws/mer-1", State: "active"},
		{SessionID: "mer-1", RepoName: "api", Branch: "ao/mer-1", WorktreePath: "/ws/mer-1/api", State: "active"},
		{SessionID: "mer-1", RepoName: "web", Branch: "ao/mer-1", WorktreePath: "/ws/mer-1/web", State: "active"},
		{SessionID: "mer-1", RepoName: "broken", Branch: "ao/mer-1", WorktreePath: "/ws/mer-1/broken", State: "active"},
	}

	if err := m.FinalizeTerminalSession(ctx, "mer-1"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	states := map[string]string{}
	for _, row := range st.worktrees["mer-1"] {
		states[row.RepoName] = row.State
	}
	if states["api"] != "unavailable" || states[domain.RootWorkspaceRepoName] != "unavailable" {
		t.Fatalf("clean children not reclaimed: states = %v", states)
	}
	if states["web"] != "active" {
		t.Fatalf("dirty child web state = %q, want preserved (unchanged)", states["web"])
	}
	if states["broken"] != "retry_remove" {
		t.Fatalf("failed child broken state = %q, want retry_remove", states["broken"])
	}
	facts := st.cleanup["mer-1"]
	if facts.WorkspaceDisposition != domain.DispositionPending {
		t.Fatalf("rollup = %q, want pending so the failed child is retried (a dirty sibling must not mask it as terminal preserved_dirty)", facts.WorkspaceDisposition)
	}
	if facts.NextAttemptAt.IsZero() {
		t.Fatal("a pending mixed dirty+failed rollup must schedule a retry")
	}
}

// overlapDetector returns an opHook that flags if two disk-touching workspace
// ops are ever in flight at once for the same session, plus a reader for the flag.
func overlapDetector() (hook func(string), overlapped func() bool) {
	var mu sync.Mutex
	inCS := 0
	over := false
	hook = func(string) {
		mu.Lock()
		inCS++
		if inCS > 1 {
			over = true
		}
		mu.Unlock()
		time.Sleep(10 * time.Millisecond) // widen the window so a missing lock overlaps
		mu.Lock()
		inCS--
		mu.Unlock()
	}
	overlapped = func() bool { mu.Lock(); defer mu.Unlock(); return over }
	return hook, overlapped
}

// TestFinalize_SerializedWithRestore pins the gating review item: the per-session
// lock must make a reconciler finalize and a concurrent RestoreWithMode mutually
// exclusive, so a finalize can't remove the worktree of a session being
// relaunched. Without the lock in RestoreWithMode, the workspace Destroy and
// Restore overlap; -race would also flag the resulting store races.
func TestFinalize_SerializedWithRestore(t *testing.T) {
	for i := 0; i < 20; i++ {
		m, st, _, ws := finalizeManager(nil)
		st.projects["mer"] = domain.ProjectRecord{ID: "mer", Path: "/repo/mer", Config: testRoleAgents()}
		st.sessions["mer-1"] = domain.SessionRecord{
			ID: "mer-1", ProjectID: "mer", IsTerminated: true, CleanupGeneration: 1,
			Metadata: domain.SessionMetadata{WorkspacePath: "/ws/mer-1", Branch: "ao/mer-1", RuntimeHandleID: "h1"},
		}
		hook, overlapped := overlapDetector()
		ws.opHook = hook

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = m.FinalizeTerminalSession(ctx, "mer-1") }()
		go func() { defer wg.Done(); _, _ = m.RestoreWithMode(ctx, "mer-1") }()
		wg.Wait()

		if overlapped() {
			t.Fatalf("iteration %d: finalize and restore overlapped — per-session lock not serializing them", i)
		}
	}
}

// TestKill_SerializedWithFinalize pins that a user Kill and a reconciler finalize
// over the same session never tear down its workspace concurrently.
func TestKill_SerializedWithFinalize(t *testing.T) {
	for i := 0; i < 20; i++ {
		m, st, _, ws := finalizeManager(nil)
		st.sessions["mer-1"] = domain.SessionRecord{
			ID: "mer-1", ProjectID: "mer", IsTerminated: true, CleanupGeneration: 1,
			Metadata: domain.SessionMetadata{WorkspacePath: "/ws/mer-1", Branch: "ao/mer-1", RuntimeHandleID: "h1"},
		}
		hook, overlapped := overlapDetector()
		ws.opHook = hook

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = m.Kill(ctx, "mer-1") }()
		go func() { defer wg.Done(); _ = m.FinalizeTerminalSession(ctx, "mer-1") }()
		wg.Wait()

		if overlapped() {
			t.Fatalf("iteration %d: kill and finalize overlapped — per-session lock not serializing them", i)
		}
	}
}

// TestKill_WritesCleanupFacts pins critique #18: every terminal path (here Kill)
// persists a facts row so the sessions-driven boot scan doesn't re-enqueue it
// forever.
func TestKill_WritesCleanupFacts(t *testing.T) {
	m, st, _, _ := finalizeManager(nil)
	st.sessions["mer-1"] = mkLive("mer-1")

	if _, err := m.Kill(ctx, "mer-1"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	facts, ok := st.cleanup["mer-1"]
	if !ok || facts.WorkspaceDisposition != domain.DispositionRemoved {
		t.Fatalf("kill facts = %+v (ok=%v), want a removed row", facts, ok)
	}
}
