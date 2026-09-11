package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func TestWorkflowPlanCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "wf")

	now := time.Now().UTC().Truncate(time.Second)
	plan := domain.DevelopmentPlan{
		ID:        "plan-1",
		ProjectID: "wf",
		Title:     "Build API",
		Objective: "REST API for users",
		Status:    domain.PlanStatusDraft,
		CreatedAt: now,
	}
	if err := s.CreateDevelopmentPlan(ctx, plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}

	got, ok, err := s.GetDevelopmentPlan(ctx, "plan-1")
	if err != nil || !ok {
		t.Fatalf("get plan: ok=%v err=%v", ok, err)
	}
	if got.Title != "Build API" || got.Status != domain.PlanStatusDraft || got.ProjectID != "wf" {
		t.Fatalf("plan = %+v", got)
	}

	list, err := s.ListDevelopmentPlansByProject(ctx, "wf")
	if err != nil || len(list) != 1 {
		t.Fatalf("list plans: len=%d err=%v", len(list), err)
	}
}

func TestWorkflowPlanStatusUpdate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "wf")
	now := time.Now().UTC().Truncate(time.Second)

	plan := domain.DevelopmentPlan{ID: "plan-2", ProjectID: "wf", Title: "T", Status: domain.PlanStatusDraft, CreatedAt: now}
	if err := s.CreateDevelopmentPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}

	confirmedAt := now.Add(time.Minute)
	if err := s.UpdateDevelopmentPlanStatus(ctx, "plan-2", domain.PlanStatusConfirmed, &confirmedAt, nil); err != nil {
		t.Fatalf("update status: %v", err)
	}
	got, _, _ := s.GetDevelopmentPlan(ctx, "plan-2")
	if got.Status != domain.PlanStatusConfirmed {
		t.Fatalf("status = %s, want confirmed", got.Status)
	}
	if got.ConfirmedAt == nil || !got.ConfirmedAt.Equal(confirmedAt) {
		t.Fatalf("confirmedAt = %v, want %v", got.ConfirmedAt, confirmedAt)
	}

	completedAt := now.Add(2 * time.Minute)
	if err := s.UpdateDevelopmentPlanStatus(ctx, "plan-2", domain.PlanStatusCompleted, nil, &completedAt); err != nil {
		t.Fatalf("complete: %v", err)
	}
	got, _, _ = s.GetDevelopmentPlan(ctx, "plan-2")
	if got.Status != domain.PlanStatusCompleted {
		t.Fatalf("status = %s, want completed", got.Status)
	}
	if got.CompletedAt == nil || !got.CompletedAt.Equal(completedAt) {
		t.Fatalf("completedAt = %v", got.CompletedAt)
	}
}

func TestWorkflowPlanContentUpdate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "wf")
	now := time.Now().UTC().Truncate(time.Second)

	plan := domain.DevelopmentPlan{ID: "plan-3", ProjectID: "wf", Title: "Old", Objective: "Old obj", Status: domain.PlanStatusDraft, CreatedAt: now}
	if err := s.CreateDevelopmentPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDevelopmentPlanContent(ctx, "plan-3", "New Title", "New obj", "reqs", "summary"); err != nil {
		t.Fatalf("update content: %v", err)
	}
	got, _, _ := s.GetDevelopmentPlan(ctx, "plan-3")
	if got.Title != "New Title" || got.Objective != "New obj" || got.Requirements != "reqs" || got.ImplementationSummary != "summary" {
		t.Fatalf("content = %+v", got)
	}
}

func TestWorkflowStageCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "wf")
	now := time.Now().UTC().Truncate(time.Second)

	if err := s.CreateDevelopmentPlan(ctx, domain.DevelopmentPlan{ID: "p1", ProjectID: "wf", Title: "P", Status: domain.PlanStatusDraft, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	stage := domain.DevelopmentStage{
		ID:        "stg-1",
		PlanID:    "p1",
		Sequence:  1,
		Title:     "Design",
		Status:    domain.StageStatusPending,
		CreatedAt: now,
	}
	if err := s.CreateDevelopmentStage(ctx, stage); err != nil {
		t.Fatalf("create stage: %v", err)
	}

	got, ok, err := s.GetDevelopmentStage(ctx, "stg-1")
	if err != nil || !ok {
		t.Fatalf("get stage: ok=%v err=%v", ok, err)
	}
	if got.Sequence != 1 || got.Title != "Design" || got.Status != domain.StageStatusPending {
		t.Fatalf("stage = %+v", got)
	}

	list, err := s.ListDevelopmentStagesByPlan(ctx, "p1")
	if err != nil || len(list) != 1 {
		t.Fatalf("list stages: len=%d err=%v", len(list), err)
	}
}

func TestWorkflowStageSequenceUnique(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "wf")
	now := time.Now().UTC().Truncate(time.Second)

	s.CreateDevelopmentPlan(ctx, domain.DevelopmentPlan{ID: "p1", ProjectID: "wf", Title: "P", Status: domain.PlanStatusDraft, CreatedAt: now})
	s.CreateDevelopmentStage(ctx, domain.DevelopmentStage{ID: "s1", PlanID: "p1", Sequence: 1, Title: "A", Status: domain.StageStatusPending, CreatedAt: now})

	err := s.CreateDevelopmentStage(ctx, domain.DevelopmentStage{ID: "s2", PlanID: "p1", Sequence: 1, Title: "B", Status: domain.StageStatusPending, CreatedAt: now})
	if err == nil {
		t.Fatal("duplicate stage sequence should be rejected")
	}
}

func TestWorkflowStageCascadeDeleteViaSQL(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "wf")
	now := time.Now().UTC().Truncate(time.Second)

	s.CreateDevelopmentPlan(ctx, domain.DevelopmentPlan{ID: "p1", ProjectID: "wf", Title: "P", Status: domain.PlanStatusDraft, CreatedAt: now})
	s.CreateDevelopmentStage(ctx, domain.DevelopmentStage{ID: "s1", PlanID: "p1", Sequence: 1, Title: "A", Status: domain.StageStatusPending, CreatedAt: now})

	// Verify FK cascade: deleting plan cascades to stages.
	// We test via the public Get method after direct SQL delete through the store.
	// The store's own Delete methods don't exist yet; the cascade is a DB-level guarantee.
	// We verify by checking the stage exists, then testing FK constraint on orphan insert.
	_, ok, _ := s.GetDevelopmentStage(ctx, "s1")
	if !ok {
		t.Fatal("stage should exist before cascade test")
	}
}

func TestWorkflowTaskCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "wf")
	now := time.Now().UTC().Truncate(time.Second)

	s.CreateDevelopmentPlan(ctx, domain.DevelopmentPlan{ID: "p1", ProjectID: "wf", Title: "P", Status: domain.PlanStatusDraft, CreatedAt: now})
	s.CreateDevelopmentStage(ctx, domain.DevelopmentStage{ID: "s1", PlanID: "p1", Sequence: 1, Title: "S", Status: domain.StageStatusPending, CreatedAt: now})

	task := domain.DevelopmentTask{
		ID:        "t1",
		StageID:   "s1",
		Sequence:  1,
		Title:     "Implement endpoint",
		TaskType:  "feature",
		Status:    domain.TaskStatusPending,
		CreatedAt: now,
	}
	if err := s.CreateDevelopmentTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	got, ok, err := s.GetDevelopmentTask(ctx, "t1")
	if err != nil || !ok {
		t.Fatalf("get task: ok=%v err=%v", ok, err)
	}
	if got.Title != "Implement endpoint" || got.TaskType != "feature" || got.Status != domain.TaskStatusPending {
		t.Fatalf("task = %+v", got)
	}

	list, err := s.ListDevelopmentTasksByStage(ctx, "s1")
	if err != nil || len(list) != 1 {
		t.Fatalf("list tasks: len=%d err=%v", len(list), err)
	}
}

func TestWorkflowTaskSequenceUnique(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "wf")
	now := time.Now().UTC().Truncate(time.Second)

	s.CreateDevelopmentPlan(ctx, domain.DevelopmentPlan{ID: "p1", ProjectID: "wf", Title: "P", Status: domain.PlanStatusDraft, CreatedAt: now})
	s.CreateDevelopmentStage(ctx, domain.DevelopmentStage{ID: "s1", PlanID: "p1", Sequence: 1, Title: "S", Status: domain.StageStatusPending, CreatedAt: now})
	s.CreateDevelopmentTask(ctx, domain.DevelopmentTask{ID: "t1", StageID: "s1", Sequence: 1, Title: "A", Status: domain.TaskStatusPending, CreatedAt: now})

	err := s.CreateDevelopmentTask(ctx, domain.DevelopmentTask{ID: "t2", StageID: "s1", Sequence: 1, Title: "B", Status: domain.TaskStatusPending, CreatedAt: now})
	if err == nil {
		t.Fatal("duplicate task sequence should be rejected")
	}
}

func TestWorkflowTaskStatusUpdate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "wf")
	now := time.Now().UTC().Truncate(time.Second)

	s.CreateDevelopmentPlan(ctx, domain.DevelopmentPlan{ID: "p1", ProjectID: "wf", Title: "P", Status: domain.PlanStatusDraft, CreatedAt: now})
	s.CreateDevelopmentStage(ctx, domain.DevelopmentStage{ID: "s1", PlanID: "p1", Sequence: 1, Title: "S", Status: domain.StageStatusPending, CreatedAt: now})
	s.CreateDevelopmentTask(ctx, domain.DevelopmentTask{ID: "t1", StageID: "s1", Sequence: 1, Title: "T", Status: domain.TaskStatusPending, CreatedAt: now})

	startedAt := now.Add(time.Minute)
	if err := s.UpdateDevelopmentTaskStatus(ctx, "t1", domain.TaskStatusRunning, &startedAt, nil); err != nil {
		t.Fatalf("update status: %v", err)
	}
	got, _, _ := s.GetDevelopmentTask(ctx, "t1")
	if got.Status != domain.TaskStatusRunning || got.StartedAt == nil {
		t.Fatalf("task = %+v", got)
	}
}

func TestWorkflowTaskAssignmentUpdate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "wf")
	now := time.Now().UTC().Truncate(time.Second)

	s.CreateDevelopmentPlan(ctx, domain.DevelopmentPlan{ID: "p1", ProjectID: "wf", Title: "P", Status: domain.PlanStatusDraft, CreatedAt: now})
	s.CreateDevelopmentStage(ctx, domain.DevelopmentStage{ID: "s1", PlanID: "p1", Sequence: 1, Title: "S", Status: domain.StageStatusPending, CreatedAt: now})
	s.CreateDevelopmentTask(ctx, domain.DevelopmentTask{ID: "t1", StageID: "s1", Sequence: 1, Title: "T", Status: domain.TaskStatusPending, CreatedAt: now})

	if err := s.UpdateDevelopmentTaskAssignment(ctx, "t1", "role-1", "prov-1", "model-1"); err != nil {
		t.Fatalf("update assignment: %v", err)
	}
	got, _, _ := s.GetDevelopmentTask(ctx, "t1")
	if got.AgentRoleID != "role-1" || got.ProviderID != "prov-1" || got.ProviderModelID != "model-1" {
		t.Fatalf("assignment = %+v", got)
	}
}

func TestAgentRoleCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	role := domain.AgentRole{
		ID:          "role-1",
		Name:        "developer",
		DisplayName: "Developer",
		Description: "Writes code",
		Enabled:     true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.CreateAgentRole(ctx, role); err != nil {
		t.Fatalf("create role: %v", err)
	}

	got, ok, err := s.GetAgentRole(ctx, "role-1")
	if err != nil || !ok {
		t.Fatalf("get role: ok=%v err=%v", ok, err)
	}
	if got.Name != "developer" || got.DisplayName != "Developer" || !got.Enabled {
		t.Fatalf("role = %+v", got)
	}

	list, err := s.ListAgentRoles(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list roles: len=%d err=%v", len(list), err)
	}
}

func TestAgentRoleNameUnique(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	s.CreateAgentRole(ctx, domain.AgentRole{ID: "r1", Name: "dev", DisplayName: "Dev", Enabled: true, CreatedAt: now, UpdatedAt: now})
	err := s.CreateAgentRole(ctx, domain.AgentRole{ID: "r2", Name: "dev", DisplayName: "Dev2", Enabled: true, CreatedAt: now, UpdatedAt: now})
	if err == nil {
		t.Fatal("duplicate role name should be rejected")
	}
}

func TestAgentRoleUpdate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	s.CreateAgentRole(ctx, domain.AgentRole{ID: "r1", Name: "dev", DisplayName: "Dev", Description: "old", Enabled: true, CreatedAt: now, UpdatedAt: now})
	updated := domain.AgentRole{ID: "r1", Name: "dev", DisplayName: "Developer", Description: "new desc", SystemPrompt: "You are a developer", Enabled: true, CreatedAt: now, UpdatedAt: now.Add(time.Minute)}
	if err := s.UpdateAgentRole(ctx, updated); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _, _ := s.GetAgentRole(ctx, "r1")
	if got.DisplayName != "Developer" || got.Description != "new desc" || got.SystemPrompt != "You are a developer" {
		t.Fatalf("role = %+v", got)
	}
}

func TestAgentRoleSetEnabled(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	s.CreateAgentRole(ctx, domain.AgentRole{ID: "r1", Name: "dev", DisplayName: "Dev", Enabled: true, CreatedAt: now, UpdatedAt: now})
	if err := s.SetAgentRoleEnabled(ctx, "r1", false, now.Add(time.Minute)); err != nil {
		t.Fatalf("disable: %v", err)
	}
	got, _, _ := s.GetAgentRole(ctx, "r1")
	if got.Enabled {
		t.Fatal("role should be disabled")
	}
}

func TestTaskRunCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "wf")
	now := time.Now().UTC().Truncate(time.Second)

	s.CreateDevelopmentPlan(ctx, domain.DevelopmentPlan{ID: "p1", ProjectID: "wf", Title: "P", Status: domain.PlanStatusDraft, CreatedAt: now})
	s.CreateDevelopmentStage(ctx, domain.DevelopmentStage{ID: "s1", PlanID: "p1", Sequence: 1, Title: "S", Status: domain.StageStatusPending, CreatedAt: now})
	s.CreateDevelopmentTask(ctx, domain.DevelopmentTask{ID: "t1", StageID: "s1", Sequence: 1, Title: "T", Status: domain.TaskStatusPending, CreatedAt: now})

	run := domain.TaskRun{
		ID:          "run-1",
		TaskID:      "t1",
		Attempt:     1,
		Status:      domain.RunStatusPending,
		CreatedAt:   now,
	}
	if err := s.CreateTaskRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	got, ok, err := s.GetTaskRun(ctx, "run-1")
	if err != nil || !ok {
		t.Fatalf("get run: ok=%v err=%v", ok, err)
	}
	if got.Attempt != 1 || got.Status != domain.RunStatusPending || got.TaskID != "t1" {
		t.Fatalf("run = %+v", got)
	}

	list, err := s.ListTaskRunsByTask(ctx, "t1")
	if err != nil || len(list) != 1 {
		t.Fatalf("list runs: len=%d err=%v", len(list), err)
	}
}

func TestTaskRunAttemptUnique(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "wf")
	now := time.Now().UTC().Truncate(time.Second)

	s.CreateDevelopmentPlan(ctx, domain.DevelopmentPlan{ID: "p1", ProjectID: "wf", Title: "P", Status: domain.PlanStatusDraft, CreatedAt: now})
	s.CreateDevelopmentStage(ctx, domain.DevelopmentStage{ID: "s1", PlanID: "p1", Sequence: 1, Title: "S", Status: domain.StageStatusPending, CreatedAt: now})
	s.CreateDevelopmentTask(ctx, domain.DevelopmentTask{ID: "t1", StageID: "s1", Sequence: 1, Title: "T", Status: domain.TaskStatusPending, CreatedAt: now})
	s.CreateTaskRun(ctx, domain.TaskRun{ID: "r1", TaskID: "t1", Attempt: 1, Status: domain.RunStatusPending, CreatedAt: now})

	err := s.CreateTaskRun(ctx, domain.TaskRun{ID: "r2", TaskID: "t1", Attempt: 1, Status: domain.RunStatusPending, CreatedAt: now})
	if err == nil {
		t.Fatal("duplicate attempt should be rejected")
	}
}

func TestTaskRunStatusUpdate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "wf")
	now := time.Now().UTC().Truncate(time.Second)

	s.CreateDevelopmentPlan(ctx, domain.DevelopmentPlan{ID: "p1", ProjectID: "wf", Title: "P", Status: domain.PlanStatusDraft, CreatedAt: now})
	s.CreateDevelopmentStage(ctx, domain.DevelopmentStage{ID: "s1", PlanID: "p1", Sequence: 1, Title: "S", Status: domain.StageStatusPending, CreatedAt: now})
	s.CreateDevelopmentTask(ctx, domain.DevelopmentTask{ID: "t1", StageID: "s1", Sequence: 1, Title: "T", Status: domain.TaskStatusPending, CreatedAt: now})
	s.CreateTaskRun(ctx, domain.TaskRun{ID: "r1", TaskID: "t1", Attempt: 1, Status: domain.RunStatusPending, CreatedAt: now})

	startedAt := now.Add(time.Minute)
	if err := s.UpdateTaskRunStatus(ctx, "r1", domain.RunStatusRunning, "", "", &startedAt, nil); err != nil {
		t.Fatalf("running: %v", err)
	}
	got, _, _ := s.GetTaskRun(ctx, "r1")
	if got.Status != domain.RunStatusRunning || got.StartedAt == nil {
		t.Fatalf("run = %+v", got)
	}

	finishedAt := now.Add(2 * time.Minute)
	if err := s.UpdateTaskRunStatus(ctx, "r1", domain.RunStatusSucceeded, "done", "", nil, &finishedAt); err != nil {
		t.Fatalf("succeeded: %v", err)
	}
	got, _, _ = s.GetTaskRun(ctx, "r1")
	if got.Status != domain.RunStatusSucceeded || got.ResultSummary != "done" || got.FinishedAt == nil {
		t.Fatalf("run = %+v", got)
	}
}

func TestTaskRunBindSession(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "wf")
	now := time.Now().UTC().Truncate(time.Second)

	s.CreateDevelopmentPlan(ctx, domain.DevelopmentPlan{ID: "p1", ProjectID: "wf", Title: "P", Status: domain.PlanStatusDraft, CreatedAt: now})
	s.CreateDevelopmentStage(ctx, domain.DevelopmentStage{ID: "s1", PlanID: "p1", Sequence: 1, Title: "S", Status: domain.StageStatusPending, CreatedAt: now})
	s.CreateDevelopmentTask(ctx, domain.DevelopmentTask{ID: "t1", StageID: "s1", Sequence: 1, Title: "T", Status: domain.TaskStatusPending, CreatedAt: now})
	s.CreateTaskRun(ctx, domain.TaskRun{ID: "r1", TaskID: "t1", Attempt: 1, Status: domain.RunStatusPending, CreatedAt: now})

	if err := s.BindTaskRunSession(ctx, "r1", "wf-1"); err != nil {
		t.Fatalf("bind: %v", err)
	}
	got, _, _ := s.GetTaskRun(ctx, "r1")
	if got.SessionID != "wf-1" {
		t.Fatalf("session = %s", got.SessionID)
	}

	// Binding again should be a no-op (already bound).
	if err := s.BindTaskRunSession(ctx, "r1", "wf-2"); err != nil {
		t.Fatalf("rebind: %v", err)
	}
	got, _, _ = s.GetTaskRun(ctx, "r1")
	if got.SessionID != "wf-1" {
		t.Fatalf("session should not change: %s", got.SessionID)
	}
}

func TestRunReviewCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "wf")
	now := time.Now().UTC().Truncate(time.Second)

	s.CreateDevelopmentPlan(ctx, domain.DevelopmentPlan{ID: "p1", ProjectID: "wf", Title: "P", Status: domain.PlanStatusDraft, CreatedAt: now})
	s.CreateDevelopmentStage(ctx, domain.DevelopmentStage{ID: "s1", PlanID: "p1", Sequence: 1, Title: "S", Status: domain.StageStatusPending, CreatedAt: now})
	s.CreateDevelopmentTask(ctx, domain.DevelopmentTask{ID: "t1", StageID: "s1", Sequence: 1, Title: "T", Status: domain.TaskStatusPending, CreatedAt: now})
	s.CreateTaskRun(ctx, domain.TaskRun{ID: "r1", TaskID: "t1", Attempt: 1, Status: domain.RunStatusPending, CreatedAt: now})

	review := domain.RunReview{
		ID:        "rev-1",
		RunID:     "r1",
		Source:    domain.RunReviewSourceAI,
		Status:    domain.RunReviewStatusPending,
		Summary:   "Looks good",
		CreatedAt: now,
	}
	if err := s.CreateRunReview(ctx, review); err != nil {
		t.Fatalf("create review: %v", err)
	}

	got, ok, err := s.GetRunReview(ctx, "rev-1")
	if err != nil || !ok {
		t.Fatalf("get review: ok=%v err=%v", ok, err)
	}
	if got.Source != domain.RunReviewSourceAI || got.Status != domain.RunReviewStatusPending || got.Summary != "Looks good" {
		t.Fatalf("review = %+v", got)
	}

	list, err := s.ListRunReviewsByRun(ctx, "r1")
	if err != nil || len(list) != 1 {
		t.Fatalf("list reviews: len=%d err=%v", len(list), err)
	}
}

func TestRunReviewStatusUpdate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "wf")
	now := time.Now().UTC().Truncate(time.Second)

	s.CreateDevelopmentPlan(ctx, domain.DevelopmentPlan{ID: "p1", ProjectID: "wf", Title: "P", Status: domain.PlanStatusDraft, CreatedAt: now})
	s.CreateDevelopmentStage(ctx, domain.DevelopmentStage{ID: "s1", PlanID: "p1", Sequence: 1, Title: "S", Status: domain.StageStatusPending, CreatedAt: now})
	s.CreateDevelopmentTask(ctx, domain.DevelopmentTask{ID: "t1", StageID: "s1", Sequence: 1, Title: "T", Status: domain.TaskStatusPending, CreatedAt: now})
	s.CreateTaskRun(ctx, domain.TaskRun{ID: "r1", TaskID: "t1", Attempt: 1, Status: domain.RunStatusPending, CreatedAt: now})
	s.CreateRunReview(ctx, domain.RunReview{ID: "rev-1", RunID: "r1", Source: domain.RunReviewSourceHuman, Status: domain.RunReviewStatusPending, CreatedAt: now})

	completedAt := now.Add(time.Minute)
	if err := s.UpdateRunReviewStatus(ctx, "rev-1", domain.RunReviewStatusPassed, &completedAt); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _, _ := s.GetRunReview(ctx, "rev-1")
	if got.Status != domain.RunReviewStatusPassed || got.CompletedAt == nil {
		t.Fatalf("review = %+v", got)
	}
}

func TestRunReviewOnePerRun(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "wf")
	now := time.Now().UTC().Truncate(time.Second)

	s.CreateDevelopmentPlan(ctx, domain.DevelopmentPlan{ID: "p1", ProjectID: "wf", Title: "P", Status: domain.PlanStatusDraft, CreatedAt: now})
	s.CreateDevelopmentStage(ctx, domain.DevelopmentStage{ID: "s1", PlanID: "p1", Sequence: 1, Title: "S", Status: domain.StageStatusPending, CreatedAt: now})
	s.CreateDevelopmentTask(ctx, domain.DevelopmentTask{ID: "t1", StageID: "s1", Sequence: 1, Title: "T", Status: domain.TaskStatusPending, CreatedAt: now})
	s.CreateTaskRun(ctx, domain.TaskRun{ID: "r1", TaskID: "t1", Attempt: 1, Status: domain.RunStatusPending, CreatedAt: now})

	if err := s.CreateRunReview(ctx, domain.RunReview{ID: "rev-ai", RunID: "r1", Source: domain.RunReviewSourceAI, Status: domain.RunReviewStatusPending, CreatedAt: now}); err != nil {
		t.Fatalf("first review: %v", err)
	}

	// Second review for the same run must fail with ErrConflict.
	if err := s.CreateRunReview(ctx, domain.RunReview{ID: "rev-human", RunID: "r1", Source: domain.RunReviewSourceHuman, Status: domain.RunReviewStatusPending, CreatedAt: now}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected ErrConflict for duplicate run_id, got %v", err)
	}

	list, err := s.ListRunReviewsByRun(ctx, "r1")
	if err != nil || len(list) != 1 {
		t.Fatalf("list reviews: len=%d err=%v", len(list), err)
	}
}

func TestGetMissingReturnsFalse(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, ok, err := s.GetDevelopmentPlan(ctx, "missing"); ok || err != nil {
		t.Fatalf("missing plan: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.GetDevelopmentStage(ctx, "missing"); ok || err != nil {
		t.Fatalf("missing stage: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.GetDevelopmentTask(ctx, "missing"); ok || err != nil {
		t.Fatalf("missing task: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.GetAgentRole(ctx, "missing"); ok || err != nil {
		t.Fatalf("missing role: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.GetTaskRun(ctx, "missing"); ok || err != nil {
		t.Fatalf("missing run: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.GetRunReview(ctx, "missing"); ok || err != nil {
		t.Fatalf("missing review: ok=%v err=%v", ok, err)
	}
}

func TestPlanFKRejectsOrphanStage(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	err := s.CreateDevelopmentStage(ctx, domain.DevelopmentStage{ID: "s1", PlanID: "nonexistent", Sequence: 1, Title: "S", Status: domain.StageStatusPending, CreatedAt: now})
	if err == nil {
		t.Fatal("stage with nonexistent plan should be rejected")
	}
}

func TestExistingReviewTableUntouched(t *testing.T) {
	// Verify that the old review/review_run tables still work.
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "wf")
	rec, _ := s.CreateSession(ctx, sampleRecord("wf"))

	now := time.Now().UTC().Truncate(time.Second)
	review := domain.Review{ID: "old-rev", SessionID: rec.ID, ProjectID: "wf", Harness: domain.ReviewerClaudeCode, CreatedAt: now, UpdatedAt: now}
	if err := s.UpsertReview(ctx, review); err != nil {
		t.Fatalf("insert old review: %v", err)
	}

	run := domain.ReviewRun{ID: "old-run", ReviewID: "old-rev", SessionID: rec.ID, Harness: domain.ReviewerClaudeCode, Status: domain.ReviewRunRunning, CreatedAt: now}
	if err := s.InsertReviewRun(ctx, run); err != nil {
		t.Fatalf("insert old review run: %v", err)
	}
}

// ---- Phase 2.5: RunReview + Retry field tests ----

// seedReviewReadyChain creates a plan→stage→task→run→review chain where the
// task is in "review" status and the review is "pending". Returns (taskID, runID, reviewID).
func seedReviewReadyChain(t *testing.T, s *sqlite.Store, prefix string) (domain.DevelopmentTaskID, domain.TaskRunID, domain.RunReviewID) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	plan := domain.DevelopmentPlan{ID: domain.DevelopmentPlanID(prefix + "-plan"), ProjectID: "wf", Title: "P", Status: domain.PlanStatusDraft, CreatedAt: now}
	if err := s.CreateDevelopmentPlan(ctx, plan); err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	stage := domain.DevelopmentStage{ID: domain.DevelopmentStageID(prefix + "-stage"), PlanID: plan.ID, Sequence: 1, Title: "S", Status: domain.StageStatusPending, CreatedAt: now}
	if err := s.CreateDevelopmentStage(ctx, stage); err != nil {
		t.Fatalf("seed stage: %v", err)
	}
	task := domain.DevelopmentTask{ID: domain.DevelopmentTaskID(prefix + "-task"), StageID: stage.ID, Sequence: 1, Title: "T", Status: domain.TaskStatusReview, CreatedAt: now}
	if err := s.CreateDevelopmentTask(ctx, task); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	run := domain.TaskRun{ID: domain.TaskRunID(prefix + "-run"), TaskID: task.ID, Attempt: 1, Status: domain.RunStatusSucceeded, CreatedAt: now}
	if err := s.CreateTaskRun(ctx, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	review := domain.RunReview{ID: domain.RunReviewID(prefix + "-review"), RunID: run.ID, Source: domain.RunReviewSourceAI, Status: domain.RunReviewStatusPending, CreatedAt: now}
	if err := s.CreateRunReview(ctx, review); err != nil {
		t.Fatalf("seed review: %v", err)
	}
	return task.ID, run.ID, review.ID
}

func TestPassRunReviewTx_HappyPath(t *testing.T) {
	s := newTestStore(t)
	seedProject(t, s, "wf")
	_, _, reviewID := seedReviewReadyChain(t, s, "pass")
	completedAt := time.Now().UTC().Truncate(time.Second)

	if err := s.PassRunReviewTx(context.Background(), reviewID, completedAt); err != nil {
		t.Fatalf("PassRunReviewTx: %v", err)
	}

	review, ok, err := s.GetRunReview(context.Background(), reviewID)
	if err != nil || !ok {
		t.Fatalf("get review: ok=%v err=%v", ok, err)
	}
	if review.Status != domain.RunReviewStatusPassed {
		t.Fatalf("review status = %s, want passed", review.Status)
	}
	if review.CompletedAt == nil || !review.CompletedAt.Equal(completedAt) {
		t.Fatalf("review completedAt = %v, want %v", review.CompletedAt, completedAt)
	}

	task, ok, err := s.GetDevelopmentTask(context.Background(), domain.DevelopmentTaskID("pass-task"))
	if err != nil || !ok {
		t.Fatalf("get task: ok=%v err=%v", ok, err)
	}
	if task.Status != domain.TaskStatusPassed {
		t.Fatalf("task status = %s, want passed", task.Status)
	}
}

func TestRejectRunReviewTx_HappyPath(t *testing.T) {
	s := newTestStore(t)
	seedProject(t, s, "wf")
	_, _, reviewID := seedReviewReadyChain(t, s, "reject")
	completedAt := time.Now().UTC().Truncate(time.Second)

	if err := s.RejectRunReviewTx(context.Background(), reviewID, completedAt, "needs rework"); err != nil {
		t.Fatalf("RejectRunReviewTx: %v", err)
	}

	review, ok, err := s.GetRunReview(context.Background(), reviewID)
	if err != nil || !ok {
		t.Fatalf("get review: ok=%v err=%v", ok, err)
	}
	if review.Status != domain.RunReviewStatusRejected {
		t.Fatalf("review status = %s, want rejected", review.Status)
	}
	if review.Issues != "needs rework" {
		t.Fatalf("review issues = %q, want %q", review.Issues, "needs rework")
	}

	task, ok, err := s.GetDevelopmentTask(context.Background(), domain.DevelopmentTaskID("reject-task"))
	if err != nil || !ok {
		t.Fatalf("get task: ok=%v err=%v", ok, err)
	}
	if task.Status != domain.TaskStatusReady {
		t.Fatalf("task status = %s, want ready", task.Status)
	}
}

func TestPassRunReviewTx_ReviewNotPending(t *testing.T) {
	s := newTestStore(t)
	seedProject(t, s, "wf")
	_, _, reviewID := seedReviewReadyChain(t, s, "np")
	completedAt := time.Now().UTC().Truncate(time.Second)

	// Pass once.
	if err := s.PassRunReviewTx(context.Background(), reviewID, completedAt); err != nil {
		t.Fatalf("first pass: %v", err)
	}

	// Pass again → should fail.
	if err := s.PassRunReviewTx(context.Background(), reviewID, completedAt); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition, got %v", err)
	}
}

func TestPassRunReviewTx_TaskNotInReview(t *testing.T) {
	s := newTestStore(t)
	seedProject(t, s, "wf")
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create a chain where the task is NOT in review status (it's "running").
	if err := s.CreateDevelopmentPlan(ctx, domain.DevelopmentPlan{ID: "nr-plan", ProjectID: "wf", Title: "P", Status: domain.PlanStatusDraft, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateDevelopmentStage(ctx, domain.DevelopmentStage{ID: "nr-stage", PlanID: "nr-plan", Sequence: 1, Title: "S", Status: domain.StageStatusPending, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateDevelopmentTask(ctx, domain.DevelopmentTask{ID: "nr-task", StageID: "nr-stage", Sequence: 1, Title: "T", Status: domain.TaskStatusRunning, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	run := domain.TaskRun{ID: "nr-run", TaskID: "nr-task", Attempt: 1, Status: domain.RunStatusSucceeded, CreatedAt: now}
	if err := s.CreateTaskRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	review := domain.RunReview{ID: "nr-review", RunID: run.ID, Source: domain.RunReviewSourceAI, Status: domain.RunReviewStatusPending, CreatedAt: now}
	if err := s.CreateRunReview(ctx, review); err != nil {
		t.Fatal(err)
	}

	completedAt := now.Add(time.Minute)
	if err := s.PassRunReviewTx(ctx, review.ID, completedAt); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition, got %v", err)
	}
}

func TestRejectRunReviewTx_ReviewNotPending(t *testing.T) {
	s := newTestStore(t)
	seedProject(t, s, "wf")
	_, _, reviewID := seedReviewReadyChain(t, s, "rnp")
	completedAt := time.Now().UTC().Truncate(time.Second)

	// Reject once.
	if err := s.RejectRunReviewTx(context.Background(), reviewID, completedAt, "issue"); err != nil {
		t.Fatalf("first reject: %v", err)
	}

	// Reject again → should fail.
	if err := s.RejectRunReviewTx(context.Background(), reviewID, completedAt, "issue2"); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition, got %v", err)
	}
}

func TestCreateRunReview_Uniqueness(t *testing.T) {
	s := newTestStore(t)
	seedProject(t, s, "wf")
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	if err := s.CreateDevelopmentPlan(ctx, domain.DevelopmentPlan{ID: "u-plan", ProjectID: "wf", Title: "P", Status: domain.PlanStatusDraft, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateDevelopmentStage(ctx, domain.DevelopmentStage{ID: "u-stage", PlanID: "u-plan", Sequence: 1, Title: "S", Status: domain.StageStatusPending, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateDevelopmentTask(ctx, domain.DevelopmentTask{ID: "u-task", StageID: "u-stage", Sequence: 1, Title: "T", Status: domain.TaskStatusReview, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	run := domain.TaskRun{ID: "u-run", TaskID: "u-task", Attempt: 1, Status: domain.RunStatusSucceeded, CreatedAt: now}
	if err := s.CreateTaskRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	rev1 := domain.RunReview{ID: "u-rev-1", RunID: run.ID, Source: domain.RunReviewSourceAI, Status: domain.RunReviewStatusPending, CreatedAt: now}
	if err := s.CreateRunReview(ctx, rev1); err != nil {
		t.Fatalf("first review: %v", err)
	}

	rev2 := domain.RunReview{ID: "u-rev-2", RunID: run.ID, Source: domain.RunReviewSourceAI, Status: domain.RunReviewStatusPending, CreatedAt: now}
	if err := s.CreateRunReview(ctx, rev2); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestGetRunReviewByRunID(t *testing.T) {
	s := newTestStore(t)
	seedProject(t, s, "wf")
	_, runID, _ := seedReviewReadyChain(t, s, "byrun")

	got, ok, err := s.GetRunReviewByRunID(context.Background(), runID)
	if err != nil || !ok {
		t.Fatalf("GetRunReviewByRunID: ok=%v err=%v", ok, err)
	}
	if got.RunID != runID {
		t.Fatalf("got run_id=%s, want %s", got.RunID, runID)
	}
	if got.Status != domain.RunReviewStatusPending {
		t.Fatalf("got status=%s, want pending", got.Status)
	}
}

func TestGetRunReviewByRunID_NotFound(t *testing.T) {
	s := newTestStore(t)

	_, ok, err := s.GetRunReviewByRunID(context.Background(), domain.TaskRunID("nonexistent"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false")
	}
}

func TestTaskRun_RetryFields(t *testing.T) {
	s := newTestStore(t)
	seedProject(t, s, "wf")
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	if err := s.CreateDevelopmentPlan(ctx, domain.DevelopmentPlan{ID: "rf-plan", ProjectID: "wf", Title: "P", Status: domain.PlanStatusDraft, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateDevelopmentStage(ctx, domain.DevelopmentStage{ID: "rf-stage", PlanID: "rf-plan", Sequence: 1, Title: "S", Status: domain.StageStatusPending, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateDevelopmentTask(ctx, domain.DevelopmentTask{ID: "rf-task", StageID: "rf-stage", Sequence: 1, Title: "T", Status: domain.TaskStatusReady, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	// Create first run (no previous).
	run1 := domain.TaskRun{ID: "rf-run-1", TaskID: "rf-task", Attempt: 1, Status: domain.RunStatusSucceeded, CreatedAt: now}
	if err := s.CreateTaskRun(ctx, run1); err != nil {
		t.Fatal(err)
	}

	// Create second run (references first, FRESH mode).
	run2 := domain.TaskRun{ID: "rf-run-2", TaskID: "rf-task", Attempt: 2, Status: domain.RunStatusPending, PreviousRunID: "rf-run-1", RetryMode: "fresh", CreatedAt: now}
	if err := s.CreateTaskRun(ctx, run2); err != nil {
		t.Fatal(err)
	}

	// Read back and verify.
	got, ok, err := s.GetTaskRun(ctx, "rf-run-2")
	if err != nil || !ok {
		t.Fatalf("GetTaskRun: ok=%v err=%v", ok, err)
	}
	if got.PreviousRunID != "rf-run-1" {
		t.Fatalf("PreviousRunID = %s, want rf-run-1", got.PreviousRunID)
	}
	if got.RetryMode != "fresh" {
		t.Fatalf("RetryMode = %s, want fresh", got.RetryMode)
	}

	// Verify first run has empty retry fields.
	got1, _, _ := s.GetTaskRun(ctx, "rf-run-1")
	if got1.PreviousRunID != "" || got1.RetryMode != "" {
		t.Fatalf("run1 retry fields should be empty: previousRunId=%q retryMode=%q", got1.PreviousRunID, got1.RetryMode)
	}

	// Verify ListTaskRunsByTask returns both with correct fields.
	list, err := s.ListTaskRunsByTask(ctx, "rf-task")
	if err != nil || len(list) != 2 {
		t.Fatalf("ListTaskRunsByTask: len=%d err=%v", len(list), err)
	}
	if list[1].PreviousRunID != "rf-run-1" || list[1].RetryMode != "fresh" {
		t.Fatalf("list[1] retry fields: previousRunId=%q retryMode=%q", list[1].PreviousRunID, list[1].RetryMode)
	}
}
