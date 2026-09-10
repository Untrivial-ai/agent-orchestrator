package workflow

import (
	"context"
	"errors"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/google/uuid"
)

// Sentinel errors for workflow service operations.
var (
	ErrNotFound           = errors.New("resource not found")
	ErrInvalidTransition  = errors.New("invalid status transition")
	ErrInvalidInput       = errors.New("invalid input")
	ErrChildrenIncomplete = errors.New("children entities incomplete")
	ErrConflict           = errors.New("state conflict")
)

// SessionRuntime abstracts session spawn/kill for the workflow service.
// The concrete implementation is typically *session_manager.Manager.
type SessionRuntime interface {
	SpawnSession(ctx context.Context, cfg ports.SpawnConfig) (domain.SessionRecord, error)
	KillSession(ctx context.Context, id domain.SessionID) (bool, error)
}

// Store defines the persistence surface needed by the workflow service.
// It matches the Phase 2.1 workflow_store.go signatures directly.
type Store interface {
	// DevelopmentPlan
	CreateDevelopmentPlan(ctx context.Context, p domain.DevelopmentPlan) error
	GetDevelopmentPlan(ctx context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, bool, error)
	ListDevelopmentPlansByProject(ctx context.Context, projectID domain.ProjectID) ([]domain.DevelopmentPlan, error)
	UpdateDevelopmentPlanStatus(ctx context.Context, id domain.DevelopmentPlanID, status domain.DevelopmentPlanStatus, confirmedAt, completedAt *time.Time) error
	UpdateDevelopmentPlanContent(ctx context.Context, id domain.DevelopmentPlanID, title, objective, requirements, implSummary string) error

	// DevelopmentStage
	CreateDevelopmentStage(ctx context.Context, st domain.DevelopmentStage) error
	GetDevelopmentStage(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, bool, error)
	ListDevelopmentStagesByPlan(ctx context.Context, planID domain.DevelopmentPlanID) ([]domain.DevelopmentStage, error)
	UpdateDevelopmentStageStatus(ctx context.Context, id domain.DevelopmentStageID, status domain.DevelopmentStageStatus, startedAt, completedAt *time.Time) error

	// DevelopmentTask
	CreateDevelopmentTask(ctx context.Context, t domain.DevelopmentTask) error
	GetDevelopmentTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, bool, error)
	ListDevelopmentTasksByStage(ctx context.Context, stageID domain.DevelopmentStageID) ([]domain.DevelopmentTask, error)
	UpdateDevelopmentTaskStatus(ctx context.Context, id domain.DevelopmentTaskID, status domain.DevelopmentTaskStatus, startedAt, completedAt *time.Time) error
	UpdateDevelopmentTaskAssignment(ctx context.Context, id domain.DevelopmentTaskID, roleID domain.AgentRoleID, providerID domain.ProviderID, modelID domain.ProviderModelID) error

	// TaskRun (Phase 2.3)
	CreateTaskRun(ctx context.Context, r domain.TaskRun) error
	GetTaskRun(ctx context.Context, id domain.TaskRunID) (domain.TaskRun, bool, error)
	ListTaskRunsByTask(ctx context.Context, taskID domain.DevelopmentTaskID) ([]domain.TaskRun, error)
	ListTaskRunsByStatus(ctx context.Context, status domain.TaskRunStatus) ([]domain.TaskRun, error)
	UpdateTaskRunStatus(ctx context.Context, id domain.TaskRunID, status domain.TaskRunStatus, resultSummary, errorMessage string, startedAt, finishedAt *time.Time) error
	UpdateTaskRunSnapshot(ctx context.Context, id domain.TaskRunID, sessionID domain.SessionID, providerID domain.ProviderID, providerModelID domain.ProviderModelID, providerDisplayName, providerModelName, executorType string) error
	BindTaskRunSession(ctx context.Context, id domain.TaskRunID, sessionID domain.SessionID) error

	// Session (read-only, used by Reconcile)
	GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error)

	// Project validation (read-only)
	GetProject(ctx context.Context, id string) (domain.ProjectRecord, bool, error)

	// AgentRole (Phase 2.4)
	CreateAgentRole(ctx context.Context, r domain.AgentRole) error
	GetAgentRole(ctx context.Context, id domain.AgentRoleID) (domain.AgentRole, bool, error)
	ListAgentRoles(ctx context.Context) ([]domain.AgentRole, error)
	UpdateAgentRole(ctx context.Context, r domain.AgentRole) error
	SetAgentRoleEnabled(ctx context.Context, id domain.AgentRoleID, enabled bool, updatedAt time.Time) error

	// Provider metadata (read-only, for validation — no secret access)
	GetProvider(ctx context.Context, id domain.ProviderID) (domain.Provider, bool, error)
	GetProviderModel(ctx context.Context, id domain.ProviderModelID) (domain.ProviderModel, bool, error)
}

// Service provides business logic for the development workflow.
type Service struct {
	store   Store
	runtime SessionRuntime // nil = no session integration (unit tests)
	now     func() time.Time
	newID   func() string
}

// New creates a new workflow Service.
func New(store Store) *Service {
	return &Service{
		store: store,
		now:   func() time.Time { return time.Now().UTC() },
		newID: uuid.NewString,
	}
}

// WithRuntime injects a SessionRuntime for spawn/kill operations.
// Returns the same Service for chaining.
func (s *Service) WithRuntime(r SessionRuntime) *Service {
	s.runtime = r
	return s
}
