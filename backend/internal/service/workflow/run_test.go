package workflow

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ---- Mock SessionRuntime ----

type mockRuntime struct {
	spawnSession        func(ctx context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, error)
	killSession         func(ctx context.Context, id domain.SessionID) (bool, error)
	restoreSession      func(ctx context.Context, id domain.SessionID) (domain.SessionRecord, error)
	resumeAgentSession  func(ctx context.Context, id domain.SessionID) (domain.SessionRecord, error)
	sendSession         func(ctx context.Context, id domain.SessionID, message string) error
}

func (m *mockRuntime) SpawnSession(ctx context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, error) {
	if m.spawnSession != nil {
		return m.spawnSession(ctx, cfg)
	}
	return domain.SessionRecord{
		ID:       domain.SessionID("session-1"),
		Harness:  "claude-code",
		Metadata: domain.SessionMetadata{},
	}, nil
}

func (m *mockRuntime) KillSession(ctx context.Context, id domain.SessionID) (bool, error) {
	if m.killSession != nil {
		return m.killSession(ctx, id)
	}
	return true, nil
}

func (m *mockRuntime) RestoreSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, error) {
	if m.restoreSession != nil {
		return m.restoreSession(ctx, id)
	}
	return domain.SessionRecord{ID: id}, nil
}

func (m *mockRuntime) ResumeAgentSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, error) {
	if m.resumeAgentSession != nil {
		return m.resumeAgentSession(ctx, id)
	}
	return domain.SessionRecord{ID: id}, nil
}

func (m *mockRuntime) SendSession(ctx context.Context, id domain.SessionID, message string) error {
	if m.sendSession != nil {
		return m.sendSession(ctx, id, message)
	}
	return nil
}

// ---- helpers ----

func readyTaskWithPlan(t *testing.T, svc *Service, project *domain.ProjectRecord) (domain.DevelopmentPlan, domain.DevelopmentStage, domain.DevelopmentTask) {
	t.Helper()
	ctx := context.Background()

	plan, err := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	svc.ConfirmPlan(ctx, plan.ID)

	stage, err := svc.CreateStage(ctx, CreateStageInput{
		PlanID: plan.ID,
		Title:  "Stage 1",
	})
	if err != nil {
		t.Fatalf("CreateStage: %v", err)
	}
	svc.StartPlan(ctx, plan.ID)
	svc.StartStage(ctx, stage.ID)

	task, err := svc.CreateTask(ctx, CreateTaskInput{
		StageID: stage.ID,
		Title:   "Task 1",
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	svc.ReadyTask(ctx, task.ID)

	return plan, stage, task
}

// createTestSession creates a session in the real SQLite store (via
// workflow.Store's wider interface) and returns its auto-generated ID.
// In production, lifecycle.MarkSpawned does this.
type widerStore interface {
	CreateSession(ctx context.Context, rec domain.SessionRecord) (domain.SessionRecord, error)
	UpdateSession(ctx context.Context, rec domain.SessionRecord) error
}

func createTestSession(t *testing.T, store widerStore, projectID domain.ProjectID) domain.SessionID {
	t.Helper()
	rec, err := store.CreateSession(context.Background(), domain.SessionRecord{
		Kind:      domain.SessionKind("worker"),
		ProjectID: projectID,
		Harness:   "claude-code",
	})
	if err != nil {
		t.Fatalf("createTestSession: %v", err)
	}
	return rec.ID
}

func markSessionTerminated(t *testing.T, store widerStore, id domain.SessionID, projectID domain.ProjectID, reason string) {
	t.Helper()
	if err := store.UpdateSession(context.Background(), domain.SessionRecord{
		ID:           id,
		Kind:         domain.SessionKind("worker"),
		ProjectID:    projectID,
		Harness:      "claude-code",
		IsTerminated: true,
		Activity:     domain.Activity{State: domain.ActivityExited},
		Metadata:     domain.SessionMetadata{TerminationReason: reason},
	}); err != nil {
		t.Fatalf("markSessionTerminated: %v", err)
	}
}

// ---- TerminationToRunStatus frozen rule ----

func TestTerminationToRunStatus_FrozenRule(t *testing.T) {
	tests := []struct {
		reason string
		want   domain.TaskRunStatus
	}{
		{"session-end", domain.RunStatusSucceeded},
		{"process-exited", domain.RunStatusFailed},
		{"reaper", domain.RunStatusFailed},
		{"explicit", domain.RunStatusCancelled},
		{"", domain.RunStatusFailed},
		{"unknown", domain.RunStatusFailed},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("reason=%q", tt.reason), func(t *testing.T) {
			got := TerminationToRunStatus(tt.reason)
			if got != tt.want {
				t.Errorf("TerminationToRunStatus(%q) = %s, want %s", tt.reason, got, tt.want)
			}
			if tt.reason == "process-exited" && got == domain.RunStatusSucceeded {
				t.Fatal("FORBIDDEN: process-exited must NEVER map to SUCCEEDED")
			}
		})
	}
}

// ---- P0-2: CreateRun must only allow READY tasks ----

func TestCreateRun_ReadyTask_Succeeds(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	run, err := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	if err != nil {
		t.Fatalf("CreateRun on READY task: %v", err)
	}
	if run.Status != domain.RunStatusPending {
		t.Errorf("expected PENDING, got %s", run.Status)
	}
	if run.Attempt != 1 {
		t.Errorf("expected attempt 1, got %d", run.Attempt)
	}
}

func TestCreateRun_PendingTask_Rejected(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})
	svc.ConfirmPlan(ctx, plan.ID)
	stage, _ := svc.CreateStage(ctx, CreateStageInput{
		PlanID: plan.ID,
		Title:  "Stage 1",
	})
	svc.StartPlan(ctx, plan.ID)
	svc.StartStage(ctx, stage.ID)
	task, _ := svc.CreateTask(ctx, CreateTaskInput{
		StageID: stage.ID,
		Title:   "Pending Task",
	})

	_, err := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	if err != ErrInvalidTransition {
		t.Errorf("expected ErrInvalidTransition for PENDING task, got %v", err)
	}
}

func TestCreateRun_RunningTask_Rejected(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)
	svc.StartTask(ctx, task.ID)

	_, err := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	if err != ErrInvalidTransition {
		t.Errorf("expected ErrInvalidTransition for RUNNING task, got %v", err)
	}
}

func TestCreateRun_ConcurrentRun_Rejected(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	_, err := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	if err != nil {
		t.Fatalf("first CreateRun: %v", err)
	}
	_, err = svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	if err != ErrConflict {
		t.Errorf("expected ErrConflict for concurrent run, got %v", err)
	}
}

// ---- P0-4: CancelRun must maintain runtime truth ----

func TestCancelRun_PendingRun_DirectCancel(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	run, _ := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	cancelled, err := svc.CancelRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("CancelRun on PENDING: %v", err)
	}
	if cancelled.Status != domain.RunStatusCancelled {
		t.Errorf("expected CANCELLED, got %s", cancelled.Status)
	}
}

func TestCancelRun_RunningKillSuccess_Cancelled(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	ws := svc.store.(widerStore)
	sid := createTestSession(t, ws, domain.ProjectID(project.ID))

	svc.runtime = &mockRuntime{
		spawnSession: func(_ context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, error) {
			return domain.SessionRecord{ID: sid, Kind: domain.SessionKind("worker"), Harness: "claude-code"}, nil
		},
		killSession: func(_ context.Context, id domain.SessionID) (bool, error) {
			return true, nil
		},
	}

	run, _ := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	run, err := svc.StartRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	cancelled, err := svc.CancelRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if cancelled.Status != domain.RunStatusCancelled {
		t.Errorf("expected CANCELLED, got %s", cancelled.Status)
	}
}

func TestCancelRun_RunningKillFail_SessionNotTerminated_Error(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	ws := svc.store.(widerStore)
	sid := createTestSession(t, ws, domain.ProjectID(project.ID))

	svc.runtime = &mockRuntime{
		spawnSession: func(_ context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, error) {
			return domain.SessionRecord{ID: sid, Kind: domain.SessionKind("worker"), Harness: "claude-code"}, nil
		},
		killSession: func(_ context.Context, id domain.SessionID) (bool, error) {
			return false, errors.New("connection lost")
		},
	}

	run, _ := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	run, err := svc.StartRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	// Kill fails + session NOT terminated → error, run stays RUNNING
	_, err = svc.CancelRun(ctx, run.ID)
	if err == nil {
		t.Fatal("expected error when Kill fails and session not terminated")
	}

	fetched, _ := svc.GetRun(ctx, run.ID)
	if fetched.Status != domain.RunStatusRunning {
		t.Errorf("expected RUNNING after failed cancel, got %s", fetched.Status)
	}
}

func TestCancelRun_RunningKillFail_SessionAlreadyTerminated_Cancelled(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	ws := svc.store.(widerStore)
	sid := createTestSession(t, ws, domain.ProjectID(project.ID))

	svc.runtime = &mockRuntime{
		spawnSession: func(_ context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, error) {
			return domain.SessionRecord{ID: sid, Kind: domain.SessionKind("worker"), Harness: "claude-code"}, nil
		},
		killSession: func(_ context.Context, id domain.SessionID) (bool, error) {
			return false, errors.New("connection refused")
		},
	}

	run, _ := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	run, err := svc.StartRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	markSessionTerminated(t, ws, sid, domain.ProjectID(project.ID), "session-end")

	cancelled, err := svc.CancelRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if cancelled.Status != domain.RunStatusCancelled {
		t.Errorf("expected CANCELLED, got %s", cancelled.Status)
	}
}

// ---- P0-3: StartRun resolves provider from system default (no task/role explicit) ----

func TestStartRun_ProviderResolution_SystemDefault(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	ws := svc.store.(widerStore)
	sid := createTestSession(t, ws, domain.ProjectID(project.ID))

	var spawnCfg ports.SpawnConfig
	svc.runtime = &mockRuntime{
		spawnSession: func(_ context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, error) {
			spawnCfg = cfg
			return domain.SessionRecord{
				ID:       sid,
				Kind:     domain.SessionKind("worker"),
				Harness:  "claude-code",
				Metadata: domain.SessionMetadata{},
			}, nil
		},
	}

	run, _ := svc.CreateRun(ctx, CreateRunInput{
		TaskID: task.ID,
	})
	_, err := svc.StartRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	// With no task-level and no AgentRole-level provider, both should be empty (system default)
	if spawnCfg.ProviderID != "" {
		t.Errorf("expected empty provider (system default), got %q", spawnCfg.ProviderID)
	}
	if spawnCfg.ProviderModelID != "" {
		t.Errorf("expected empty model (system default), got %q", spawnCfg.ProviderModelID)
	}
}

// ---- Runtime Snapshot ----

func TestStartRun_Snapshot_WritesActualSessionRecord(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	ws := svc.store.(widerStore)
	sid := createTestSession(t, ws, domain.ProjectID(project.ID))

	svc.runtime = &mockRuntime{
		spawnSession: func(_ context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, error) {
			return domain.SessionRecord{
				ID:      sid,
				Kind:    domain.SessionKind("worker"),
				Harness: "claude-code",
				Metadata: domain.SessionMetadata{
					ProviderID:          "anthropic",
					ProviderModelID:     "claude-sonnet-4-20250514",
					ProviderDisplayName: "Anthropic",
					ProviderModelName:   "Claude Sonnet 4",
				},
			}, nil
		},
	}

	run, _ := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	run, err := svc.StartRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if run.ProviderID != "anthropic" {
		t.Errorf("snapshot provider: expected 'anthropic', got %q", run.ProviderID)
	}
	if run.ProviderModelID != "claude-sonnet-4-20250514" {
		t.Errorf("snapshot model: expected 'claude-sonnet-4-20250514', got %q", run.ProviderModelID)
	}
	if run.ProviderDisplayName != "Anthropic" {
		t.Errorf("snapshot display name: expected 'Anthropic', got %q", run.ProviderDisplayName)
	}
	if run.ExecutorType != "claude-code" {
		t.Errorf("snapshot executor: expected 'claude-code', got %q", run.ExecutorType)
	}
}

// ---- StartRun state order ----

func TestStartRun_StateOrder_BindBeforeRunStatus(t *testing.T) {
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
	run, err := svc.StartRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if run.SessionID != sid {
		t.Errorf("session not bound: got %q", run.SessionID)
	}
	if run.Status != domain.RunStatusRunning {
		t.Errorf("expected RUNNING, got %s", run.Status)
	}
	if run.StartedAt == nil {
		t.Error("expected started_at to be set")
	}
}

func TestStartRun_SpawnFail_StaysPending(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	svc.runtime = &mockRuntime{
		spawnSession: func(_ context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, error) {
			return domain.SessionRecord{}, errors.New("spawn failed")
		},
	}

	run, _ := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	_, err := svc.StartRun(ctx, run.ID)
	if err == nil {
		t.Fatal("expected error from failed spawn")
	}

	fetched, _ := svc.GetRun(ctx, run.ID)
	if fetched.Status != domain.RunStatusPending {
		t.Errorf("expected PENDING after spawn fail, got %s", fetched.Status)
	}
}

// ---- ReconcileRunningRuns ----

func TestReconcileRunningRuns_SessionTerminated_FinalizesRun(t *testing.T) {
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

	n, err := svc.ReconcileRunningRuns(ctx)
	if err != nil {
		t.Fatalf("ReconcileRunningRuns: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 reconciled, got %d", n)
	}

	fetched, _ := svc.GetRun(ctx, run.ID)
	if fetched.Status != domain.RunStatusSucceeded {
		t.Errorf("expected SUCCEEDED, got %s", fetched.Status)
	}
}

func TestReconcileRunningRuns_ProcessExited_FinalizesRunAsFailed(t *testing.T) {
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

	markSessionTerminated(t, ws, sid, domain.ProjectID(project.ID), "process-exited")

	n, err := svc.ReconcileRunningRuns(ctx)
	if err != nil {
		t.Fatalf("ReconcileRunningRuns: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 reconciled, got %d", n)
	}

	fetched, _ := svc.GetRun(ctx, run.ID)
	if fetched.Status != domain.RunStatusFailed {
		t.Errorf("expected FAILED, got %s", fetched.Status)
	}
}

// ---- Observer: StartCompletionObserver integration ----

func TestStartCompletionObserver_FiresAndFinalizes(t *testing.T) {
	svc, project := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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

	svc.StartCompletionObserver(ctx, 100*time.Millisecond)

	markSessionTerminated(t, ws, sid, domain.ProjectID(project.ID), "session-end")

	time.Sleep(250 * time.Millisecond)

	fetched, _ := svc.GetRun(ctx, run.ID)
	if fetched.Status != domain.RunStatusSucceeded {
		t.Errorf("expected SUCCEEDED after observer tick, got %s", fetched.Status)
	}

	cancel()
	time.Sleep(50 * time.Millisecond)
}

// ---- Startup Reconcile ----

func TestStartupReconcile_FinalizesOrphanedRun(t *testing.T) {
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

	markSessionTerminated(t, ws, sid, domain.ProjectID(project.ID), "reaper")

	n, err := svc.ReconcileRunningRuns(ctx)
	if err != nil {
		t.Fatalf("startup Reconcile: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 reconciled, got %d", n)
	}

	fetched, _ := svc.GetRun(ctx, run.ID)
	if fetched.Status != domain.RunStatusFailed {
		t.Errorf("expected FAILED for reaper, got %s", fetched.Status)
	}
}

// ---- GetRun / ListRunsByTask ----

func TestGetRun_NotFound(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.GetRun(ctx, domain.TaskRunID("nonexistent"))
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestListRunsByTask_Empty(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	runs, err := svc.ListRunsByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRunsByTask: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("expected 0 runs, got %d", len(runs))
	}
}

func TestCancelRun_TerminalState_Rejected(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, task := readyTaskWithPlan(t, svc, project)

	svc.runtime = &mockRuntime{}

	run, _ := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	svc.CancelRun(ctx, run.ID)

	_, err := svc.CancelRun(ctx, run.ID)
	if err != ErrInvalidTransition {
		t.Errorf("expected ErrInvalidTransition for terminal state, got %v", err)
	}
}

func TestRunAPI_Not501_WhenServiceConfigured(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.GetRun(ctx, domain.TaskRunID("nonexistent"))
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
