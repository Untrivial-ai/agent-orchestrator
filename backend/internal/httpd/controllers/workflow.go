package controllers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/workflow"
)

// WorkflowService is the interface the controller depends on. *workflow.Service
// satisfies it at compile time. It exists so the controller can be tested with
// a lightweight fake without modifying the service package.
type WorkflowService interface {
	// Plan
	CreatePlan(ctx context.Context, in workflow.CreatePlanInput) (domain.DevelopmentPlan, error)
	GetPlan(ctx context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error)
	ListPlansByProject(ctx context.Context, projectID domain.ProjectID) ([]domain.DevelopmentPlan, error)
	UpdatePlanContent(ctx context.Context, id domain.DevelopmentPlanID, in workflow.UpdatePlanContentInput) (domain.DevelopmentPlan, error)
	ConfirmPlan(ctx context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error)
	StartPlan(ctx context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error)
	CompletePlan(ctx context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error)
	CancelPlan(ctx context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error)
	// Stage
	CreateStage(ctx context.Context, in workflow.CreateStageInput) (domain.DevelopmentStage, error)
	GetStage(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error)
	ListStagesByPlan(ctx context.Context, planID domain.DevelopmentPlanID) ([]domain.DevelopmentStage, error)
	StartStage(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error)
	ReadyForApproval(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error)
	PassStage(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error)
	BlockStage(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error)
	UnblockStage(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error)
	CancelStage(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error)
	// Task
	CreateTask(ctx context.Context, in workflow.CreateTaskInput) (domain.DevelopmentTask, error)
	GetTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error)
	ListTasksByStage(ctx context.Context, stageID domain.DevelopmentStageID) ([]domain.DevelopmentTask, error)
	ReadyTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error)
	StartTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error)
	SubmitForReview(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error)
	PassTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error)
	RejectTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error)
	BlockTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error)
	UnblockTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error)
	CancelTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error)
	AssignTask(ctx context.Context, id domain.DevelopmentTaskID, roleID domain.AgentRoleID, providerID domain.ProviderID, modelID domain.ProviderModelID) (domain.DevelopmentTask, error)
	// Run (Phase 2.3)
	CreateRun(ctx context.Context, in workflow.CreateRunInput) (domain.TaskRun, error)
	GetRun(ctx context.Context, id domain.TaskRunID) (domain.TaskRun, error)
	ListRunsByTask(ctx context.Context, taskID domain.DevelopmentTaskID) ([]domain.TaskRun, error)
	StartRun(ctx context.Context, id domain.TaskRunID) (domain.TaskRun, error)
	CancelRun(ctx context.Context, id domain.TaskRunID) (domain.TaskRun, error)
	// AgentRole (Phase 2.4)
	CreateAgentRole(ctx context.Context, in workflow.CreateAgentRoleInput) (domain.AgentRole, error)
	GetAgentRole(ctx context.Context, id domain.AgentRoleID) (domain.AgentRole, error)
	ListAgentRoles(ctx context.Context) ([]domain.AgentRole, error)
	UpdateAgentRole(ctx context.Context, id domain.AgentRoleID, in workflow.UpdateAgentRoleInput) (domain.AgentRole, error)
	SetAgentRoleEnabled(ctx context.Context, id domain.AgentRoleID, enabled bool) error
}

// Compile-time check: *workflow.Service must satisfy WorkflowService.
var _ WorkflowService = (*workflow.Service)(nil)

// WorkflowController owns the /workflow routes for the development workflow
// lifecycle. Nil Svc keeps routes registered but returns 501s.
type WorkflowController struct {
	Svc WorkflowService
}

// Register mounts the workflow routes on the supplied router.
func (c *WorkflowController) Register(r chi.Router) {
	// Plan
	r.Post("/workflow/plans", c.createPlan)
	r.Get("/workflow/plans/{id}", c.getPlan)
	r.Patch("/workflow/plans/{id}", c.updatePlanContent)
	r.Post("/workflow/plans/{id}/confirm", c.confirmPlan)
	r.Post("/workflow/plans/{id}/start", c.startPlan)
	r.Post("/workflow/plans/{id}/complete", c.completePlan)
	r.Post("/workflow/plans/{id}/cancel", c.cancelPlan)

	// Plan → Stages (child)
	r.Get("/workflow/plans/{id}/stages", c.listStagesByPlan)

	// Stage
	r.Post("/workflow/stages", c.createStage)
	r.Get("/workflow/stages/{id}", c.getStage)
	r.Post("/workflow/stages/{id}/start", c.startStage)
	r.Post("/workflow/stages/{id}/ready", c.readyForApproval)
	r.Post("/workflow/stages/{id}/pass", c.passStage)
	r.Post("/workflow/stages/{id}/block", c.blockStage)
	r.Post("/workflow/stages/{id}/unblock", c.unblockStage)
	r.Post("/workflow/stages/{id}/cancel", c.cancelStage)

	// Stage → Tasks (child)
	r.Get("/workflow/stages/{id}/tasks", c.listTasksByStage)

	// Task
	r.Post("/workflow/tasks", c.createTask)
	r.Get("/workflow/tasks/{id}", c.getTask)
	r.Post("/workflow/tasks/{id}/ready", c.readyTask)
	r.Post("/workflow/tasks/{id}/start", c.startTask)
	r.Post("/workflow/tasks/{id}/review", c.submitForReview)
	r.Post("/workflow/tasks/{id}/pass", c.passTask)
	r.Post("/workflow/tasks/{id}/reject", c.rejectTask)
	r.Post("/workflow/tasks/{id}/block", c.blockTask)
	r.Post("/workflow/tasks/{id}/unblock", c.unblockTask)
	r.Post("/workflow/tasks/{id}/cancel", c.cancelTask)
	r.Patch("/workflow/tasks/{id}/assignment", c.assignTask)

	// Task → Runs (child)
	r.Get("/workflow/tasks/{id}/runs", c.listRunsByTask)

	// Run
	r.Post("/workflow/runs", c.createRun)
	r.Get("/workflow/runs/{id}", c.getRun)
	r.Post("/workflow/runs/{id}/start", c.startRun)
	r.Post("/workflow/runs/{id}/cancel", c.cancelRun)

	// AgentRole (Phase 2.4)
	r.Post("/workflow/roles", c.createAgentRole)
	r.Get("/workflow/roles", c.listAgentRoles)
	r.Get("/workflow/roles/{id}", c.getAgentRole)
	r.Patch("/workflow/roles/{id}", c.updateAgentRole)
	r.Patch("/workflow/roles/{id}/enabled", c.setAgentRoleEnabled)

	// Project child route
	r.Get("/projects/{id}/plans", c.listPlansByProject)
}

// ---------------------------------------------------------------------------
// Request DTOs
// ---------------------------------------------------------------------------

// CreatePlanRequest is the body of POST /workflow/plans.
type CreatePlanRequest struct {
	ProjectID             string `json:"projectId" description:"Parent project identifier."`
	Title                 string `json:"title" description:"Plan title."`
	Objective             string `json:"objective,omitempty" description:"Plan objective."`
	Requirements          string `json:"requirements,omitempty" description:"Requirements text."`
	ImplementationSummary string `json:"implementationSummary,omitempty" description:"Implementation summary."`
}

// UpdatePlanRequest is the body of PATCH /workflow/plans/{id}.
// Pointer fields distinguish omitted (nil → keep current) from explicitly
// empty ("", non-nil → clear). This preserves true PATCH partial-update
// semantics without requiring the Service layer to change.
type UpdatePlanRequest struct {
	Title                 *string `json:"title,omitempty" description:"Plan title."`
	Objective             *string `json:"objective,omitempty" description:"Plan objective."`
	Requirements          *string `json:"requirements,omitempty" description:"Requirements text."`
	ImplementationSummary *string `json:"implementationSummary,omitempty" description:"Implementation summary."`
}

// CreateStageRequest is the body of POST /workflow/stages.
type CreateStageRequest struct {
	PlanID             string `json:"planId" description:"Parent plan identifier."`
	Sequence           int    `json:"sequence" description:"Ordering within the plan."`
	Title              string `json:"title" description:"Stage title."`
	Description        string `json:"description,omitempty"`
	AcceptanceCriteria string `json:"acceptanceCriteria,omitempty"`
}

// CreateTaskRequest is the body of POST /workflow/tasks.
type CreateTaskRequest struct {
	StageID            string `json:"stageId" description:"Parent stage identifier."`
	Sequence           int    `json:"sequence" description:"Ordering within the stage."`
	Title              string `json:"title" description:"Task title."`
	Description        string `json:"description,omitempty"`
	TaskType           string `json:"taskType,omitempty"`
	AcceptanceCriteria string `json:"acceptanceCriteria,omitempty"`
	AgentRoleID        string `json:"agentRoleId,omitempty"`
	ProviderID         string `json:"providerId,omitempty"`
	ProviderModelID    string `json:"providerModelId,omitempty"`
}

// AssignTaskRequest is the body of PATCH /workflow/tasks/{id}/assignment.
type AssignTaskRequest struct {
	AgentRoleID     string `json:"agentRoleId" description:"Agent role identifier."`
	ProviderID      string `json:"providerId" description:"Provider identifier."`
	ProviderModelID string `json:"providerModelId" description:"Provider model identifier."`
}

// CreateRunRequest is the body of POST /workflow/runs.
// Phase 2.4: Run-level Provider/AgentRole override removed.
// AgentRoleID and Provider/Model come from the Task.
type CreateRunRequest struct {
	TaskID string `json:"taskId" description:"Parent task identifier."`
}

// CreateAgentRoleRequest is the body of POST /workflow/roles.
type CreateAgentRoleRequest struct {
	Name                   string `json:"name"`
	DisplayName            string `json:"displayName,omitempty"`
	Description            string `json:"description,omitempty"`
	SystemPrompt           string `json:"systemPrompt,omitempty"`
	DefaultProviderID      string `json:"defaultProviderId,omitempty"`
	DefaultProviderModelID string `json:"defaultProviderModelId,omitempty"`
}

// UpdateAgentRoleRequest is the body of PATCH /workflow/roles/{id}.
type UpdateAgentRoleRequest struct {
	DisplayName            *string `json:"displayName,omitempty"`
	Description            *string `json:"description,omitempty"`
	SystemPrompt           *string `json:"systemPrompt,omitempty"`
	DefaultProviderID      *string `json:"defaultProviderId,omitempty"`
	DefaultProviderModelID *string `json:"defaultProviderModelId,omitempty"`
}

// SetAgentRoleEnabledRequest is the body of PATCH /workflow/roles/{id}/enabled.
type SetAgentRoleEnabledRequest struct {
	Enabled bool `json:"enabled"`
}

// ---------------------------------------------------------------------------
// Response DTOs
// ---------------------------------------------------------------------------

// PlanView is the wire representation of a DevelopmentPlan.
type PlanView struct {
	ID                    string  `json:"id" description:"Plan identifier."`
	ProjectID             string  `json:"projectId" description:"Parent project identifier."`
	Title                 string  `json:"title"`
	Objective             string  `json:"objective"`
	Requirements          string  `json:"requirements"`
	ImplementationSummary string  `json:"implementationSummary"`
	Status                string  `json:"status" enum:"draft,confirmed,in_progress,completed,cancelled"`
	CreatedAt             string  `json:"createdAt" format:"date-time"`
	ConfirmedAt           *string `json:"confirmedAt,omitempty" format:"date-time"`
	CompletedAt           *string `json:"completedAt,omitempty" format:"date-time"`
}

// PlanResponse wraps a single plan.
type PlanResponse struct {
	Plan PlanView `json:"plan"`
}

// ListPlansResponse wraps a list of plans.
type ListPlansResponse struct {
	Plans []PlanView `json:"plans"`
}

// StageView is the wire representation of a DevelopmentStage.
type StageView struct {
	ID                 string  `json:"id" description:"Stage identifier."`
	PlanID             string  `json:"planId" description:"Parent plan identifier."`
	Sequence           int     `json:"sequence"`
	Title              string  `json:"title"`
	Description        string  `json:"description"`
	AcceptanceCriteria string  `json:"acceptanceCriteria"`
	Status             string  `json:"status" enum:"pending,in_progress,ready_for_approval,passed,blocked,cancelled"`
	CreatedAt          string  `json:"createdAt" format:"date-time"`
	StartedAt          *string `json:"startedAt,omitempty" format:"date-time"`
	CompletedAt        *string `json:"completedAt,omitempty" format:"date-time"`
}

// StageResponse wraps a single stage.
type StageResponse struct {
	Stage StageView `json:"stage"`
}

// ListStagesResponse wraps a list of stages.
type ListStagesResponse struct {
	Stages []StageView `json:"stages"`
}

// TaskView is the wire representation of a DevelopmentTask.
type TaskView struct {
	ID                 string  `json:"id" description:"Task identifier."`
	StageID            string  `json:"stageId" description:"Parent stage identifier."`
	Sequence           int     `json:"sequence"`
	Title              string  `json:"title"`
	Description        string  `json:"description"`
	TaskType           string  `json:"taskType"`
	AcceptanceCriteria string  `json:"acceptanceCriteria"`
	Status             string  `json:"status" enum:"pending,ready,running,review,passed,blocked,cancelled"`
	AgentRoleID        string  `json:"agentRoleId"`
	ProviderID         string  `json:"providerId"`
	ProviderModelID    string  `json:"providerModelId"`
	CreatedAt          string  `json:"createdAt" format:"date-time"`
	StartedAt          *string `json:"startedAt,omitempty" format:"date-time"`
	CompletedAt        *string `json:"completedAt,omitempty" format:"date-time"`
}

// TaskResponse wraps a single task.
type TaskResponse struct {
	Task TaskView `json:"task"`
}

// ListTasksResponse wraps a list of tasks.
type ListTasksResponse struct {
	Tasks []TaskView `json:"tasks"`
}

// RunView is the wire representation of a TaskRun.
type RunView struct {
	ID                string  `json:"id" description:"Run identifier."`
	TaskID            string  `json:"taskId" description:"Parent task identifier."`
	Attempt           int     `json:"attempt"`
	SessionID         string  `json:"sessionId,omitempty"`
	AgentRoleID       string  `json:"agentRoleId,omitempty"`
	ProviderID        string  `json:"providerId,omitempty"`
	ProviderModelID   string  `json:"providerModelId,omitempty"`
	ProviderDisplayName string `json:"providerDisplayName,omitempty"`
	ProviderModelName string  `json:"providerModelName,omitempty"`
	ExecutorType      string  `json:"executorType,omitempty"`
	Status            string  `json:"status" enum:"pending,running,succeeded,failed,cancelled"`
	ResultSummary     string  `json:"resultSummary,omitempty"`
	ErrorMessage      string  `json:"errorMessage,omitempty"`
	CreatedAt         string  `json:"createdAt" format:"date-time"`
	StartedAt         *string `json:"startedAt,omitempty" format:"date-time"`
	FinishedAt        *string `json:"finishedAt,omitempty" format:"date-time"`
}

// RunResponse wraps a single run.
type RunResponse struct {
	Run RunView `json:"run"`
}

// ListRunsResponse wraps a list of runs.
type ListRunsResponse struct {
	Runs []RunView `json:"runs"`
}

// WorkflowIDParam is the {id} path parameter for /workflow/*/{id} routes.
type WorkflowIDParam struct {
	ID string `path:"id" description:"Resource identifier."`
}

// ProjectIDForPlansParam is the {id} path parameter for /projects/{id}/plans.
type ProjectIDForPlansParam struct {
	ID string `path:"id" description:"Project identifier."`
}

// ---------------------------------------------------------------------------
// Domain → View mappers
// ---------------------------------------------------------------------------

func timePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(time.RFC3339)
	return &s
}

func planToView(p domain.DevelopmentPlan) PlanView {
	return PlanView{
		ID:                    string(p.ID),
		ProjectID:             string(p.ProjectID),
		Title:                 p.Title,
		Objective:             p.Objective,
		Requirements:          p.Requirements,
		ImplementationSummary: p.ImplementationSummary,
		Status:                string(p.Status),
		CreatedAt:             p.CreatedAt.Format(time.RFC3339),
		ConfirmedAt:           timePtr(p.ConfirmedAt),
		CompletedAt:           timePtr(p.CompletedAt),
	}
}

func stageToView(s domain.DevelopmentStage) StageView {
	return StageView{
		ID:                 string(s.ID),
		PlanID:             string(s.PlanID),
		Sequence:           s.Sequence,
		Title:              s.Title,
		Description:        s.Description,
		AcceptanceCriteria: s.AcceptanceCriteria,
		Status:             string(s.Status),
		CreatedAt:          s.CreatedAt.Format(time.RFC3339),
		StartedAt:          timePtr(s.StartedAt),
		CompletedAt:        timePtr(s.CompletedAt),
	}
}

func taskToView(t domain.DevelopmentTask) TaskView {
	return TaskView{
		ID:                 string(t.ID),
		StageID:            string(t.StageID),
		Sequence:           t.Sequence,
		Title:              t.Title,
		Description:        t.Description,
		TaskType:           t.TaskType,
		AcceptanceCriteria: t.AcceptanceCriteria,
		Status:             string(t.Status),
		AgentRoleID:        string(t.AgentRoleID),
		ProviderID:         string(t.ProviderID),
		ProviderModelID:    string(t.ProviderModelID),
		CreatedAt:          t.CreatedAt.Format(time.RFC3339),
		StartedAt:          timePtr(t.StartedAt),
		CompletedAt:        timePtr(t.CompletedAt),
	}
}

func runToView(r domain.TaskRun) RunView {
	return RunView{
		ID:                  string(r.ID),
		TaskID:              string(r.TaskID),
		Attempt:             r.Attempt,
		SessionID:           string(r.SessionID),
		AgentRoleID:         string(r.AgentRoleID),
		ProviderID:          string(r.ProviderID),
		ProviderModelID:     string(r.ProviderModelID),
		ProviderDisplayName: r.ProviderDisplayName,
		ProviderModelName:   r.ProviderModelName,
		ExecutorType:        r.ExecutorType,
		Status:              string(r.Status),
		ResultSummary:       r.ResultSummary,
		ErrorMessage:        r.ErrorMessage,
		CreatedAt:           r.CreatedAt.Format(time.RFC3339),
		StartedAt:           timePtr(r.StartedAt),
		FinishedAt:          timePtr(r.FinishedAt),
	}
}

// ---------------------------------------------------------------------------
// Error mapping
// ---------------------------------------------------------------------------

// writeWorkflowError maps workflow service errors to the standard API envelope.
func writeWorkflowError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, workflow.ErrNotFound):
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found",
			"WORKFLOW_NOT_FOUND", "Resource not found", nil)
	case errors.Is(err, workflow.ErrInvalidInput):
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request",
			"INVALID_INPUT", err.Error(), nil)
	case errors.Is(err, workflow.ErrInvalidTransition):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"INVALID_TRANSITION", "Invalid status transition", nil)
	case errors.Is(err, workflow.ErrChildrenIncomplete):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHILDREN_INCOMPLETE", "Child entities are incomplete", nil)
	case errors.Is(err, workflow.ErrConflict):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"STATE_CONFLICT", "State conflict", nil)
	default:
		envelope.WriteError(w, r, err)
	}
}

// ---------------------------------------------------------------------------
// Path parameter helpers
// ---------------------------------------------------------------------------

func planIDParam(r *http.Request) domain.DevelopmentPlanID {
	return domain.DevelopmentPlanID(chi.URLParam(r, "id"))
}

func stageIDParam(r *http.Request) domain.DevelopmentStageID {
	return domain.DevelopmentStageID(chi.URLParam(r, "id"))
}

func taskIDParam(r *http.Request) domain.DevelopmentTaskID {
	return domain.DevelopmentTaskID(chi.URLParam(r, "id"))
}

// ---------------------------------------------------------------------------
// Plan handlers
// ---------------------------------------------------------------------------

func (c *WorkflowController) createPlan(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/plans")
		return
	}
	var in CreatePlanRequest
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	plan, err := c.Svc.CreatePlan(r.Context(), workflow.CreatePlanInput{
		ProjectID:             domain.ProjectID(in.ProjectID),
		Title:                 in.Title,
		Objective:             in.Objective,
		Requirements:          in.Requirements,
		ImplementationSummary: in.ImplementationSummary,
	})
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, PlanResponse{Plan: planToView(plan)})
}

func (c *WorkflowController) getPlan(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/workflow/plans/{id}")
		return
	}
	plan, err := c.Svc.GetPlan(r.Context(), planIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, PlanResponse{Plan: planToView(plan)})
}

func (c *WorkflowController) listPlansByProject(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/projects/{id}/plans")
		return
	}
	projectID := domain.ProjectID(chi.URLParam(r, "id"))
	plans, err := c.Svc.ListPlansByProject(r.Context(), projectID)
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	views := make([]PlanView, len(plans))
	for i, p := range plans {
		views[i] = planToView(p)
	}
	envelope.WriteJSON(w, http.StatusOK, ListPlansResponse{Plans: views})
}

func (c *WorkflowController) updatePlanContent(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "PATCH", "/api/v1/workflow/plans/{id}")
		return
	}
	var in UpdatePlanRequest
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}

	// Read current plan so omitted *string fields keep their stored value
	// (true PATCH semantics). Only explicitly supplied fields are forwarded
	// to the Service; nil pointers are replaced by the current value.
	current, err := c.Svc.GetPlan(r.Context(), planIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}

	title := current.Title
	if in.Title != nil {
		title = *in.Title
	}
	objective := current.Objective
	if in.Objective != nil {
		objective = *in.Objective
	}
	requirements := current.Requirements
	if in.Requirements != nil {
		requirements = *in.Requirements
	}
	implSummary := current.ImplementationSummary
	if in.ImplementationSummary != nil {
		implSummary = *in.ImplementationSummary
	}

	plan, err := c.Svc.UpdatePlanContent(r.Context(), planIDParam(r), workflow.UpdatePlanContentInput{
		Title:                 title,
		Objective:             objective,
		Requirements:          requirements,
		ImplementationSummary: implSummary,
	})
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, PlanResponse{Plan: planToView(plan)})
}

func (c *WorkflowController) confirmPlan(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/plans/{id}/confirm")
		return
	}
	plan, err := c.Svc.ConfirmPlan(r.Context(), planIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, PlanResponse{Plan: planToView(plan)})
}

func (c *WorkflowController) startPlan(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/plans/{id}/start")
		return
	}
	plan, err := c.Svc.StartPlan(r.Context(), planIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, PlanResponse{Plan: planToView(plan)})
}

func (c *WorkflowController) completePlan(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/plans/{id}/complete")
		return
	}
	plan, err := c.Svc.CompletePlan(r.Context(), planIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, PlanResponse{Plan: planToView(plan)})
}

func (c *WorkflowController) cancelPlan(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/plans/{id}/cancel")
		return
	}
	plan, err := c.Svc.CancelPlan(r.Context(), planIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, PlanResponse{Plan: planToView(plan)})
}

// ---------------------------------------------------------------------------
// Stage handlers
// ---------------------------------------------------------------------------

func (c *WorkflowController) createStage(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/stages")
		return
	}
	var in CreateStageRequest
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	stage, err := c.Svc.CreateStage(r.Context(), workflow.CreateStageInput{
		PlanID:             domain.DevelopmentPlanID(in.PlanID),
		Sequence:           in.Sequence,
		Title:              in.Title,
		Description:        in.Description,
		AcceptanceCriteria: in.AcceptanceCriteria,
	})
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, StageResponse{Stage: stageToView(stage)})
}

func (c *WorkflowController) getStage(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/workflow/stages/{id}")
		return
	}
	stage, err := c.Svc.GetStage(r.Context(), stageIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, StageResponse{Stage: stageToView(stage)})
}

func (c *WorkflowController) listStagesByPlan(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/workflow/plans/{id}/stages")
		return
	}
	stages, err := c.Svc.ListStagesByPlan(r.Context(), planIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	views := make([]StageView, len(stages))
	for i, s := range stages {
		views[i] = stageToView(s)
	}
	envelope.WriteJSON(w, http.StatusOK, ListStagesResponse{Stages: views})
}

func (c *WorkflowController) startStage(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/stages/{id}/start")
		return
	}
	stage, err := c.Svc.StartStage(r.Context(), stageIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, StageResponse{Stage: stageToView(stage)})
}

func (c *WorkflowController) readyForApproval(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/stages/{id}/ready")
		return
	}
	stage, err := c.Svc.ReadyForApproval(r.Context(), stageIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, StageResponse{Stage: stageToView(stage)})
}

func (c *WorkflowController) passStage(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/stages/{id}/pass")
		return
	}
	stage, err := c.Svc.PassStage(r.Context(), stageIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, StageResponse{Stage: stageToView(stage)})
}

func (c *WorkflowController) blockStage(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/stages/{id}/block")
		return
	}
	stage, err := c.Svc.BlockStage(r.Context(), stageIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, StageResponse{Stage: stageToView(stage)})
}

func (c *WorkflowController) unblockStage(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/stages/{id}/unblock")
		return
	}
	stage, err := c.Svc.UnblockStage(r.Context(), stageIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, StageResponse{Stage: stageToView(stage)})
}

func (c *WorkflowController) cancelStage(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/stages/{id}/cancel")
		return
	}
	stage, err := c.Svc.CancelStage(r.Context(), stageIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, StageResponse{Stage: stageToView(stage)})
}

// ---------------------------------------------------------------------------
// Task handlers
// ---------------------------------------------------------------------------

func (c *WorkflowController) createTask(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/tasks")
		return
	}
	var in CreateTaskRequest
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	task, err := c.Svc.CreateTask(r.Context(), workflow.CreateTaskInput{
		StageID:            domain.DevelopmentStageID(in.StageID),
		Sequence:           in.Sequence,
		Title:              in.Title,
		Description:        in.Description,
		TaskType:           in.TaskType,
		AcceptanceCriteria: in.AcceptanceCriteria,
		AgentRoleID:        domain.AgentRoleID(in.AgentRoleID),
		ProviderID:         domain.ProviderID(in.ProviderID),
		ProviderModelID:    domain.ProviderModelID(in.ProviderModelID),
	})
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, TaskResponse{Task: taskToView(task)})
}

func (c *WorkflowController) getTask(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/workflow/tasks/{id}")
		return
	}
	task, err := c.Svc.GetTask(r.Context(), taskIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, TaskResponse{Task: taskToView(task)})
}

func (c *WorkflowController) listTasksByStage(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/workflow/stages/{id}/tasks")
		return
	}
	tasks, err := c.Svc.ListTasksByStage(r.Context(), stageIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	views := make([]TaskView, len(tasks))
	for i, t := range tasks {
		views[i] = taskToView(t)
	}
	envelope.WriteJSON(w, http.StatusOK, ListTasksResponse{Tasks: views})
}

func (c *WorkflowController) readyTask(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/tasks/{id}/ready")
		return
	}
	task, err := c.Svc.ReadyTask(r.Context(), taskIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, TaskResponse{Task: taskToView(task)})
}

func (c *WorkflowController) startTask(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/tasks/{id}/start")
		return
	}
	task, err := c.Svc.StartTask(r.Context(), taskIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, TaskResponse{Task: taskToView(task)})
}

func (c *WorkflowController) submitForReview(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/tasks/{id}/review")
		return
	}
	task, err := c.Svc.SubmitForReview(r.Context(), taskIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, TaskResponse{Task: taskToView(task)})
}

func (c *WorkflowController) passTask(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/tasks/{id}/pass")
		return
	}
	task, err := c.Svc.PassTask(r.Context(), taskIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, TaskResponse{Task: taskToView(task)})
}

func (c *WorkflowController) rejectTask(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/tasks/{id}/reject")
		return
	}
	task, err := c.Svc.RejectTask(r.Context(), taskIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, TaskResponse{Task: taskToView(task)})
}

func (c *WorkflowController) blockTask(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/tasks/{id}/block")
		return
	}
	task, err := c.Svc.BlockTask(r.Context(), taskIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, TaskResponse{Task: taskToView(task)})
}

func (c *WorkflowController) unblockTask(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/tasks/{id}/unblock")
		return
	}
	task, err := c.Svc.UnblockTask(r.Context(), taskIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, TaskResponse{Task: taskToView(task)})
}

func (c *WorkflowController) cancelTask(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/tasks/{id}/cancel")
		return
	}
	task, err := c.Svc.CancelTask(r.Context(), taskIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, TaskResponse{Task: taskToView(task)})
}

func (c *WorkflowController) assignTask(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "PATCH", "/api/v1/workflow/tasks/{id}/assignment")
		return
	}
	var in AssignTaskRequest
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	task, err := c.Svc.AssignTask(r.Context(), taskIDParam(r),
		domain.AgentRoleID(in.AgentRoleID),
		domain.ProviderID(in.ProviderID),
		domain.ProviderModelID(in.ProviderModelID),
	)
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, TaskResponse{Task: taskToView(task)})
}

// ---------------------------------------------------------------------------
// Run handlers (Phase 2.3)
// ---------------------------------------------------------------------------

func runIDParam(r *http.Request) domain.TaskRunID {
	return domain.TaskRunID(chi.URLParam(r, "id"))
}

func (c *WorkflowController) createRun(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/runs")
		return
	}
	var in CreateRunRequest
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	run, err := c.Svc.CreateRun(r.Context(), workflow.CreateRunInput{
		TaskID: domain.DevelopmentTaskID(in.TaskID),
	})
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, RunResponse{Run: runToView(run)})
}

func (c *WorkflowController) getRun(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/workflow/runs/{id}")
		return
	}
	run, err := c.Svc.GetRun(r.Context(), runIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, RunResponse{Run: runToView(run)})
}

func (c *WorkflowController) listRunsByTask(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/workflow/tasks/{id}/runs")
		return
	}
	runs, err := c.Svc.ListRunsByTask(r.Context(), taskIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	views := make([]RunView, len(runs))
	for i, run := range runs {
		views[i] = runToView(run)
	}
	envelope.WriteJSON(w, http.StatusOK, ListRunsResponse{Runs: views})
}

func (c *WorkflowController) startRun(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/runs/{id}/start")
		return
	}
	run, err := c.Svc.StartRun(r.Context(), runIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, RunResponse{Run: runToView(run)})
}

func (c *WorkflowController) cancelRun(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/runs/{id}/cancel")
		return
	}
	run, err := c.Svc.CancelRun(r.Context(), runIDParam(r))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, RunResponse{Run: runToView(run)})
}

// ---------------------------------------------------------------------------
// AgentRole handlers (Phase 2.4)
// ---------------------------------------------------------------------------

func (c *WorkflowController) createAgentRole(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/workflow/roles")
		return
	}
	var in CreateAgentRoleRequest
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	role, err := c.Svc.CreateAgentRole(r.Context(), workflow.CreateAgentRoleInput{
		Name:                   in.Name,
		DisplayName:            in.DisplayName,
		Description:            in.Description,
		SystemPrompt:           in.SystemPrompt,
		DefaultProviderID:      domain.ProviderID(in.DefaultProviderID),
		DefaultProviderModelID: domain.ProviderModelID(in.DefaultProviderModelID),
	})
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, agentRoleToView(role))
}

func (c *WorkflowController) listAgentRoles(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/workflow/roles")
		return
	}
	roles, err := c.Svc.ListAgentRoles(r.Context())
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	out := make([]AgentRoleView, len(roles))
	for i, role := range roles {
		out[i] = agentRoleToView(role)
	}
	envelope.WriteJSON(w, http.StatusOK, ListAgentRolesResponse{Roles: out})
}

func (c *WorkflowController) getAgentRole(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/workflow/roles/{id}")
		return
	}
	role, err := c.Svc.GetAgentRole(r.Context(), domain.AgentRoleID(chi.URLParam(r, "id")))
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, agentRoleToView(role))
}

func (c *WorkflowController) updateAgentRole(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "PATCH", "/api/v1/workflow/roles/{id}")
		return
	}
	var in UpdateAgentRoleRequest
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	input := workflow.UpdateAgentRoleInput{
		DisplayName:  in.DisplayName,
		Description:  in.Description,
		SystemPrompt: in.SystemPrompt,
	}
	if in.DefaultProviderID != nil {
		pid := domain.ProviderID(*in.DefaultProviderID)
		input.DefaultProviderID = &pid
	}
	if in.DefaultProviderModelID != nil {
		mid := domain.ProviderModelID(*in.DefaultProviderModelID)
		input.DefaultProviderModelID = &mid
	}
	role, err := c.Svc.UpdateAgentRole(r.Context(), domain.AgentRoleID(chi.URLParam(r, "id")), input)
	if err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, agentRoleToView(role))
}

func (c *WorkflowController) setAgentRoleEnabled(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "PATCH", "/api/v1/workflow/roles/{id}/enabled")
		return
	}
	var in SetAgentRoleEnabledRequest
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	if err := c.Svc.SetAgentRoleEnabled(r.Context(), domain.AgentRoleID(chi.URLParam(r, "id")), in.Enabled); err != nil {
		writeWorkflowError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// AgentRoleView is the wire representation of an AgentRole.
type AgentRoleView struct {
	ID                     string `json:"id"`
	Name                   string `json:"name"`
	DisplayName            string `json:"displayName"`
	Description            string `json:"description"`
	SystemPrompt           string `json:"systemPrompt,omitempty"`
	DefaultProviderID      string `json:"defaultProviderId,omitempty"`
	DefaultProviderModelID string `json:"defaultProviderModelId,omitempty"`
	Enabled                bool   `json:"enabled"`
	CreatedAt              string `json:"createdAt"`
	UpdatedAt              string `json:"updatedAt"`
}

func agentRoleToView(r domain.AgentRole) AgentRoleView {
	return AgentRoleView{
		ID:                     r.ID,
		Name:                   r.Name,
		DisplayName:            r.DisplayName,
		Description:            r.Description,
		SystemPrompt:           r.SystemPrompt,
		DefaultProviderID:      string(r.DefaultProviderID),
		DefaultProviderModelID: string(r.DefaultProviderModelID),
		Enabled:                r.Enabled,
		CreatedAt:              r.CreatedAt.Format(time.RFC3339),
		UpdatedAt:              r.UpdatedAt.Format(time.RFC3339),
	}
}

// ListAgentRolesResponse is the wire shape for GET /api/v1/workflow/roles.
type ListAgentRolesResponse struct {
	Roles []AgentRoleView `json:"roles"`
}
