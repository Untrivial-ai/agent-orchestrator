package workflow

import (
	"context"
	"errors"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
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

	// Project validation (read-only)
	GetProject(ctx context.Context, id string) (domain.ProjectRecord, bool, error)
}

// Service provides business logic for the development workflow.
type Service struct {
	store Store
	now   func() time.Time
	newID func() string
}

// New creates a new workflow Service.
func New(store Store) *Service {
	return &Service{
		store: store,
		now:   func() time.Time { return time.Now().UTC() },
		newID: uuid.NewString,
	}
}
