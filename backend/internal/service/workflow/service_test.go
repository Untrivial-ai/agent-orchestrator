package workflow

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func newTestService(t *testing.T) (*Service, *domain.ProjectRecord) {
	t.Helper()
	store := sqlitetest.MustOpen(t)

	// Create a test project
	project := domain.ProjectRecord{
		ID:          "test-project-1",
		Path:        "/test/path",
		DisplayName: "Test Project",
		RegisteredAt: time.Now().UTC(),
	}
	if err := store.UpsertProject(context.Background(), project); err != nil {
		t.Fatalf("failed to create test project: %v", err)
	}

	svc := New(store)
	svc.now = func() time.Time { return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC) }
	idCounter := 0
	svc.newID = func() string {
		idCounter++
		return fmt.Sprintf("test-id-%d", idCounter)
	}
	return svc, &project
}

// ---- Plan Tests ----

func TestCreatePlan(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, err := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})
	if err != nil {
		t.Fatalf("CreatePlan failed: %v", err)
	}
	if plan.Status != domain.PlanStatusDraft {
		t.Errorf("expected draft, got %s", plan.Status)
	}
	if plan.Title != "Test Plan" {
		t.Errorf("expected 'Test Plan', got %s", plan.Title)
	}
}

func TestCreatePlanNotFoundProject(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: "nonexistent",
		Title:     "Test Plan",
	})
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestCreatePlanEmptyTitle(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "",
	})
	if err != ErrInvalidInput {
		t.Errorf("expected ErrInvalidInput, got %v", err)
	}
}

func TestConfirmPlan(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})

	confirmed, err := svc.ConfirmPlan(ctx, plan.ID)
	if err != nil {
		t.Fatalf("ConfirmPlan failed: %v", err)
	}
	if confirmed.Status != domain.PlanStatusConfirmed {
		t.Errorf("expected confirmed, got %s", confirmed.Status)
	}
	if confirmed.ConfirmedAt == nil {
		t.Error("expected confirmed_at to be set")
	}
}

func TestConfirmPlanInvalidTransition(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})
	svc.ConfirmPlan(ctx, plan.ID)

	_, err := svc.ConfirmPlan(ctx, plan.ID)
	if err != ErrInvalidTransition {
		t.Errorf("expected ErrInvalidTransition, got %v", err)
	}
}

func TestStartPlanNoStage(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})
	svc.ConfirmPlan(ctx, plan.ID)

	_, err := svc.StartPlan(ctx, plan.ID)
	if err != ErrChildrenIncomplete {
		t.Errorf("expected ErrChildrenIncomplete, got %v", err)
	}
}

func TestStartPlanWithStage(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})
	svc.ConfirmPlan(ctx, plan.ID)

	svc.CreateStage(ctx, CreateStageInput{
		PlanID: plan.ID,
		Title:  "Stage 1",
	})

	started, err := svc.StartPlan(ctx, plan.ID)
	if err != nil {
		t.Fatalf("StartPlan failed: %v", err)
	}
	if started.Status != domain.PlanStatusInProgress {
		t.Errorf("expected in_progress, got %s", started.Status)
	}
}

func TestCompletePlanWithIncompleteStage(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})
	svc.ConfirmPlan(ctx, plan.ID)

	svc.CreateStage(ctx, CreateStageInput{
		PlanID: plan.ID,
		Title:  "Stage 1",
	})
	svc.StartPlan(ctx, plan.ID)

	_, err := svc.CompletePlan(ctx, plan.ID)
	if err != ErrChildrenIncomplete {
		t.Errorf("expected ErrChildrenIncomplete, got %v", err)
	}
}

func TestCompletePlanAllStagesPassed(t *testing.T) {
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

	// Add a task and complete it
	task, _ := svc.CreateTask(ctx, CreateTaskInput{
		StageID: stage.ID,
		Title:   "Task 1",
	})
	svc.ReadyTask(ctx, task.ID)
	svc.StartTask(ctx, task.ID)
	svc.SubmitForReview(ctx, task.ID)
	svc.PassTask(ctx, task.ID)

	svc.ReadyForApproval(ctx, stage.ID)
	svc.PassStage(ctx, stage.ID)

	completed, err := svc.CompletePlan(ctx, plan.ID)
	if err != nil {
		t.Fatalf("CompletePlan failed: %v", err)
	}
	if completed.Status != domain.PlanStatusCompleted {
		t.Errorf("expected completed, got %s", completed.Status)
	}
	if completed.CompletedAt == nil {
		t.Error("expected completed_at to be set")
	}
}

func TestCompletePlanWithCancelledStage(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})
	svc.ConfirmPlan(ctx, plan.ID)

	stage1, _ := svc.CreateStage(ctx, CreateStageInput{
		PlanID:   plan.ID,
		Title:    "Stage 1",
		Sequence: 1,
	})
	stage2, _ := svc.CreateStage(ctx, CreateStageInput{
		PlanID:   plan.ID,
		Title:    "Stage 2",
		Sequence: 2,
	})
	svc.StartPlan(ctx, plan.ID)
	svc.StartStage(ctx, stage1.ID)

	// Complete stage1
	task1, _ := svc.CreateTask(ctx, CreateTaskInput{
		StageID: stage1.ID,
		Title:   "Task 1",
	})
	svc.ReadyTask(ctx, task1.ID)
	svc.StartTask(ctx, task1.ID)
	svc.SubmitForReview(ctx, task1.ID)
	svc.PassTask(ctx, task1.ID)
	svc.ReadyForApproval(ctx, stage1.ID)
	svc.PassStage(ctx, stage1.ID)

	// Cancel stage2
	svc.StartStage(ctx, stage2.ID)
	svc.CancelStage(ctx, stage2.ID)

	completed, err := svc.CompletePlan(ctx, plan.ID)
	if err != nil {
		t.Fatalf("CompletePlan failed: %v", err)
	}
	if completed.Status != domain.PlanStatusCompleted {
		t.Errorf("expected completed, got %s", completed.Status)
	}
}

func TestCancelPlan(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})

	cancelled, err := svc.CancelPlan(ctx, plan.ID)
	if err != nil {
		t.Fatalf("CancelPlan failed: %v", err)
	}
	if cancelled.Status != domain.PlanStatusCancelled {
		t.Errorf("expected cancelled, got %s", cancelled.Status)
	}
	if cancelled.CompletedAt == nil {
		t.Error("expected completed_at to be set")
	}
}

// ---- Stage Tests ----

func TestCreateStage(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})

	stage, err := svc.CreateStage(ctx, CreateStageInput{
		PlanID: plan.ID,
		Title:  "Test Stage",
	})
	if err != nil {
		t.Fatalf("CreateStage failed: %v", err)
	}
	if stage.Status != domain.StageStatusPending {
		t.Errorf("expected pending, got %s", stage.Status)
	}
}

func TestStartStageInvalidPlanStatus(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})

	stage, _ := svc.CreateStage(ctx, CreateStageInput{
		PlanID: plan.ID,
		Title:  "Test Stage",
	})

	// Plan is still DRAFT, should fail
	_, err := svc.StartStage(ctx, stage.ID)
	if err != ErrInvalidTransition {
		t.Errorf("expected ErrInvalidTransition, got %v", err)
	}
}

func TestStartStageValid(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})
	svc.ConfirmPlan(ctx, plan.ID)

	stage, _ := svc.CreateStage(ctx, CreateStageInput{
		PlanID: plan.ID,
		Title:  "Test Stage",
	})

	started, err := svc.StartStage(ctx, stage.ID)
	if err != nil {
		t.Fatalf("StartStage failed: %v", err)
	}
	if started.Status != domain.StageStatusInProgress {
		t.Errorf("expected in_progress, got %s", started.Status)
	}
	if started.StartedAt == nil {
		t.Error("expected started_at to be set")
	}
}

func TestReadyForApprovalWithIncompleteTasks(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})
	svc.ConfirmPlan(ctx, plan.ID)

	stage, _ := svc.CreateStage(ctx, CreateStageInput{
		PlanID: plan.ID,
		Title:  "Test Stage",
	})
	svc.StartPlan(ctx, plan.ID)
	svc.StartStage(ctx, stage.ID)

	// Create a task but don't complete it
	svc.CreateTask(ctx, CreateTaskInput{
		StageID: stage.ID,
		Title:   "Task 1",
	})

	_, err := svc.ReadyForApproval(ctx, stage.ID)
	if err != ErrChildrenIncomplete {
		t.Errorf("expected ErrChildrenIncomplete, got %v", err)
	}
}

func TestReadyForApprovalAllTasksPassed(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})
	svc.ConfirmPlan(ctx, plan.ID)

	stage, _ := svc.CreateStage(ctx, CreateStageInput{
		PlanID: plan.ID,
		Title:  "Test Stage",
	})
	svc.StartPlan(ctx, plan.ID)
	svc.StartStage(ctx, stage.ID)

	task, _ := svc.CreateTask(ctx, CreateTaskInput{
		StageID: stage.ID,
		Title:   "Task 1",
	})
	svc.ReadyTask(ctx, task.ID)
	svc.StartTask(ctx, task.ID)
	svc.SubmitForReview(ctx, task.ID)
	svc.PassTask(ctx, task.ID)

	ready, err := svc.ReadyForApproval(ctx, stage.ID)
	if err != nil {
		t.Fatalf("ReadyForApproval failed: %v", err)
	}
	if ready.Status != domain.StageStatusReadyForApproval {
		t.Errorf("expected ready_for_approval, got %s", ready.Status)
	}
}

func TestReadyForApprovalCancelledTaskNotBlocking(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})
	svc.ConfirmPlan(ctx, plan.ID)

	stage, _ := svc.CreateStage(ctx, CreateStageInput{
		PlanID: plan.ID,
		Title:  "Test Stage",
	})
	svc.StartPlan(ctx, plan.ID)
	svc.StartStage(ctx, stage.ID)

	// Create and pass one task
	task1, _ := svc.CreateTask(ctx, CreateTaskInput{
		StageID:  stage.ID,
		Title:    "Task 1",
		Sequence: 1,
	})
	svc.ReadyTask(ctx, task1.ID)
	svc.StartTask(ctx, task1.ID)
	svc.SubmitForReview(ctx, task1.ID)
	svc.PassTask(ctx, task1.ID)

	// Create and cancel another task
	task2, _ := svc.CreateTask(ctx, CreateTaskInput{
		StageID:  stage.ID,
		Title:    "Task 2",
		Sequence: 2,
	})
	svc.CancelTask(ctx, task2.ID)

	ready, err := svc.ReadyForApproval(ctx, stage.ID)
	if err != nil {
		t.Fatalf("ReadyForApproval failed: %v", err)
	}
	if ready.Status != domain.StageStatusReadyForApproval {
		t.Errorf("expected ready_for_approval, got %s", ready.Status)
	}
}

func TestPassStageRevalidatesTasks(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})
	svc.ConfirmPlan(ctx, plan.ID)

	stage, _ := svc.CreateStage(ctx, CreateStageInput{
		PlanID: plan.ID,
		Title:  "Test Stage",
	})
	svc.StartPlan(ctx, plan.ID)
	svc.StartStage(ctx, stage.ID)

	task, _ := svc.CreateTask(ctx, CreateTaskInput{
		StageID: stage.ID,
		Title:   "Task 1",
	})
	svc.ReadyTask(ctx, task.ID)
	svc.StartTask(ctx, task.ID)
	svc.SubmitForReview(ctx, task.ID)
	svc.PassTask(ctx, task.ID)

	svc.ReadyForApproval(ctx, stage.ID)

	passed, err := svc.PassStage(ctx, stage.ID)
	if err != nil {
		t.Fatalf("PassStage failed: %v", err)
	}
	if passed.Status != domain.StageStatusPassed {
		t.Errorf("expected passed, got %s", passed.Status)
	}
	if passed.CompletedAt == nil {
		t.Error("expected completed_at to be set")
	}
}

func TestCancelStage(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})
	svc.ConfirmPlan(ctx, plan.ID)

	stage, _ := svc.CreateStage(ctx, CreateStageInput{
		PlanID: plan.ID,
		Title:  "Test Stage",
	})
	svc.StartStage(ctx, stage.ID)

	cancelled, err := svc.CancelStage(ctx, stage.ID)
	if err != nil {
		t.Fatalf("CancelStage failed: %v", err)
	}
	if cancelled.Status != domain.StageStatusCancelled {
		t.Errorf("expected cancelled, got %s", cancelled.Status)
	}
	if cancelled.CompletedAt == nil {
		t.Error("expected completed_at to be set")
	}
}

// ---- Task Tests ----

func TestCreateTask(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})
	svc.ConfirmPlan(ctx, plan.ID)

	stage, _ := svc.CreateStage(ctx, CreateStageInput{
		PlanID: plan.ID,
		Title:  "Test Stage",
	})

	task, err := svc.CreateTask(ctx, CreateTaskInput{
		StageID: stage.ID,
		Title:   "Test Task",
	})
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}
	if task.Status != domain.TaskStatusPending {
		t.Errorf("expected pending, got %s", task.Status)
	}
}

func TestReadyTask(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})
	svc.ConfirmPlan(ctx, plan.ID)

	stage, _ := svc.CreateStage(ctx, CreateStageInput{
		PlanID: plan.ID,
		Title:  "Test Stage",
	})

	task, _ := svc.CreateTask(ctx, CreateTaskInput{
		StageID: stage.ID,
		Title:   "Test Task",
	})

	ready, err := svc.ReadyTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ReadyTask failed: %v", err)
	}
	if ready.Status != domain.TaskStatusReady {
		t.Errorf("expected ready, got %s", ready.Status)
	}
}

func TestStartTaskInvalidStageStatus(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})
	svc.ConfirmPlan(ctx, plan.ID)

	stage, _ := svc.CreateStage(ctx, CreateStageInput{
		PlanID: plan.ID,
		Title:  "Test Stage",
	})

	task, _ := svc.CreateTask(ctx, CreateTaskInput{
		StageID: stage.ID,
		Title:   "Test Task",
	})
	svc.ReadyTask(ctx, task.ID)

	// Stage is still PENDING, should fail
	_, err := svc.StartTask(ctx, task.ID)
	if err != ErrInvalidTransition {
		t.Errorf("expected ErrInvalidTransition, got %v", err)
	}
}

func TestStartTaskValid(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})
	svc.ConfirmPlan(ctx, plan.ID)
	svc.CreateStage(ctx, CreateStageInput{
		PlanID:   plan.ID,
		Title:    "Stage 1",
		Sequence: 1,
	})
	svc.StartPlan(ctx, plan.ID)

	stage, _ := svc.CreateStage(ctx, CreateStageInput{
		PlanID:   plan.ID,
		Title:    "Stage 2",
		Sequence: 2,
	})
	svc.StartStage(ctx, stage.ID)

	task, _ := svc.CreateTask(ctx, CreateTaskInput{
		StageID: stage.ID,
		Title:   "Test Task",
	})
	svc.ReadyTask(ctx, task.ID)

	started, err := svc.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask failed: %v", err)
	}
	if started.Status != domain.TaskStatusRunning {
		t.Errorf("expected running, got %s", started.Status)
	}
	if started.StartedAt == nil {
		t.Error("expected started_at to be set")
	}
}

func TestSubmitForReview(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})
	svc.ConfirmPlan(ctx, plan.ID)

	stage, _ := svc.CreateStage(ctx, CreateStageInput{
		PlanID: plan.ID,
		Title:  "Test Stage",
	})
	svc.StartPlan(ctx, plan.ID)
	svc.StartStage(ctx, stage.ID)

	task, _ := svc.CreateTask(ctx, CreateTaskInput{
		StageID: stage.ID,
		Title:   "Test Task",
	})
	svc.ReadyTask(ctx, task.ID)
	svc.StartTask(ctx, task.ID)

	review, err := svc.SubmitForReview(ctx, task.ID)
	if err != nil {
		t.Fatalf("SubmitForReview failed: %v", err)
	}
	if review.Status != domain.TaskStatusReview {
		t.Errorf("expected review, got %s", review.Status)
	}
}

func TestPassTask(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})
	svc.ConfirmPlan(ctx, plan.ID)

	stage, _ := svc.CreateStage(ctx, CreateStageInput{
		PlanID: plan.ID,
		Title:  "Test Stage",
	})
	svc.StartPlan(ctx, plan.ID)
	svc.StartStage(ctx, stage.ID)

	task, _ := svc.CreateTask(ctx, CreateTaskInput{
		StageID: stage.ID,
		Title:   "Test Task",
	})
	svc.ReadyTask(ctx, task.ID)
	svc.StartTask(ctx, task.ID)
	svc.SubmitForReview(ctx, task.ID)

	passed, err := svc.PassTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("PassTask failed: %v", err)
	}
	if passed.Status != domain.TaskStatusPassed {
		t.Errorf("expected passed, got %s", passed.Status)
	}
	if passed.CompletedAt == nil {
		t.Error("expected completed_at to be set")
	}
}

func TestRejectTask(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})
	svc.ConfirmPlan(ctx, plan.ID)

	stage, _ := svc.CreateStage(ctx, CreateStageInput{
		PlanID: plan.ID,
		Title:  "Test Stage",
	})
	svc.StartPlan(ctx, plan.ID)
	svc.StartStage(ctx, stage.ID)

	task, _ := svc.CreateTask(ctx, CreateTaskInput{
		StageID: stage.ID,
		Title:   "Test Task",
	})
	svc.ReadyTask(ctx, task.ID)
	svc.StartTask(ctx, task.ID)
	svc.SubmitForReview(ctx, task.ID)

	rejected, err := svc.RejectTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("RejectTask failed: %v", err)
	}
	if rejected.Status != domain.TaskStatusReady {
		t.Errorf("expected ready, got %s", rejected.Status)
	}
	// Verify no REJECTED status
	if rejected.Status == "rejected" {
		t.Error("task should not have REJECTED status")
	}
}

func TestBlockTask(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})
	svc.ConfirmPlan(ctx, plan.ID)

	stage, _ := svc.CreateStage(ctx, CreateStageInput{
		PlanID: plan.ID,
		Title:  "Test Stage",
	})
	svc.StartPlan(ctx, plan.ID)
	svc.StartStage(ctx, stage.ID)

	task, _ := svc.CreateTask(ctx, CreateTaskInput{
		StageID: stage.ID,
		Title:   "Test Task",
	})
	svc.ReadyTask(ctx, task.ID)

	blocked, err := svc.BlockTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("BlockTask failed: %v", err)
	}
	if blocked.Status != domain.TaskStatusBlocked {
		t.Errorf("expected blocked, got %s", blocked.Status)
	}
}

func TestUnblockTask(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})
	svc.ConfirmPlan(ctx, plan.ID)

	stage, _ := svc.CreateStage(ctx, CreateStageInput{
		PlanID: plan.ID,
		Title:  "Test Stage",
	})
	svc.StartPlan(ctx, plan.ID)
	svc.StartStage(ctx, stage.ID)

	task, _ := svc.CreateTask(ctx, CreateTaskInput{
		StageID: stage.ID,
		Title:   "Test Task",
	})
	svc.ReadyTask(ctx, task.ID)
	svc.BlockTask(ctx, task.ID)

	unblocked, err := svc.UnblockTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("UnblockTask failed: %v", err)
	}
	if unblocked.Status != domain.TaskStatusReady {
		t.Errorf("expected ready, got %s", unblocked.Status)
	}
}

func TestCancelTask(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})
	svc.ConfirmPlan(ctx, plan.ID)

	stage, _ := svc.CreateStage(ctx, CreateStageInput{
		PlanID: plan.ID,
		Title:  "Test Stage",
	})

	task, _ := svc.CreateTask(ctx, CreateTaskInput{
		StageID: stage.ID,
		Title:   "Test Task",
	})

	cancelled, err := svc.CancelTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("CancelTask failed: %v", err)
	}
	if cancelled.Status != domain.TaskStatusCancelled {
		t.Errorf("expected cancelled, got %s", cancelled.Status)
	}
	if cancelled.CompletedAt == nil {
		t.Error("expected completed_at to be set")
	}
}

// ---- Error Tests ----

func TestGetNotFound(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.GetPlan(ctx, "nonexistent")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	_, err = svc.GetStage(ctx, "nonexistent")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	_, err = svc.GetTask(ctx, "nonexistent")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestInvalidTransition(t *testing.T) {
	svc, project := newTestService(t)
	ctx := context.Background()

	plan, _ := svc.CreatePlan(ctx, CreatePlanInput{
		ProjectID: domain.ProjectID(project.ID),
		Title:     "Test Plan",
	})

	// Try to start a draft plan
	_, err := svc.StartPlan(ctx, plan.ID)
	if err != ErrInvalidTransition {
		t.Errorf("expected ErrInvalidTransition, got %v", err)
	}
}
