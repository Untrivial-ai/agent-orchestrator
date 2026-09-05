package workflow

import (
	"context"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// CreateStageInput describes a new development stage.
type CreateStageInput struct {
	PlanID            domain.DevelopmentPlanID
	Sequence          int
	Title             string
	Description       string
	AcceptanceCriteria string
}

// CreateStage creates a new development stage in PENDING status.
func (s *Service) CreateStage(ctx context.Context, in CreateStageInput) (domain.DevelopmentStage, error) {
	if strings.TrimSpace(in.Title) == "" {
		return domain.DevelopmentStage{}, ErrInvalidInput
	}

	// Verify plan exists
	if _, ok, err := s.store.GetDevelopmentPlan(ctx, in.PlanID); err != nil {
		return domain.DevelopmentStage{}, err
	} else if !ok {
		return domain.DevelopmentStage{}, ErrNotFound
	}

	now := s.now()
	stage := domain.DevelopmentStage{
		ID:                 domain.DevelopmentStageID(s.newID()),
		PlanID:             in.PlanID,
		Sequence:           in.Sequence,
		Title:              strings.TrimSpace(in.Title),
		Description:        strings.TrimSpace(in.Description),
		AcceptanceCriteria: strings.TrimSpace(in.AcceptanceCriteria),
		Status:             domain.StageStatusPending,
		CreatedAt:          now,
	}

	if err := s.store.CreateDevelopmentStage(ctx, stage); err != nil {
		return domain.DevelopmentStage{}, err
	}
	return stage, nil
}

// GetStage retrieves a development stage by ID.
func (s *Service) GetStage(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error) {
	stage, ok, err := s.store.GetDevelopmentStage(ctx, id)
	if err != nil {
		return domain.DevelopmentStage{}, err
	}
	if !ok {
		return domain.DevelopmentStage{}, ErrNotFound
	}
	return stage, nil
}

// ListStagesByPlan lists all stages for a plan.
func (s *Service) ListStagesByPlan(ctx context.Context, planID domain.DevelopmentPlanID) ([]domain.DevelopmentStage, error) {
	return s.store.ListDevelopmentStagesByPlan(ctx, planID)
}

// StartStage transitions a stage from PENDING to IN_PROGRESS.
// Requires the parent plan to be CONFIRMED or IN_PROGRESS.
func (s *Service) StartStage(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error) {
	stage, ok, err := s.store.GetDevelopmentStage(ctx, id)
	if err != nil {
		return domain.DevelopmentStage{}, err
	}
	if !ok {
		return domain.DevelopmentStage{}, ErrNotFound
	}

	if err := domain.ValidDevelopmentStageTransition(stage.Status, domain.StageStatusInProgress); err != nil {
		return domain.DevelopmentStage{}, ErrInvalidTransition
	}

	// Verify plan state
	plan, ok, err := s.store.GetDevelopmentPlan(ctx, stage.PlanID)
	if err != nil {
		return domain.DevelopmentStage{}, err
	}
	if !ok {
		return domain.DevelopmentStage{}, ErrNotFound
	}
	if plan.Status != domain.PlanStatusConfirmed && plan.Status != domain.PlanStatusInProgress {
		return domain.DevelopmentStage{}, ErrInvalidTransition
	}

	now := s.now()
	if err := s.store.UpdateDevelopmentStageStatus(ctx, id, domain.StageStatusInProgress, &now, nil); err != nil {
		return domain.DevelopmentStage{}, err
	}

	stage.Status = domain.StageStatusInProgress
	stage.StartedAt = &now
	return stage, nil
}

// ReadyForApproval transitions a stage from IN_PROGRESS to READY_FOR_APPROVAL.
// Requires all non-CANCELLED tasks to be PASSED.
func (s *Service) ReadyForApproval(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error) {
	stage, ok, err := s.store.GetDevelopmentStage(ctx, id)
	if err != nil {
		return domain.DevelopmentStage{}, err
	}
	if !ok {
		return domain.DevelopmentStage{}, ErrNotFound
	}

	if err := domain.ValidDevelopmentStageTransition(stage.Status, domain.StageStatusReadyForApproval); err != nil {
		return domain.DevelopmentStage{}, ErrInvalidTransition
	}

	// Verify all non-cancelled tasks are passed
	tasks, err := s.store.ListDevelopmentTasksByStage(ctx, id)
	if err != nil {
		return domain.DevelopmentStage{}, err
	}
	for _, task := range tasks {
		if task.Status != domain.TaskStatusCancelled && task.Status != domain.TaskStatusPassed {
			return domain.DevelopmentStage{}, ErrChildrenIncomplete
		}
	}

	if err := s.store.UpdateDevelopmentStageStatus(ctx, id, domain.StageStatusReadyForApproval, nil, nil); err != nil {
		return domain.DevelopmentStage{}, err
	}

	stage.Status = domain.StageStatusReadyForApproval
	return stage, nil
}

// PassStage transitions a stage from READY_FOR_APPROVAL to PASSED.
// Re-validates all non-CANCELLED tasks are PASSED.
func (s *Service) PassStage(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error) {
	stage, ok, err := s.store.GetDevelopmentStage(ctx, id)
	if err != nil {
		return domain.DevelopmentStage{}, err
	}
	if !ok {
		return domain.DevelopmentStage{}, ErrNotFound
	}

	if err := domain.ValidDevelopmentStageTransition(stage.Status, domain.StageStatusPassed); err != nil {
		return domain.DevelopmentStage{}, ErrInvalidTransition
	}

	// Re-validate all non-cancelled tasks are passed
	tasks, err := s.store.ListDevelopmentTasksByStage(ctx, id)
	if err != nil {
		return domain.DevelopmentStage{}, err
	}
	for _, task := range tasks {
		if task.Status != domain.TaskStatusCancelled && task.Status != domain.TaskStatusPassed {
			return domain.DevelopmentStage{}, ErrChildrenIncomplete
		}
	}

	now := s.now()
	if err := s.store.UpdateDevelopmentStageStatus(ctx, id, domain.StageStatusPassed, nil, &now); err != nil {
		return domain.DevelopmentStage{}, err
	}

	stage.Status = domain.StageStatusPassed
	stage.CompletedAt = &now
	return stage, nil
}

// BlockStage transitions a stage to BLOCKED.
func (s *Service) BlockStage(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error) {
	stage, ok, err := s.store.GetDevelopmentStage(ctx, id)
	if err != nil {
		return domain.DevelopmentStage{}, err
	}
	if !ok {
		return domain.DevelopmentStage{}, ErrNotFound
	}

	if err := domain.ValidDevelopmentStageTransition(stage.Status, domain.StageStatusBlocked); err != nil {
		return domain.DevelopmentStage{}, ErrInvalidTransition
	}

	if err := s.store.UpdateDevelopmentStageStatus(ctx, id, domain.StageStatusBlocked, nil, nil); err != nil {
		return domain.DevelopmentStage{}, err
	}

	stage.Status = domain.StageStatusBlocked
	return stage, nil
}

// UnblockStage transitions a stage from BLOCKED to IN_PROGRESS.
func (s *Service) UnblockStage(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error) {
	stage, ok, err := s.store.GetDevelopmentStage(ctx, id)
	if err != nil {
		return domain.DevelopmentStage{}, err
	}
	if !ok {
		return domain.DevelopmentStage{}, ErrNotFound
	}

	if err := domain.ValidDevelopmentStageTransition(stage.Status, domain.StageStatusInProgress); err != nil {
		return domain.DevelopmentStage{}, ErrInvalidTransition
	}

	if err := s.store.UpdateDevelopmentStageStatus(ctx, id, domain.StageStatusInProgress, nil, nil); err != nil {
		return domain.DevelopmentStage{}, err
	}

	stage.Status = domain.StageStatusInProgress
	return stage, nil
}

// CancelStage transitions a stage to CANCELLED.
func (s *Service) CancelStage(ctx context.Context, id domain.DevelopmentStageID) (domain.DevelopmentStage, error) {
	stage, ok, err := s.store.GetDevelopmentStage(ctx, id)
	if err != nil {
		return domain.DevelopmentStage{}, err
	}
	if !ok {
		return domain.DevelopmentStage{}, ErrNotFound
	}

	if err := domain.ValidDevelopmentStageTransition(stage.Status, domain.StageStatusCancelled); err != nil {
		return domain.DevelopmentStage{}, ErrInvalidTransition
	}

	now := s.now()
	if err := s.store.UpdateDevelopmentStageStatus(ctx, id, domain.StageStatusCancelled, nil, &now); err != nil {
		return domain.DevelopmentStage{}, err
	}

	stage.Status = domain.StageStatusCancelled
	stage.CompletedAt = &now
	return stage, nil
}
