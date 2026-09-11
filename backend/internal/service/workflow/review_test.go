package workflow

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ---- helpers ----

// reviewReadyRun brings a task to REVIEW status with a SUCCEEDED run.
func reviewReadyRun(t *testing.T, svc *Service, project *domain.ProjectRecord) (domain.DevelopmentStage, domain.DevelopmentTask, domain.TaskRun) {
	t.Helper()
	ctx := context.Background()
	_, stage, task := readyTaskWithPlan(t, svc, project)

	ws := svc.store.(widerStore)
	sid := createTestSession(t, ws, domain.ProjectID(project.ID))
	svc.runtime = &mockRuntime{
		spawnSession: func(_ context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, error) {
			return domain.SessionRecord{ID: sid, Kind: domain.SessionKind("worker"), Harness: "claude-code"}, nil
		},
	}

	run, err := svc.CreateRun(ctx, CreateRunInput{TaskID: task.ID})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	run, err = svc.StartRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	markSessionTerminated(t, ws, sid, domain.ProjectID(project.ID), "session-end")
	if _, err := svc.ReconcileRunningRuns(ctx); err != nil {
		t.Fatalf("ReconcileRunningRuns: %v", err)
	}
	fetched, _ := svc.GetRun(ctx, run.ID)
	if fetched.Status != domain.RunStatusSucceeded {
		t.Fatalf("expected SUCCEEDED, got %s", fetched.Status)
	}
	// Phase 2.5: task convergence auto-transitions to REVIEW.
	// Verify the task is already in REVIEW status.
	task, _ = svc.GetTask(ctx, task.ID)
	if task.Status != domain.TaskStatusReview {
		t.Fatalf("expected task REVIEW after convergence, got %s", task.Status)
	}
	return stage, task, fetched
}

// seedReview creates a review for a SUCCEEDED run whose task is in REVIEW.
func seedReview(t *testing.T, svc *Service, project *domain.ProjectRecord) (domain.DevelopmentStage, domain.DevelopmentTask, domain.RunReview) {
	t.Helper()
	stage, _, run := reviewReadyRun(t, svc, project)
	ctx := context.Background()
	review, err := svc.CreateRunReview(ctx, run.ID, domain.RunReviewSourceAI, "summary", "")
	if err != nil {
		t.Fatalf("CreateRunReview: %v", err)
	}
	task, _ := svc.GetTask(ctx, domain.DevelopmentTaskID(string(run.TaskID)))
	return stage, task, review
}

// ---- 1. CreateRunReview happy path ----

func TestCreateRunReview_HappyPath(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, run := reviewReadyRun(t, svc, project)

	review, err := svc.CreateRunReview(ctx, run.ID, domain.RunReviewSourceAI, "run summary", "")
	if err != nil {
		t.Fatalf("CreateRunReview: %v", err)
	}
	if review.Status != domain.RunReviewStatusPending {
		t.Errorf("expected PENDING, got %s", review.Status)
	}
	if review.RunID != run.ID {
		t.Errorf("expected run %s, got %s", run.ID, review.RunID)
	}
	if review.Source != domain.RunReviewSourceAI {
		t.Errorf("expected source ai, got %s", review.Source)
	}
	if review.Summary != "run summary" {
		t.Errorf("expected summary 'run summary', got %q", review.Summary)
	}
	if review.CompletedAt != nil {
		t.Error("expected CompletedAt nil for PENDING review")
	}
}

// ---- 2. CreateReview on RUNNING run ----

func TestCreateRunReview_RunRunning_ErrInvalidTransition(t *testing.T) {
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

	_, err = svc.CreateRunReview(ctx, run.ID, domain.RunReviewSourceAI, "", "")
	if err != ErrInvalidTransition {
		t.Errorf("expected ErrInvalidTransition for RUNNING run, got %v", err)
	}
}

// ---- 3. CreateReview on FAILED run ----

func TestCreateRunReview_RunFailed_ErrInvalidTransition(t *testing.T) {
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
	run, _ = svc.StartRun(ctx, run.ID)
	markSessionTerminated(t, ws, sid, domain.ProjectID(project.ID), "process-exited")
	svc.ReconcileRunningRuns(ctx)
	fetched, _ := svc.GetRun(ctx, run.ID)
	if fetched.Status != domain.RunStatusFailed {
		t.Fatalf("expected FAILED, got %s", fetched.Status)
	}

	_, err := svc.CreateRunReview(ctx, run.ID, domain.RunReviewSourceAI, "", "")
	if err != ErrInvalidTransition {
		t.Errorf("expected ErrInvalidTransition for FAILED run, got %v", err)
	}
}

// ---- 4. CreateReview run not SUCCEEDED (task READY after FAILED convergence) ----

func TestCreateRunReview_RunNotSucceeded_ErrInvalidTransition(t *testing.T) {
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
	run, _ = svc.StartRun(ctx, run.ID)
	markSessionTerminated(t, ws, sid, domain.ProjectID(project.ID), "process-exited")
	svc.ReconcileRunningRuns(ctx)
	// Task is READY after FAILED convergence.

	_, err := svc.CreateRunReview(ctx, run.ID, domain.RunReviewSourceAI, "", "")
	if err != ErrInvalidTransition {
		t.Errorf("expected ErrInvalidTransition for FAILED run, got %v", err)
	}
}

// ---- 5. Invalid source ----

func TestCreateRunReview_InvalidSource_ErrInvalidInput(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, run := reviewReadyRun(t, svc, project)

	_, err := svc.CreateRunReview(ctx, run.ID, domain.RunReviewSource("invalid"), "", "")
	if err != ErrInvalidInput {
		t.Errorf("expected ErrInvalidInput for invalid source, got %v", err)
	}
}

// ---- 6. Duplicate review ----

func TestCreateRunReview_Duplicate_ErrConflict(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, run := reviewReadyRun(t, svc, project)

	_, err := svc.CreateRunReview(ctx, run.ID, domain.RunReviewSourceAI, "", "")
	if err != nil {
		t.Fatalf("first CreateRunReview: %v", err)
	}
	_, err = svc.CreateRunReview(ctx, run.ID, domain.RunReviewSourceHuman, "", "")
	if err != ErrConflict {
		t.Errorf("expected ErrConflict for duplicate, got %v", err)
	}
}

// ---- 7. GetRunReview happy ----

func TestGetRunReview_HappyPath(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, review := seedReview(t, svc, project)

	fetched, err := svc.GetRunReview(ctx, review.ID)
	if err != nil {
		t.Fatalf("GetRunReview: %v", err)
	}
	if fetched.ID != review.ID {
		t.Errorf("expected %s, got %s", review.ID, fetched.ID)
	}
	if fetched.Status != domain.RunReviewStatusPending {
		t.Errorf("expected PENDING, got %s", fetched.Status)
	}
}

// ---- 8. GetRunReview not found ----

func TestGetRunReview_NotFound(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.GetRunReview(ctx, domain.RunReviewID("nonexistent"))
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ---- 9. ListRunReviews: existing run, no reviews → empty ----

func TestListRunReviewsByRun_NoReviews_Empty(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, run := reviewReadyRun(t, svc, project)

	reviews, err := svc.ListRunReviewsByRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunReviewsByRun: %v", err)
	}
	if len(reviews) != 0 {
		t.Errorf("expected 0 reviews, got %d", len(reviews))
	}
}

// ---- 10. Pass pending Review ----

func TestPassRunReview_Pending_TaskPassed(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, task, review := seedReview(t, svc, project)

	passed, err := svc.PassRunReview(ctx, review.ID)
	if err != nil {
		t.Fatalf("PassRunReview: %v", err)
	}
	if passed.Status != domain.RunReviewStatusPassed {
		t.Errorf("expected PASSED, got %s", passed.Status)
	}
	if passed.CompletedAt == nil {
		t.Error("expected CompletedAt to be set")
	}

	// Verify review PASSED in store.
	fetched, _ := svc.GetRunReview(ctx, review.ID)
	if fetched.Status != domain.RunReviewStatusPassed {
		t.Errorf("expected PASSED in store, got %s", fetched.Status)
	}

	// Verify task transitioned to PASSED (atomic).
	fetchedTask, _ := svc.GetTask(ctx, task.ID)
	if fetchedTask.Status != domain.TaskStatusPassed {
		t.Errorf("task: expected PASSED, got %s", fetchedTask.Status)
	}
	if fetchedTask.CompletedAt == nil {
		t.Error("task: expected CompletedAt to be set")
	}
}

// ---- 11. Pass terminal Review ----

func TestPassRunReview_Terminal_ErrInvalidTransition(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, review := seedReview(t, svc, project)

	svc.PassRunReview(ctx, review.ID)

	_, err := svc.PassRunReview(ctx, review.ID)
	if err != ErrInvalidTransition {
		t.Errorf("expected ErrInvalidTransition for PASSED review, got %v", err)
	}
}

// ---- 12. Reject pending Review with issues ----

func TestRejectRunReview_Pending_TaskReady(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, task, review := seedReview(t, svc, project)

	rejected, err := svc.RejectRunReview(ctx, review.ID, "code quality issues")
	if err != nil {
		t.Fatalf("RejectRunReview: %v", err)
	}
	if rejected.Status != domain.RunReviewStatusRejected {
		t.Errorf("expected REJECTED, got %s", rejected.Status)
	}
	if rejected.Issues != "code quality issues" {
		t.Errorf("expected 'code quality issues', got %q", rejected.Issues)
	}
	if rejected.CompletedAt == nil {
		t.Error("expected CompletedAt to be set")
	}

	fetchedTask, _ := svc.GetTask(ctx, task.ID)
	if fetchedTask.Status != domain.TaskStatusReady {
		t.Errorf("task: expected READY, got %s", fetchedTask.Status)
	}
	if fetchedTask.CompletedAt != nil {
		t.Error("task: expected CompletedAt nil for READY (rejected) task")
	}
}

// ---- 13. Reject without effective issues ----

func TestRejectRunReview_NoEffectiveIssues_ErrInvalidInput(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, review := seedReview(t, svc, project)

	_, err := svc.RejectRunReview(ctx, review.ID, "  ")
	if err != ErrInvalidInput {
		t.Errorf("expected ErrInvalidInput for empty issues, got %v", err)
	}
	_, err = svc.RejectRunReview(ctx, review.ID, "")
	if err != ErrInvalidInput {
		t.Errorf("expected ErrInvalidInput for empty issues, got %v", err)
	}
}

// ---- 14. Reject fallback to Create-time Issues ----

func TestRejectRunReview_FallbackToCreateIssues(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, run := reviewReadyRun(t, svc, project)

	review, err := svc.CreateRunReview(ctx, run.ID, domain.RunReviewSourceAI, "", "pre-existing issues")
	if err != nil {
		t.Fatalf("CreateRunReview: %v", err)
	}

	rejected, err := svc.RejectRunReview(ctx, review.ID, "")
	if err != nil {
		t.Fatalf("RejectRunReview: %v", err)
	}
	if rejected.Issues != "pre-existing issues" {
		t.Errorf("expected 'pre-existing issues', got %q", rejected.Issues)
	}
}

// ---- 15. Reject terminal Review ----

func TestRejectRunReview_Terminal_ErrInvalidTransition(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, _, review := seedReview(t, svc, project)

	svc.PassRunReview(ctx, review.ID)

	_, err := svc.RejectRunReview(ctx, review.ID, "issues")
	if err != ErrInvalidTransition {
		t.Errorf("expected ErrInvalidTransition for PASSED review, got %v", err)
	}
}

// ---- 16. Pass transaction atomicity: Review PASSED cannot leave Task at REVIEW ----

func TestPassRunReview_TransactionAtomicity_TaskNeverStuckInReview(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, task, review := seedReview(t, svc, project)

	// Pass succeeds — both review and task transition atomically.
	_, err := svc.PassRunReview(ctx, review.ID)
	if err != nil {
		t.Fatalf("PassRunReview: %v", err)
	}

	// Second pass is rejected at service layer; task must stay PASSED (not revert to REVIEW).
	_, err = svc.PassRunReview(ctx, review.ID)
	if err != ErrInvalidTransition {
		t.Fatalf("expected ErrInvalidTransition, got %v", err)
	}

	fetchedTask, _ := svc.GetTask(ctx, task.ID)
	if fetchedTask.Status != domain.TaskStatusPassed {
		t.Errorf("task must remain PASSED, got %s", fetchedTask.Status)
	}

	// Review remains PASSED — no partial rollback visible.
	fetchedReview, _ := svc.GetRunReview(ctx, review.ID)
	if fetchedReview.Status != domain.RunReviewStatusPassed {
		t.Errorf("review must remain PASSED, got %s", fetchedReview.Status)
	}
}

// ---- 17. Reject transaction atomicity: Review REJECTED cannot leave Task at REVIEW ----

func TestRejectRunReview_TransactionAtomicity_TaskNeverStuckInReview(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()
	_, task, review := seedReview(t, svc, project)

	_, err := svc.RejectRunReview(ctx, review.ID, "issues found")
	if err != nil {
		t.Fatalf("RejectRunReview: %v", err)
	}

	// Second reject is rejected at service layer; task must stay READY (not revert to REVIEW).
	_, err = svc.RejectRunReview(ctx, review.ID, "more issues")
	if err != ErrInvalidTransition {
		t.Fatalf("expected ErrInvalidTransition, got %v", err)
	}

	fetchedTask, _ := svc.GetTask(ctx, task.ID)
	if fetchedTask.Status != domain.TaskStatusReady {
		t.Errorf("task must remain READY, got %s", fetchedTask.Status)
	}

	// Review remains REJECTED.
	fetchedReview, _ := svc.GetRunReview(ctx, review.ID)
	if fetchedReview.Status != domain.RunReviewStatusRejected {
		t.Errorf("review must remain REJECTED, got %s", fetchedReview.Status)
	}
}
