package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/workflow"
)

// ---------------------------------------------------------------------------
// Mock WorkflowService
// ---------------------------------------------------------------------------

type mockWorkflowService struct {
	// Plan
	createPlanFn         func(ctx context.Context, in workflow.CreatePlanInput) (domain.DevelopmentPlan, error)
	getPlanFn            func(ctx context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error)
	listPlansByProjectFn func(ctx context.Context, projectID domain.ProjectID) ([]domain.DevelopmentPlan, error)
	updatePlanContentFn  func(ctx context.Context, id domain.DevelopmentPlanID, in workflow.UpdatePlanContentInput) (domain.DevelopmentPlan, error)
	confirmPlanFn        func(ctx context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error)
	startPlanFn          func(ctx context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error)
	completePlanFn       func(ctx context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error)
	cancelPlanFn         func(ctx context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error)
	// Stage
	createStageFn       func(ctx context.Context, in workflow.CreateStageInput) (domain.DevelopmentStage, error)
	getStageFn          func(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error)
	listStagesByPlanFn  func(ctx context.Context, planID domain.DevelopmentPlanID) ([]domain.DevelopmentStage, error)
	startStageFn        func(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error)
	readyForApprovalFn  func(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error)
	passStageFn         func(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error)
	blockStageFn        func(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error)
	unblockStageFn      func(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error)
	cancelStageFn       func(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error)
	// Task
	createTaskFn        func(ctx context.Context, in workflow.CreateTaskInput) (domain.DevelopmentTask, error)
	getTaskFn           func(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error)
	listTasksByStageFn  func(ctx context.Context, stageID domain.DevelopmentStageID) ([]domain.DevelopmentTask, error)
	readyTaskFn         func(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error)
	startTaskFn         func(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error)
	submitForReviewFn   func(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error)
	passTaskFn          func(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error)
	rejectTaskFn        func(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error)
	blockTaskFn         func(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error)
	unblockTaskFn       func(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error)
	cancelTaskFn        func(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error)
	assignTaskFn        func(ctx context.Context, id domain.DevelopmentTaskID, roleID domain.AgentRoleID, providerID domain.ProviderID, modelID domain.ProviderModelID) (domain.DevelopmentTask, error)
}

func (m *mockWorkflowService) CreatePlan(ctx context.Context, in workflow.CreatePlanInput) (domain.DevelopmentPlan, error) {
	return m.createPlanFn(ctx, in)
}
func (m *mockWorkflowService) GetPlan(ctx context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error) {
	return m.getPlanFn(ctx, id)
}
func (m *mockWorkflowService) ListPlansByProject(ctx context.Context, projectID domain.ProjectID) ([]domain.DevelopmentPlan, error) {
	return m.listPlansByProjectFn(ctx, projectID)
}
func (m *mockWorkflowService) UpdatePlanContent(ctx context.Context, id domain.DevelopmentPlanID, in workflow.UpdatePlanContentInput) (domain.DevelopmentPlan, error) {
	return m.updatePlanContentFn(ctx, id, in)
}
func (m *mockWorkflowService) ConfirmPlan(ctx context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error) {
	return m.confirmPlanFn(ctx, id)
}
func (m *mockWorkflowService) StartPlan(ctx context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error) {
	return m.startPlanFn(ctx, id)
}
func (m *mockWorkflowService) CompletePlan(ctx context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error) {
	return m.completePlanFn(ctx, id)
}
func (m *mockWorkflowService) CancelPlan(ctx context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error) {
	return m.cancelPlanFn(ctx, id)
}
func (m *mockWorkflowService) CreateStage(ctx context.Context, in workflow.CreateStageInput) (domain.DevelopmentStage, error) {
	return m.createStageFn(ctx, in)
}
func (m *mockWorkflowService) GetStage(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error) {
	return m.getStageFn(ctx, id)
}
func (m *mockWorkflowService) ListStagesByPlan(ctx context.Context, planID domain.DevelopmentPlanID) ([]domain.DevelopmentStage, error) {
	return m.listStagesByPlanFn(ctx, planID)
}
func (m *mockWorkflowService) StartStage(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error) {
	return m.startStageFn(ctx, id)
}
func (m *mockWorkflowService) ReadyForApproval(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error) {
	return m.readyForApprovalFn(ctx, id)
}
func (m *mockWorkflowService) PassStage(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error) {
	return m.passStageFn(ctx, id)
}
func (m *mockWorkflowService) BlockStage(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error) {
	return m.blockStageFn(ctx, id)
}
func (m *mockWorkflowService) UnblockStage(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error) {
	return m.unblockStageFn(ctx, id)
}
func (m *mockWorkflowService) CancelStage(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error) {
	return m.cancelStageFn(ctx, id)
}
func (m *mockWorkflowService) CreateTask(ctx context.Context, in workflow.CreateTaskInput) (domain.DevelopmentTask, error) {
	return m.createTaskFn(ctx, in)
}
func (m *mockWorkflowService) GetTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
	return m.getTaskFn(ctx, id)
}
func (m *mockWorkflowService) ListTasksByStage(ctx context.Context, stageID domain.DevelopmentStageID) ([]domain.DevelopmentTask, error) {
	return m.listTasksByStageFn(ctx, stageID)
}
func (m *mockWorkflowService) ReadyTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
	return m.readyTaskFn(ctx, id)
}
func (m *mockWorkflowService) StartTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
	return m.startTaskFn(ctx, id)
}
func (m *mockWorkflowService) SubmitForReview(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
	return m.submitForReviewFn(ctx, id)
}
func (m *mockWorkflowService) PassTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
	return m.passTaskFn(ctx, id)
}
func (m *mockWorkflowService) RejectTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
	return m.rejectTaskFn(ctx, id)
}
func (m *mockWorkflowService) BlockTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
	return m.blockTaskFn(ctx, id)
}
func (m *mockWorkflowService) UnblockTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
	return m.unblockTaskFn(ctx, id)
}
func (m *mockWorkflowService) CancelTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
	return m.cancelTaskFn(ctx, id)
}
func (m *mockWorkflowService) AssignTask(ctx context.Context, id domain.DevelopmentTaskID, roleID domain.AgentRoleID, providerID domain.ProviderID, modelID domain.ProviderModelID) (domain.DevelopmentTask, error) {
	return m.assignTaskFn(ctx, id, roleID, providerID, modelID)
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

var testNow = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

func samplePlan() domain.DevelopmentPlan {
	return domain.DevelopmentPlan{
		ID:        "plan-1",
		ProjectID: "proj-1",
		Title:     "Test Plan",
		Objective: "Build API",
		Status:    domain.PlanStatusDraft,
		CreatedAt: testNow,
	}
}

func sampleStage() domain.DevelopmentStage {
	return domain.DevelopmentStage{
		ID:        "stage-1",
		PlanID:    "plan-1",
		Sequence:  1,
		Title:     "Stage 1",
		Status:    domain.StageStatusPending,
		CreatedAt: testNow,
	}
}

func sampleTask() domain.DevelopmentTask {
	return domain.DevelopmentTask{
		ID:        "task-1",
		StageID:   "stage-1",
		Sequence:  1,
		Title:     "Task 1",
		Status:    domain.TaskStatusPending,
		CreatedAt: testNow,
	}
}

func newTestRouter(mock *mockWorkflowService) *chi.Mux {
	r := chi.NewRouter()
	ctrl := &WorkflowController{Svc: mock}
	ctrl.Register(r)
	return r
}

func doJSON(method, path string, body any) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	r := httptest.NewRequest(method, path, &buf)
	r.Header.Set("Content-Type", "application/json")
	return r
}

func decodeResp(t *testing.T, rr *httptest.ResponseRecorder, out any) {
	t.Helper()
	if err := json.NewDecoder(rr.Body).Decode(out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Plan tests
// ---------------------------------------------------------------------------

func TestCreatePlan(t *testing.T) {
	plan := samplePlan()
	mock := &mockWorkflowService{
		createPlanFn: func(_ context.Context, in workflow.CreatePlanInput) (domain.DevelopmentPlan, error) {
			if in.Title == "" {
				return domain.DevelopmentPlan{}, workflow.ErrInvalidInput
			}
			return plan, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/plans", CreatePlanRequest{
		ProjectID: "proj-1",
		Title:     "Test Plan",
	}))
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp PlanResponse
	decodeResp(t, rr, &resp)
	if resp.Plan.ID != "plan-1" || resp.Plan.Status != "draft" {
		t.Fatalf("unexpected response: %+v", resp.Plan)
	}
}

func TestCreatePlan_InvalidInput(t *testing.T) {
	mock := &mockWorkflowService{
		createPlanFn: func(_ context.Context, in workflow.CreatePlanInput) (domain.DevelopmentPlan, error) {
			return domain.DevelopmentPlan{}, workflow.ErrInvalidInput
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/plans", CreatePlanRequest{
		ProjectID: "proj-1",
		Title:     "",
	}))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestCreatePlan_NotFound(t *testing.T) {
	mock := &mockWorkflowService{
		createPlanFn: func(_ context.Context, in workflow.CreatePlanInput) (domain.DevelopmentPlan, error) {
			return domain.DevelopmentPlan{}, workflow.ErrNotFound
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/plans", CreatePlanRequest{
		ProjectID: "missing",
		Title:     "Test",
	}))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestGetPlan(t *testing.T) {
	plan := samplePlan()
	plan.Status = domain.PlanStatusConfirmed
	confirmedAt := testNow
	plan.ConfirmedAt = &confirmedAt
	mock := &mockWorkflowService{
		getPlanFn: func(_ context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error) {
			if id == "plan-1" {
				return plan, nil
			}
			return domain.DevelopmentPlan{}, workflow.ErrNotFound
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest("GET", "/workflow/plans/plan-1", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp PlanResponse
	decodeResp(t, rr, &resp)
	if resp.Plan.Status != "confirmed" || resp.Plan.ConfirmedAt == nil {
		t.Fatalf("unexpected response: %+v", resp.Plan)
	}
}

func TestGetPlan_NotFound(t *testing.T) {
	mock := &mockWorkflowService{
		getPlanFn: func(_ context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error) {
			return domain.DevelopmentPlan{}, workflow.ErrNotFound
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest("GET", "/workflow/plans/missing", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestListPlansByProject(t *testing.T) {
	mock := &mockWorkflowService{
		listPlansByProjectFn: func(_ context.Context, pid domain.ProjectID) ([]domain.DevelopmentPlan, error) {
			return []domain.DevelopmentPlan{samplePlan()}, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest("GET", "/projects/proj-1/plans", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp ListPlansResponse
	decodeResp(t, rr, &resp)
	if len(resp.Plans) != 1 || resp.Plans[0].ID != "plan-1" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestUpdatePlanContent(t *testing.T) {
	current := samplePlan()
	current.Title = "Old Title"
	current.Objective = "Old Objective"
	mock := &mockWorkflowService{
		getPlanFn: func(_ context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error) {
			if id == "plan-1" {
				return current, nil
			}
			return domain.DevelopmentPlan{}, workflow.ErrNotFound
		},
		updatePlanContentFn: func(_ context.Context, id domain.DevelopmentPlanID, in workflow.UpdatePlanContentInput) (domain.DevelopmentPlan, error) {
			if id != "plan-1" {
				return domain.DevelopmentPlan{}, workflow.ErrNotFound
			}
			plan := current
			plan.Title = in.Title
			plan.Objective = in.Objective
			plan.Requirements = in.Requirements
			plan.ImplementationSummary = in.ImplementationSummary
			return plan, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	title := "Updated Title"
	r.ServeHTTP(rr, doJSON("PATCH", "/workflow/plans/plan-1", UpdatePlanRequest{
		Title: &title,
	}))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp PlanResponse
	decodeResp(t, rr, &resp)
	if resp.Plan.Title != "Updated Title" {
		t.Fatalf("expected title 'Updated Title', got %s", resp.Plan.Title)
	}
}

func TestUpdatePlanContent_PreservesOmittedFields(t *testing.T) {
	current := samplePlan()
	current.Title = "Keep Title"
	current.Objective = "Keep Objective"
	current.Requirements = "Keep Reqs"
	current.ImplementationSummary = "Keep Summary"
	var capturedInput workflow.UpdatePlanContentInput
	mock := &mockWorkflowService{
		getPlanFn: func(_ context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error) {
			return current, nil
		},
		updatePlanContentFn: func(_ context.Context, id domain.DevelopmentPlanID, in workflow.UpdatePlanContentInput) (domain.DevelopmentPlan, error) {
			capturedInput = in
			plan := current
			plan.Title = in.Title
			plan.Objective = in.Objective
			plan.Requirements = in.Requirements
			plan.ImplementationSummary = in.ImplementationSummary
			return plan, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	newTitle := "New Title"
	r.ServeHTTP(rr, doJSON("PATCH", "/workflow/plans/plan-1", UpdatePlanRequest{
		Title: &newTitle,
	}))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if capturedInput.Title != "New Title" {
		t.Fatalf("title should be updated, got %q", capturedInput.Title)
	}
	if capturedInput.Objective != "Keep Objective" {
		t.Fatalf("objective should be preserved, got %q", capturedInput.Objective)
	}
	if capturedInput.Requirements != "Keep Reqs" {
		t.Fatalf("requirements should be preserved, got %q", capturedInput.Requirements)
	}
	if capturedInput.ImplementationSummary != "Keep Summary" {
		t.Fatalf("implementationSummary should be preserved, got %q", capturedInput.ImplementationSummary)
	}
}

func TestUpdatePlanContent_AllowsOmittedTitle(t *testing.T) {
	current := samplePlan()
	current.Title = "Keep Title"
	current.Objective = "Old Objective"
	current.Requirements = "Keep Reqs"
	current.ImplementationSummary = "Keep Summary"
	var capturedInput workflow.UpdatePlanContentInput
	mock := &mockWorkflowService{
		getPlanFn: func(_ context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error) {
			return current, nil
		},
		updatePlanContentFn: func(_ context.Context, id domain.DevelopmentPlanID, in workflow.UpdatePlanContentInput) (domain.DevelopmentPlan, error) {
			capturedInput = in
			plan := current
			plan.Title = in.Title
			plan.Objective = in.Objective
			plan.Requirements = in.Requirements
			plan.ImplementationSummary = in.ImplementationSummary
			return plan, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	newObj := "New Objective"
	r.ServeHTTP(rr, doJSON("PATCH", "/workflow/plans/plan-1", UpdatePlanRequest{
		Objective: &newObj,
	}))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if capturedInput.Title != "Keep Title" {
		t.Fatalf("title should be preserved when omitted, got %q", capturedInput.Title)
	}
	if capturedInput.Objective != "New Objective" {
		t.Fatalf("objective should be updated, got %q", capturedInput.Objective)
	}
	if capturedInput.Requirements != "Keep Reqs" {
		t.Fatalf("requirements should be preserved, got %q", capturedInput.Requirements)
	}
	if capturedInput.ImplementationSummary != "Keep Summary" {
		t.Fatalf("implementationSummary should be preserved, got %q", capturedInput.ImplementationSummary)
	}
	var resp PlanResponse
	decodeResp(t, rr, &resp)
	if resp.Plan.Title != "Keep Title" {
		t.Fatalf("response title should be preserved, got %s", resp.Plan.Title)
	}
	if resp.Plan.Objective != "New Objective" {
		t.Fatalf("response objective should be updated, got %s", resp.Plan.Objective)
	}
}

func TestConfirmPlan(t *testing.T) {
	plan := samplePlan()
	plan.Status = domain.PlanStatusConfirmed
	confirmedAt := testNow
	plan.ConfirmedAt = &confirmedAt
	mock := &mockWorkflowService{
		confirmPlanFn: func(_ context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error) {
			return plan, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/plans/plan-1/confirm", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp PlanResponse
	decodeResp(t, rr, &resp)
	if resp.Plan.Status != "confirmed" {
		t.Fatalf("expected confirmed, got %s", resp.Plan.Status)
	}
}

func TestConfirmPlan_InvalidTransition(t *testing.T) {
	mock := &mockWorkflowService{
		confirmPlanFn: func(_ context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error) {
			return domain.DevelopmentPlan{}, workflow.ErrInvalidTransition
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/plans/plan-1/confirm", nil))
	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rr.Code)
	}
}

func TestStartPlan(t *testing.T) {
	mock := &mockWorkflowService{
		startPlanFn: func(_ context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error) {
			plan := samplePlan()
			plan.Status = domain.PlanStatusInProgress
			return plan, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/plans/plan-1/start", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestStartPlan_ChildrenIncomplete(t *testing.T) {
	mock := &mockWorkflowService{
		startPlanFn: func(_ context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error) {
			return domain.DevelopmentPlan{}, workflow.ErrChildrenIncomplete
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/plans/plan-1/start", nil))
	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rr.Code)
	}
}

func TestCompletePlan(t *testing.T) {
	plan := samplePlan()
	plan.Status = domain.PlanStatusCompleted
	completedAt := testNow
	plan.CompletedAt = &completedAt
	mock := &mockWorkflowService{
		completePlanFn: func(_ context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error) {
			return plan, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/plans/plan-1/complete", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp PlanResponse
	decodeResp(t, rr, &resp)
	if resp.Plan.Status != "completed" || resp.Plan.CompletedAt == nil {
		t.Fatalf("unexpected: %+v", resp.Plan)
	}
}

func TestCancelPlan(t *testing.T) {
	plan := samplePlan()
	plan.Status = domain.PlanStatusCancelled
	completedAt := testNow
	plan.CompletedAt = &completedAt
	mock := &mockWorkflowService{
		cancelPlanFn: func(_ context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error) {
			return plan, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/plans/plan-1/cancel", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp PlanResponse
	decodeResp(t, rr, &resp)
	if resp.Plan.Status != "cancelled" || resp.Plan.CompletedAt == nil {
		t.Fatalf("unexpected: %+v", resp.Plan)
	}
}

// ---------------------------------------------------------------------------
// Stage tests
// ---------------------------------------------------------------------------

func TestCreateStage(t *testing.T) {
	mock := &mockWorkflowService{
		createStageFn: func(_ context.Context, in workflow.CreateStageInput) (domain.DevelopmentStage, error) {
			return sampleStage(), nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/stages", CreateStageRequest{
		PlanID: "plan-1", Sequence: 1, Title: "Stage 1",
	}))
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rr.Code)
	}
}

func TestGetStage(t *testing.T) {
	mock := &mockWorkflowService{
		getStageFn: func(_ context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error) {
			return sampleStage(), nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest("GET", "/workflow/stages/stage-1", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestListStagesByPlan(t *testing.T) {
	mock := &mockWorkflowService{
		listStagesByPlanFn: func(_ context.Context, pid domain.DevelopmentPlanID) ([]domain.DevelopmentStage, error) {
			return []domain.DevelopmentStage{sampleStage()}, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest("GET", "/workflow/plans/plan-1/stages", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp ListStagesResponse
	decodeResp(t, rr, &resp)
	if len(resp.Stages) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(resp.Stages))
	}
}

func TestStartStage(t *testing.T) {
	mock := &mockWorkflowService{
		startStageFn: func(_ context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error) {
			s := sampleStage()
			s.Status = domain.StageStatusInProgress
			startedAt := testNow
			s.StartedAt = &startedAt
			return s, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/stages/stage-1/start", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestReadyForApproval(t *testing.T) {
	mock := &mockWorkflowService{
		readyForApprovalFn: func(_ context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error) {
			s := sampleStage()
			s.Status = domain.StageStatusReadyForApproval
			return s, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/stages/stage-1/ready", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestPassStage(t *testing.T) {
	mock := &mockWorkflowService{
		passStageFn: func(_ context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error) {
			s := sampleStage()
			s.Status = domain.StageStatusPassed
			completedAt := testNow
			s.CompletedAt = &completedAt
			return s, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/stages/stage-1/pass", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestBlockStage(t *testing.T) {
	mock := &mockWorkflowService{
		blockStageFn: func(_ context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error) {
			s := sampleStage()
			s.Status = domain.StageStatusBlocked
			return s, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/stages/stage-1/block", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp StageResponse
	decodeResp(t, rr, &resp)
	if resp.Stage.Status != "blocked" {
		t.Fatalf("expected blocked, got %s", resp.Stage.Status)
	}
}

func TestUnblockStage(t *testing.T) {
	mock := &mockWorkflowService{
		unblockStageFn: func(_ context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error) {
			s := sampleStage()
			s.Status = domain.StageStatusInProgress
			return s, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/stages/stage-1/unblock", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp StageResponse
	decodeResp(t, rr, &resp)
	if resp.Stage.Status != "in_progress" {
		t.Fatalf("expected in_progress, got %s", resp.Stage.Status)
	}
}

func TestCancelStage(t *testing.T) {
	mock := &mockWorkflowService{
		cancelStageFn: func(_ context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error) {
			s := sampleStage()
			s.Status = domain.StageStatusCancelled
			completedAt := testNow
			s.CompletedAt = &completedAt
			return s, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/stages/stage-1/cancel", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Task tests
// ---------------------------------------------------------------------------

func TestCreateTask(t *testing.T) {
	mock := &mockWorkflowService{
		createTaskFn: func(_ context.Context, in workflow.CreateTaskInput) (domain.DevelopmentTask, error) {
			return sampleTask(), nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/tasks", CreateTaskRequest{
		StageID: "stage-1", Sequence: 1, Title: "Task 1",
	}))
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rr.Code)
	}
}

func TestGetTask(t *testing.T) {
	mock := &mockWorkflowService{
		getTaskFn: func(_ context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
			return sampleTask(), nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest("GET", "/workflow/tasks/task-1", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestListTasksByStage(t *testing.T) {
	mock := &mockWorkflowService{
		listTasksByStageFn: func(_ context.Context, sid domain.DevelopmentStageID) ([]domain.DevelopmentTask, error) {
			return []domain.DevelopmentTask{sampleTask()}, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest("GET", "/workflow/stages/stage-1/tasks", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestReadyTask(t *testing.T) {
	mock := &mockWorkflowService{
		readyTaskFn: func(_ context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
			task := sampleTask()
			task.Status = domain.TaskStatusReady
			return task, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/tasks/task-1/ready", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestStartTask(t *testing.T) {
	mock := &mockWorkflowService{
		startTaskFn: func(_ context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
			task := sampleTask()
			task.Status = domain.TaskStatusRunning
			startedAt := testNow
			task.StartedAt = &startedAt
			return task, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/tasks/task-1/start", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp TaskResponse
	decodeResp(t, rr, &resp)
	if resp.Task.Status != "running" || resp.Task.StartedAt == nil {
		t.Fatalf("unexpected: %+v", resp.Task)
	}
}

func TestSubmitForReview(t *testing.T) {
	mock := &mockWorkflowService{
		submitForReviewFn: func(_ context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
			task := sampleTask()
			task.Status = domain.TaskStatusReview
			return task, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/tasks/task-1/review", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestPassTask(t *testing.T) {
	mock := &mockWorkflowService{
		passTaskFn: func(_ context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
			task := sampleTask()
			task.Status = domain.TaskStatusPassed
			completedAt := testNow
			task.CompletedAt = &completedAt
			return task, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/tasks/task-1/pass", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestRejectTask(t *testing.T) {
	mock := &mockWorkflowService{
		rejectTaskFn: func(_ context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
			task := sampleTask()
			task.Status = domain.TaskStatusReady
			return task, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/tasks/task-1/reject", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp TaskResponse
	decodeResp(t, rr, &resp)
	if resp.Task.Status != "ready" {
		t.Fatalf("expected status=ready after reject, got %s", resp.Task.Status)
	}
}

func TestBlockTask(t *testing.T) {
	mock := &mockWorkflowService{
		blockTaskFn: func(_ context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
			task := sampleTask()
			task.Status = domain.TaskStatusBlocked
			return task, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/tasks/task-1/block", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp TaskResponse
	decodeResp(t, rr, &resp)
	if resp.Task.Status != "blocked" {
		t.Fatalf("expected blocked, got %s", resp.Task.Status)
	}
}

func TestUnblockTask(t *testing.T) {
	mock := &mockWorkflowService{
		unblockTaskFn: func(_ context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
			task := sampleTask()
			task.Status = domain.TaskStatusReady
			return task, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/tasks/task-1/unblock", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp TaskResponse
	decodeResp(t, rr, &resp)
	if resp.Task.Status != "ready" {
		t.Fatalf("expected ready, got %s", resp.Task.Status)
	}
}

func TestCancelTask(t *testing.T) {
	mock := &mockWorkflowService{
		cancelTaskFn: func(_ context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
			task := sampleTask()
			task.Status = domain.TaskStatusCancelled
			completedAt := testNow
			task.CompletedAt = &completedAt
			return task, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("POST", "/workflow/tasks/task-1/cancel", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestAssignTask(t *testing.T) {
	mock := &mockWorkflowService{
		assignTaskFn: func(_ context.Context, id domain.DevelopmentTaskID, roleID domain.AgentRoleID, providerID domain.ProviderID, modelID domain.ProviderModelID) (domain.DevelopmentTask, error) {
			task := sampleTask()
			task.AgentRoleID = roleID
			task.ProviderID = providerID
			task.ProviderModelID = modelID
			return task, nil
		},
	}
	r := newTestRouter(mock)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, doJSON("PATCH", "/workflow/tasks/task-1/assignment", AssignTaskRequest{
		AgentRoleID: "role-1", ProviderID: "prov-1", ProviderModelID: "model-1",
	}))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp TaskResponse
	decodeResp(t, rr, &resp)
	if resp.Task.AgentRoleID != "role-1" || resp.Task.ProviderID != "prov-1" {
		t.Fatalf("unexpected: %+v", resp.Task)
	}
}

// ---------------------------------------------------------------------------
// Nil service (501) test
// ---------------------------------------------------------------------------

func TestWorkflowController_NilSvc(t *testing.T) {
	r := chi.NewRouter()
	ctrl := &WorkflowController{Svc: nil}
	ctrl.Register(r)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest("GET", "/workflow/plans/plan-1", nil))
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", rr.Code)
	}
}
