package workflow

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ---- store wrapper for at-least-once test ----

type atLeastOnceStore struct {
	Store
	recordPromptFail bool // when true, RecordSessionLatestUserPrompt returns (false, nil) without persisting
	recordCalls      int
}

func (s *atLeastOnceStore) RecordSessionLatestUserPrompt(ctx context.Context, id domain.SessionID, prompt string, updatedAt time.Time) (bool, error) {
	s.recordCalls++
	if s.recordPromptFail {
		return false, nil // simulate persistence failure without error
	}
	return s.Store.RecordSessionLatestUserPrompt(ctx, id, prompt, updatedAt)
}

// ---- helper: create retry run in a specific partially-completed state ----

// crashReadyRetry sets up a rejected-review state and creates a PENDING retry run.
// Returns the retry run, previous run, session, review, and task.
func crashReadyRetry(t *testing.T, svc *Service, project *domain.ProjectRecord) (retry domain.TaskRun, prevRun domain.TaskRun, session domain.SessionID, review domain.RunReview, task domain.DevelopmentTask) {
	t.Helper()
	ctx := context.Background()
	st := rejectedReviewReady(t, svc, project)

	retry, err := svc.CreateRetryRun(ctx, CreateRetryRunInput{PreviousRunID: st.run.ID})
	if err != nil {
		t.Fatalf("CreateRetryRun: %v", err)
	}
	return retry, st.run, st.session, st.review, st.task
}

// ---- Crash A: Restore成功、Bind前crash ----
// State: retry Run SessionID="" (unbound), previousRun.SessionID=S1, Session S1 live.
// Re-StartRun: no Restore needed (session live), bind S1, send correction prompt, RUNNING.

func TestCrashA_RestoreSucceeded_BeforeBind(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	retry, prevRun, sid, _, _ := crashReadyRetry(t, svc, project)

	// Session S1 is terminated from the previous run's reconcile.
	// Make it live again (simulating that Restore was already done).
	ws := svc.store.(widerStore)
	ws.UpdateSession(ctx, domain.SessionRecord{
		ID:        sid,
		Kind:      domain.SessionKind("worker"),
		ProjectID: domain.ProjectID(project.ID),
		Harness:   "claude-code",
		Activity:  domain.Activity{State: domain.ActivityIdle},
	})

	var restoreCalls int32
	var sendCalls int32
	svc.runtime = &mockRuntime{
		restoreSession: func(_ context.Context, id domain.SessionID) (domain.SessionRecord, error) {
			atomic.AddInt32(&restoreCalls, 1)
			return domain.SessionRecord{ID: id}, nil
		},
		sendSession: func(_ context.Context, id domain.SessionID, msg string) error {
			atomic.AddInt32(&sendCalls, 1)
			return nil
		},
	}

	started, err := svc.StartRun(ctx, retry.ID)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	if atomic.LoadInt32(&restoreCalls) != 0 {
		t.Errorf("expected 0 Restore calls (session live), got %d", restoreCalls)
	}
	if started.SessionID != sid {
		t.Errorf("expected session bound to %s, got %s", sid, started.SessionID)
	}
	if atomic.LoadInt32(&sendCalls) != 1 {
		t.Errorf("expected 1 Send call, got %d", sendCalls)
	}
	if started.Status != domain.RunStatusRunning {
		t.Errorf("expected RUNNING, got %s", started.Status)
	}
	task, _ := svc.GetTask(ctx, prevRun.TaskID)
	if task.Status != domain.TaskStatusRunning {
		t.Errorf("task: expected RUNNING, got %s", task.Status)
	}
}

// ---- Crash B: Bind成功、Send前crash ----
// State: retry Run SessionID=S1 (bound), Status=PENDING, Session live, LatestUserPrompt mismatch.
// Re-StartRun: no Restore, no Bind, Send once, then RUNNING.

func TestCrashB_BindSucceeded_BeforeSend(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	retry, _, sid, _, _ := crashReadyRetry(t, svc, project)
	ctx2 := context.Background()

	// Simulate: Bind was already done (SessionID=S1 in DB), but Send didn't happen.
	// We need to manually set the run's SessionID in the store to simulate the bound state.
	// Use BindTaskRunSession to bind it.
	ws := svc.store.(widerStore)
	svc.store.BindTaskRunSession(ctx, retry.ID, sid)

	// Make session live.
	ws.UpdateSession(ctx2, domain.SessionRecord{
		ID:        sid,
		Kind:      domain.SessionKind("worker"),
		ProjectID: domain.ProjectID(project.ID),
		Harness:   "claude-code",
		Activity:  domain.Activity{State: domain.ActivityIdle},
		Metadata:  domain.SessionMetadata{LatestUserPrompt: "some-old-prompt"},
	})

	var sendCalls int32
	svc.runtime = &mockRuntime{
		sendSession: func(_ context.Context, id domain.SessionID, msg string) error {
			atomic.AddInt32(&sendCalls, 1)
			return nil
		},
	}

	started, err := svc.StartRun(ctx, retry.ID)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	if atomic.LoadInt32(&sendCalls) != 1 {
		t.Errorf("expected 1 Send call (dedup mismatch), got %d", sendCalls)
	}
	if started.Status != domain.RunStatusRunning {
		t.Errorf("expected RUNNING, got %s", started.Status)
	}
}

// ---- Crash C: Send成功、状态更新前crash ----
// State: retry Run SessionID=S1 (bound), Status=PENDING, Session live, LatestUserPrompt matches.
// Re-StartRun: no Restore, no Bind, no Send, just update Run→RUNNING, Task→RUNNING.

func TestCrashC_SendSucceeded_BeforeStatusUpdate(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	retry, prevRun, sid, review, _ := crashReadyRetry(t, svc, project)
	ctx2 := context.Background()

	// Bind the run.
	svc.store.BindTaskRunSession(ctx, retry.ID, sid)

	// Compute the expected canonicalized correction prompt.
	correctionPrompt := buildCorrectionPrompt(retry.ID, prevRun.ID, review.ID, review.Issues)
	expectedDedup := CanonicalizePrompt(correctionPrompt)

	// Make session live with matching LatestUserPrompt (Send already succeeded).
	ws := svc.store.(widerStore)
	ws.UpdateSession(ctx2, domain.SessionRecord{
		ID:        sid,
		Kind:      domain.SessionKind("worker"),
		ProjectID: domain.ProjectID(project.ID),
		Harness:   "claude-code",
		Activity:  domain.Activity{State: domain.ActivityIdle},
		Metadata:  domain.SessionMetadata{LatestUserPrompt: expectedDedup},
	})

	var restoreCalls, resumeCalls, sendCalls int32
	svc.runtime = &mockRuntime{
		restoreSession: func(_ context.Context, id domain.SessionID) (domain.SessionRecord, error) {
			atomic.AddInt32(&restoreCalls, 1)
			return domain.SessionRecord{ID: id}, nil
		},
		resumeAgentSession: func(_ context.Context, id domain.SessionID) (domain.SessionRecord, error) {
			atomic.AddInt32(&resumeCalls, 1)
			return domain.SessionRecord{ID: id}, nil
		},
		sendSession: func(_ context.Context, id domain.SessionID, msg string) error {
			atomic.AddInt32(&sendCalls, 1)
			return nil
		},
	}

	started, err := svc.StartRun(ctx, retry.ID)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	if atomic.LoadInt32(&restoreCalls) != 0 {
		t.Errorf("expected 0 Restore calls, got %d", restoreCalls)
	}
	if atomic.LoadInt32(&resumeCalls) != 0 {
		t.Errorf("expected 0 ResumeAgent calls, got %d", resumeCalls)
	}
	if atomic.LoadInt32(&sendCalls) != 0 {
		t.Errorf("expected 0 Send calls (dedup match), got %d", sendCalls)
	}
	if started.Status != domain.RunStatusRunning {
		t.Errorf("expected RUNNING, got %s", started.Status)
	}
	task, _ := svc.GetTask(ctx, prevRun.TaskID)
	if task.Status != domain.TaskStatusRunning {
		t.Errorf("task: expected RUNNING, got %s", task.Status)
	}
}

// ---- Crash E: 已Bind但重启后Session terminated ----
// State: retry Run SessionID=S1, Session IsTerminated=true.
// Re-StartRun: RestoreSession(S1), no re-Bind, dedup decides Send.

func TestCrashE_BoundButSessionTerminated(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	retry, _, sid, _, _ := crashReadyRetry(t, svc, project)

	// Bind the run.
	svc.store.BindTaskRunSession(ctx, retry.ID, sid)

	// Session is terminated (from previous run's reconcile — not re-activated).
	// Don't change the session state — it's already terminated from the rejectedReviewReady flow.

	var restoreCalls int32
	var sendCalls int32
	svc.runtime = &mockRuntime{
		restoreSession: func(_ context.Context, id domain.SessionID) (domain.SessionRecord, error) {
			atomic.AddInt32(&restoreCalls, 1)
			return domain.SessionRecord{ID: id, Kind: domain.SessionKind("worker"), Harness: "claude-code"}, nil
		},
		sendSession: func(_ context.Context, id domain.SessionID, msg string) error {
			atomic.AddInt32(&sendCalls, 1)
			return nil
		},
	}

	started, err := svc.StartRun(ctx, retry.ID)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	if atomic.LoadInt32(&restoreCalls) != 1 {
		t.Errorf("expected 1 Restore call, got %d", restoreCalls)
	}
	if atomic.LoadInt32(&sendCalls) != 1 {
		t.Errorf("expected 1 Send call (dedup mismatch), got %d", sendCalls)
	}
	if started.Status != domain.RunStatusRunning {
		t.Errorf("expected RUNNING, got %s", started.Status)
	}
}

// ---- Crash F: 已Bind, Session nonterminated, Agent exited ----
// State: retry Run SessionID=S1, Session not terminated, Activity=Exited.
// Re-StartRun: ResumeAgentSession(S1), no Restore, no re-Bind, dedup decides Send.

func TestCrashF_BoundButAgentExited(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	retry, _, sid, _, _ := crashReadyRetry(t, svc, project)
	ctx2 := context.Background()

	// Bind the run.
	svc.store.BindTaskRunSession(ctx, retry.ID, sid)

	// Make session non-terminated but agent exited.
	ws := svc.store.(widerStore)
	ws.UpdateSession(ctx2, domain.SessionRecord{
		ID:        sid,
		Kind:      domain.SessionKind("worker"),
		ProjectID: domain.ProjectID(project.ID),
		Harness:   "claude-code",
		Activity:  domain.Activity{State: domain.ActivityExited},
	})

	var restoreCalls, resumeCalls, sendCalls int32
	svc.runtime = &mockRuntime{
		restoreSession: func(_ context.Context, id domain.SessionID) (domain.SessionRecord, error) {
			atomic.AddInt32(&restoreCalls, 1)
			return domain.SessionRecord{ID: id}, nil
		},
		resumeAgentSession: func(_ context.Context, id domain.SessionID) (domain.SessionRecord, error) {
			atomic.AddInt32(&resumeCalls, 1)
			return domain.SessionRecord{ID: id, Kind: domain.SessionKind("worker"), Harness: "claude-code"}, nil
		},
		sendSession: func(_ context.Context, id domain.SessionID, msg string) error {
			atomic.AddInt32(&sendCalls, 1)
			return nil
		},
	}

	started, err := svc.StartRun(ctx, retry.ID)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	if atomic.LoadInt32(&restoreCalls) != 0 {
		t.Errorf("expected 0 Restore calls (session not terminated), got %d", restoreCalls)
	}
	if atomic.LoadInt32(&resumeCalls) != 1 {
		t.Errorf("expected 1 ResumeAgent call, got %d", resumeCalls)
	}
	if atomic.LoadInt32(&sendCalls) != 1 {
		t.Errorf("expected 1 Send call, got %d", sendCalls)
	}
	if started.Status != domain.RunStatusRunning {
		t.Errorf("expected RUNNING, got %s", started.Status)
	}
}

// ---- At-least-once semantics ----
// Scenario: Send succeeds, but RecordSessionLatestUserPrompt fails (returns false).
// On next attempt: LatestUserPrompt still mismatched → Send again.
// This is the accepted at-least-once boundary.

func TestAtLeastOnce_SendSucceedsButPromptNotPersisted(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	retry, _, sid, _, _ := crashReadyRetry(t, svc, project)
	ctx2 := context.Background()

	// Session is terminated from rejectedReviewReady. Make it live.
	ws := svc.store.(widerStore)
	ws.UpdateSession(ctx2, domain.SessionRecord{
		ID:        sid,
		Kind:      domain.SessionKind("worker"),
		ProjectID: domain.ProjectID(project.ID),
		Harness:   "claude-code",
		Activity:  domain.Activity{State: domain.ActivityIdle},
		Metadata:  domain.SessionMetadata{LatestUserPrompt: ""},
	})

	var sendCalls int32
	svc.runtime = &mockRuntime{
		restoreSession: func(_ context.Context, id domain.SessionID) (domain.SessionRecord, error) {
			return domain.SessionRecord{ID: id, Kind: domain.SessionKind("worker"), Harness: "claude-code"}, nil
		},
		sendSession: func(_ context.Context, id domain.SessionID, msg string) error {
			atomic.AddInt32(&sendCalls, 1)
			return nil
		},
	}

	// Start the retry — Send succeeds, but we simulate RecordSessionLatestUserPrompt
	// returning false (persistence failure). We achieve this by wrapping the store.
	origStore := svc.store
	wrapper := &atLeastOnceStore{Store: origStore, recordPromptFail: true}
	svc.store = wrapper

	started, err := svc.StartRun(ctx, retry.ID)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if started.Status != domain.RunStatusRunning {
		t.Fatalf("expected RUNNING, got %s", started.Status)
	}

	// Simulate daemon crash: reset run to PENDING, keep session live.
	// The key insight: LatestUserPrompt was NOT updated (wrapper returned false),
	// so on re-StartRun, dedup will see a mismatch → Send again.

	// Restore original store for re-StartRun (simulating daemon restart).
	svc.store = origStore

	// Reset run to PENDING to simulate incomplete status update.
	svc.store.UpdateTaskRunStatus(ctx, retry.ID, domain.RunStatusPending, "", "", nil, nil)

	// Re-trigger StartRun (simulating startup reconcile).
	started2, err := svc.StartRun(ctx, retry.ID)
	if err != nil {
		t.Fatalf("Second StartRun: %v", err)
	}

	if atomic.LoadInt32(&sendCalls) != 2 {
		t.Errorf("expected 2 Send calls (at-least-once), got %d", sendCalls)
	}
	if started2.Status != domain.RunStatusRunning {
		t.Errorf("expected RUNNING, got %s", started2.Status)
	}
	_ = wrapper
}

// ---- Consistency: transient convergence failure recovery ----

func TestConsistencyTransientFailure_Succeeded_RecoversNextCycle(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	ws := svc.store.(widerStore)
	sid := createTestSession(t, ws, domain.ProjectID(project.ID))
	svc.runtime = &mockRuntime{
		spawnSession: func(_ context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, error) {
			return domain.SessionRecord{ID: sid, Kind: domain.SessionKind("worker"), Harness: "claude-code"}, nil
		},
	}
	run1, _ := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	svc.StartRun(ctx, run1.ID)
	markSessionTerminated(t, ws, sid, domain.ProjectID(project.ID), "session-end")
	svc.ReconcileRunningRuns(ctx)
	// convergence: SUCCEEDED → REVIEW

	// Simulate: convergence update failed, task stuck at RUNNING.
	svc.store.UpdateDevelopmentTaskStatus(ctx, task.ID, domain.TaskStatusRunning, nil, nil)

	// Cycle 1: consistency reconcile should recover.
	n, err := svc.ReconcileWorkflowStateConsistency(ctx)
	if err != nil {
		t.Fatalf("ReconcileWorkflowStateConsistency: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 compensated, got %d", n)
	}
	task, _ = svc.GetTask(ctx, task.ID)
	if task.Status != domain.TaskStatusReview {
		t.Errorf("expected REVIEW, got %s", task.Status)
	}
}

func TestConsistencyTransientFailure_Failed_RecoversNextCycle(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	ws := svc.store.(widerStore)
	sid := createTestSession(t, ws, domain.ProjectID(project.ID))
	svc.runtime = &mockRuntime{
		spawnSession: func(_ context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, error) {
			return domain.SessionRecord{ID: sid, Kind: domain.SessionKind("worker"), Harness: "claude-code"}, nil
		},
	}
	run1, _ := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	svc.StartRun(ctx, run1.ID)
	markSessionTerminated(t, ws, sid, domain.ProjectID(project.ID), "process-exited")
	svc.ReconcileRunningRuns(ctx)
	// convergence: FAILED → READY

	// Task should be READY now. But let's simulate the failure case:
	// the run is FAILED, but task was stuck at RUNNING (convergence failed).
	svc.store.UpdateDevelopmentTaskStatus(ctx, task.ID, domain.TaskStatusRunning, nil, nil)

	n, err := svc.ReconcileWorkflowStateConsistency(ctx)
	if err != nil {
		t.Fatalf("ReconcileWorkflowStateConsistency: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 compensated, got %d", n)
	}
	task, _ = svc.GetTask(ctx, task.ID)
	if task.Status != domain.TaskStatusReady {
		t.Errorf("expected READY, got %s", task.Status)
	}
}

// ---- Latest-attempt protection: Run1 SUCCEEDED + Run2 RUNNING → NO-OP ----

func TestLatestAttemptProtection_HistoricalRunDoesNotDriveTask(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	ws := svc.store.(widerStore)

	// Run1 SUCCEEDED.
	sid1 := createTestSession(t, ws, domain.ProjectID(project.ID))
	svc.runtime = &mockRuntime{
		spawnSession: func(_ context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, error) {
			return domain.SessionRecord{ID: sid1, Kind: domain.SessionKind("worker"), Harness: "claude-code"}, nil
		},
	}
	run1, _ := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	svc.StartRun(ctx, run1.ID)
	markSessionTerminated(t, ws, sid1, domain.ProjectID(project.ID), "session-end")
	svc.ReconcileRunningRuns(ctx)
	// convergence: SUCCEEDED → REVIEW

	// Reject task → READY.
	svc.RejectTask(ctx, task.ID)

	// Run2 RUNNING.
	sid2 := createTestSession(t, ws, domain.ProjectID(project.ID))
	svc.runtime = &mockRuntime{
		spawnSession: func(_ context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, error) {
			return domain.SessionRecord{ID: sid2, Kind: domain.SessionKind("worker"), Harness: "claude-code"}, nil
		},
	}
	run2, _ := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	svc.StartRun(ctx, run2.ID)
	// Task is RUNNING, Run2 is RUNNING, Run1 is SUCCEEDED.

	// Force task to RUNNING (it already is from StartRun).
	// Now: Run1=SUCCEEDED, Run2=RUNNING, Task=RUNNING.
	// Consistency should NOT compensate (latest run is RUNNING).
	n, err := svc.ReconcileWorkflowStateConsistency(ctx)
	if err != nil {
		t.Fatalf("ReconcileWorkflowStateConsistency: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 compensated (latest run is RUNNING), got %d", n)
	}
	task, _ = svc.GetTask(ctx, task.ID)
	if task.Status != domain.TaskStatusRunning {
		t.Errorf("task should remain RUNNING, got %s", task.Status)
	}
}

// ---- Observer wiring: StartCompletionObserver calls both reconcile + consistency ----

func TestObserverWiring_CallsBothReconcileAndConsistency(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	ws := svc.store.(widerStore)
	sid := createTestSession(t, ws, domain.ProjectID(project.ID))
	svc.runtime = &mockRuntime{
		spawnSession: func(_ context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, error) {
			return domain.SessionRecord{ID: sid, Kind: domain.SessionKind("worker"), Harness: "claude-code"}, nil
		},
	}
	run, _ := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	svc.StartRun(ctx, run.ID)
	markSessionTerminated(t, ws, sid, domain.ProjectID(project.ID), "session-end")

	// Force task to RUNNING to test consistency (simulating convergence failure).
	svc.store.UpdateDevelopmentTaskStatus(ctx, task.ID, domain.TaskStatusRunning, nil, nil)

	// Start observer with a very short interval.
	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()
	svc.StartCompletionObserver(ctx2, 50*time.Millisecond)

	// Wait long enough for at least one observer tick to fire.
	time.Sleep(200 * time.Millisecond)

	// Verify: run finalized and task compensated.
	fetchedRun, _ := svc.GetRun(ctx, run.ID)
	fetchedTask, _ := svc.GetTask(ctx, task.ID)

	if fetchedRun.Status != domain.RunStatusSucceeded {
		t.Errorf("expected run SUCCEEDED, got %s", fetchedRun.Status)
	}
	if fetchedTask.Status != domain.TaskStatusReview {
		t.Errorf("expected task REVIEW (both reconcile + consistency ran), got %s", fetchedTask.Status)
	}
}

// ---- TestCanonicalizePrompt_DelegatesToTextutil ----

func TestCanonicalizePrompt_DelegatesToTextutil(t *testing.T) {
	input := "  hello world  "
	got := CanonicalizePrompt(input)
	want := "hello world"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}

	// Verify it handles truncation the same way.
	big := strings.Repeat("x", 20<<10)
	got = CanonicalizePrompt(big)
	if len(got) != 16<<10 {
		t.Errorf("expected %d bytes, got %d", 16<<10, len(got))
	}
}
