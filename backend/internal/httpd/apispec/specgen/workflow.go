package specgen

import (
	"net/http"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

func workflowOperations() []operation {
	return []operation{
		// ---- Plan ----
		{
			method: http.MethodPost, path: "/api/v1/workflow/plans", id: "createPlan", tag: "workflow",
			summary: "Create a development plan in DRAFT status",
			reqBody: controllers.CreatePlanRequest{},
			resps: []respUnit{
				{http.StatusCreated, controllers.PlanResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/workflow/plans/{id}", id: "getPlan", tag: "workflow",
			summary:    "Get a development plan by ID",
			pathParams: []any{controllers.WorkflowIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.PlanResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/projects/{id}/plans", id: "listPlansByProject", tag: "workflow",
			summary:    "List all development plans for a project",
			pathParams: []any{controllers.ProjectIDForPlansParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.ListPlansResponse{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPatch, path: "/api/v1/workflow/plans/{id}", id: "updatePlanContent", tag: "workflow",
			summary:    "Update plan content (title, objective, requirements, summary)",
			pathParams: []any{controllers.WorkflowIDParam{}},
			reqBody:    controllers.UpdatePlanRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.PlanResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/workflow/plans/{id}/confirm", id: "confirmPlan", tag: "workflow",
			summary:    "Confirm a DRAFT plan",
			pathParams: []any{controllers.WorkflowIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.PlanResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/workflow/plans/{id}/start", id: "startPlan", tag: "workflow",
			summary:    "Start a confirmed plan (requires at least one stage)",
			pathParams: []any{controllers.WorkflowIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.PlanResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/workflow/plans/{id}/complete", id: "completePlan", tag: "workflow",
			summary:    "Complete a plan (all non-cancelled stages must be passed)",
			pathParams: []any{controllers.WorkflowIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.PlanResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/workflow/plans/{id}/cancel", id: "cancelPlan", tag: "workflow",
			summary:    "Cancel a plan",
			pathParams: []any{controllers.WorkflowIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.PlanResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},

		// ---- Stage ----
		{
			method: http.MethodPost, path: "/api/v1/workflow/stages", id: "createStage", tag: "workflow",
			summary: "Create a development stage in PENDING status",
			reqBody: controllers.CreateStageRequest{},
			resps: []respUnit{
				{http.StatusCreated, controllers.StageResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/workflow/stages/{id}", id: "getStage", tag: "workflow",
			summary:    "Get a development stage by ID",
			pathParams: []any{controllers.WorkflowIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.StageResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/workflow/plans/{id}/stages", id: "listStagesByPlan", tag: "workflow",
			summary:    "List all stages for a plan",
			pathParams: []any{controllers.WorkflowIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.ListStagesResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/workflow/stages/{id}/start", id: "startStage", tag: "workflow",
			summary:    "Start a pending stage (plan must be confirmed or in_progress)",
			pathParams: []any{controllers.WorkflowIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.StageResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/workflow/stages/{id}/ready", id: "readyForApproval", tag: "workflow",
			summary:    "Mark stage ready for approval (all non-cancelled tasks must be passed)",
			pathParams: []any{controllers.WorkflowIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.StageResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/workflow/stages/{id}/pass", id: "passStage", tag: "workflow",
			summary:    "Pass a stage (re-validates task completeness)",
			pathParams: []any{controllers.WorkflowIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.StageResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/workflow/stages/{id}/block", id: "blockStage", tag: "workflow",
			summary:    "Block a stage",
			pathParams: []any{controllers.WorkflowIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.StageResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/workflow/stages/{id}/unblock", id: "unblockStage", tag: "workflow",
			summary:    "Unblock a stage (returns to in_progress)",
			pathParams: []any{controllers.WorkflowIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.StageResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/workflow/stages/{id}/cancel", id: "cancelStage", tag: "workflow",
			summary:    "Cancel a stage",
			pathParams: []any{controllers.WorkflowIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.StageResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},

		// ---- Task ----
		{
			method: http.MethodPost, path: "/api/v1/workflow/tasks", id: "createTask", tag: "workflow",
			summary: "Create a development task in PENDING status",
			reqBody: controllers.CreateTaskRequest{},
			resps: []respUnit{
				{http.StatusCreated, controllers.TaskResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/workflow/tasks/{id}", id: "getTask", tag: "workflow",
			summary:    "Get a development task by ID",
			pathParams: []any{controllers.WorkflowIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.TaskResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/workflow/stages/{id}/tasks", id: "listTasksByStage", tag: "workflow",
			summary:    "List all tasks for a stage",
			pathParams: []any{controllers.WorkflowIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.ListTasksResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/workflow/tasks/{id}/ready", id: "readyTask", tag: "workflow",
			summary:    "Mark task as ready (from pending or blocked)",
			pathParams: []any{controllers.WorkflowIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.TaskResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/workflow/tasks/{id}/start", id: "startTask", tag: "workflow",
			summary:    "Start a ready task (stage must be in_progress)",
			pathParams: []any{controllers.WorkflowIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.TaskResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/workflow/tasks/{id}/review", id: "submitTaskForReview", tag: "workflow",
			summary:    "Submit a running task for review",
			pathParams: []any{controllers.WorkflowIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.TaskResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/workflow/tasks/{id}/pass", id: "passTask", tag: "workflow",
			summary:    "Pass a task under review",
			pathParams: []any{controllers.WorkflowIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.TaskResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/workflow/tasks/{id}/reject", id: "rejectTask", tag: "workflow",
			summary:    "Reject a task (returns to ready, not rejected status)",
			pathParams: []any{controllers.WorkflowIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.TaskResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/workflow/tasks/{id}/block", id: "blockTask", tag: "workflow",
			summary:    "Block a task",
			pathParams: []any{controllers.WorkflowIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.TaskResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/workflow/tasks/{id}/unblock", id: "unblockTask", tag: "workflow",
			summary:    "Unblock a task (returns to ready)",
			pathParams: []any{controllers.WorkflowIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.TaskResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/workflow/tasks/{id}/cancel", id: "cancelTask", tag: "workflow",
			summary:    "Cancel a task",
			pathParams: []any{controllers.WorkflowIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.TaskResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPatch, path: "/api/v1/workflow/tasks/{id}/assignment", id: "assignTask", tag: "workflow",
			summary:    "Update agent role and provider assignment for a task",
			pathParams: []any{controllers.WorkflowIDParam{}},
			reqBody:    controllers.AssignTaskRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.TaskResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
	}
}
