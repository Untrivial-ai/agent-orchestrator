package workflow

import (
	"context"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// CreatePlanInput describes a new development plan.
type CreatePlanInput struct {
	ProjectID            domain.ProjectID
	Title                string
	Objective            string
	Requirements         string
	ImplementationSummary string
}

// UpdatePlanContentInput describes plan content updates.
type UpdatePlanContentInput struct {
	Title                string
	Objective            string
	Requirements         string
	ImplementationSummary string
}

// CreatePlan creates a new development plan in DRAFT status.
func (s *Service) CreatePlan(ctx context.Context, in CreatePlanInput) (domain.DevelopmentPlan, error) {
	if strings.TrimSpace(in.Title) == "" {
		return domain.DevelopmentPlan{}, ErrInvalidInput
	}

	// Verify project exists
	if _, ok, err := s.store.GetProject(ctx, string(in.ProjectID)); err != nil {
		return domain.DevelopmentPlan{}, err
	} else if !ok {
		return domain.DevelopmentPlan{}, ErrNotFound
	}

	now := s.now()
	plan := domain.DevelopmentPlan{
		ID:                    domain.DevelopmentPlanID(s.newID()),
		ProjectID:             in.ProjectID,
		Title:                 strings.TrimSpace(in.Title),
		Objective:             strings.TrimSpace(in.Objective),
		Requirements:          strings.TrimSpace(in.Requirements),
		ImplementationSummary: strings.TrimSpace(in.ImplementationSummary),
		Status:                domain.PlanStatusDraft,
		CreatedAt:             now,
	}

	if err := s.store.CreateDevelopmentPlan(ctx, plan); err != nil {
		return domain.DevelopmentPlan{}, err
	}
	return plan, nil
}

// GetPlan retrieves a development plan by ID.
func (s *Service) GetPlan(ctx context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error) {
	plan, ok, err := s.store.GetDevelopmentPlan(ctx, id)
	if err != nil {
		return domain.DevelopmentPlan{}, err
	}
	if !ok {
		return domain.DevelopmentPlan{}, ErrNotFound
	}
	return plan, nil
}

// ListPlansByProject lists all plans for a project.
func (s *Service) ListPlansByProject(ctx context.Context, projectID domain.ProjectID) ([]domain.DevelopmentPlan, error) {
	return s.store.ListDevelopmentPlansByProject(ctx, projectID)
}

// ConfirmPlan transitions a plan from DRAFT to CONFIRMED.
func (s *Service) ConfirmPlan(ctx context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error) {
	plan, ok, err := s.store.GetDevelopmentPlan(ctx, id)
	if err != nil {
		return domain.DevelopmentPlan{}, err
	}
	if !ok {
		return domain.DevelopmentPlan{}, ErrNotFound
	}

	if err := domain.ValidDevelopmentPlanTransition(plan.Status, domain.PlanStatusConfirmed); err != nil {
		return domain.DevelopmentPlan{}, ErrInvalidTransition
	}

	now := s.now()
	if err := s.store.UpdateDevelopmentPlanStatus(ctx, id, domain.PlanStatusConfirmed, &now, nil); err != nil {
		return domain.DevelopmentPlan{}, err
	}

	plan.Status = domain.PlanStatusConfirmed
	plan.ConfirmedAt = &now
	return plan, nil
}

// StartPlan transitions a plan from CONFIRMED to IN_PROGRESS.
// Requires at least one stage to exist.
func (s *Service) StartPlan(ctx context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error) {
	plan, ok, err := s.store.GetDevelopmentPlan(ctx, id)
	if err != nil {
		return domain.DevelopmentPlan{}, err
	}
	if !ok {
		return domain.DevelopmentPlan{}, ErrNotFound
	}

	if err := domain.ValidDevelopmentPlanTransition(plan.Status, domain.PlanStatusInProgress); err != nil {
		return domain.DevelopmentPlan{}, ErrInvalidTransition
	}

	// Verify at least one stage exists
	stages, err := s.store.ListDevelopmentStagesByPlan(ctx, id)
	if err != nil {
		return domain.DevelopmentPlan{}, err
	}
	if len(stages) == 0 {
		return domain.DevelopmentPlan{}, ErrChildrenIncomplete
	}

	if err := s.store.UpdateDevelopmentPlanStatus(ctx, id, domain.PlanStatusInProgress, nil, nil); err != nil {
		return domain.DevelopmentPlan{}, err
	}

	plan.Status = domain.PlanStatusInProgress
	return plan, nil
}

// CompletePlan transitions a plan from IN_PROGRESS to COMPLETED.
// Requires all non-CANCELLED stages to be PASSED.
func (s *Service) CompletePlan(ctx context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error) {
	plan, ok, err := s.store.GetDevelopmentPlan(ctx, id)
	if err != nil {
		return domain.DevelopmentPlan{}, err
	}
	if !ok {
		return domain.DevelopmentPlan{}, ErrNotFound
	}

	if err := domain.ValidDevelopmentPlanTransition(plan.Status, domain.PlanStatusCompleted); err != nil {
		return domain.DevelopmentPlan{}, ErrInvalidTransition
	}

	// Verify all non-cancelled stages are passed
	stages, err := s.store.ListDevelopmentStagesByPlan(ctx, id)
	if err != nil {
		return domain.DevelopmentPlan{}, err
	}
	for _, stage := range stages {
		if stage.Status != domain.StageStatusCancelled && stage.Status != domain.StageStatusPassed {
			return domain.DevelopmentPlan{}, ErrChildrenIncomplete
		}
	}

	now := s.now()
	if err := s.store.UpdateDevelopmentPlanStatus(ctx, id, domain.PlanStatusCompleted, nil, &now); err != nil {
		return domain.DevelopmentPlan{}, err
	}

	plan.Status = domain.PlanStatusCompleted
	plan.CompletedAt = &now
	return plan, nil
}

// CancelPlan transitions a plan to CANCELLED from any non-terminal state.
func (s *Service) CancelPlan(ctx context.Context, id domain.DevelopmentPlanID) (domain.DevelopmentPlan, error) {
	plan, ok, err := s.store.GetDevelopmentPlan(ctx, id)
	if err != nil {
		return domain.DevelopmentPlan{}, err
	}
	if !ok {
		return domain.DevelopmentPlan{}, ErrNotFound
	}

	if err := domain.ValidDevelopmentPlanTransition(plan.Status, domain.PlanStatusCancelled); err != nil {
		return domain.DevelopmentPlan{}, ErrInvalidTransition
	}

	now := s.now()
	if err := s.store.UpdateDevelopmentPlanStatus(ctx, id, domain.PlanStatusCancelled, nil, &now); err != nil {
		return domain.DevelopmentPlan{}, err
	}

	plan.Status = domain.PlanStatusCancelled
	plan.CompletedAt = &now
	return plan, nil
}

// UpdatePlanContent updates non-status fields of a plan.
func (s *Service) UpdatePlanContent(ctx context.Context, id domain.DevelopmentPlanID, in UpdatePlanContentInput) (domain.DevelopmentPlan, error) {
	plan, ok, err := s.store.GetDevelopmentPlan(ctx, id)
	if err != nil {
		return domain.DevelopmentPlan{}, err
	}
	if !ok {
		return domain.DevelopmentPlan{}, ErrNotFound
	}

	if strings.TrimSpace(in.Title) == "" {
		return domain.DevelopmentPlan{}, ErrInvalidInput
	}

	if err := s.store.UpdateDevelopmentPlanContent(ctx, id,
		strings.TrimSpace(in.Title),
		strings.TrimSpace(in.Objective),
		strings.TrimSpace(in.Requirements),
		strings.TrimSpace(in.ImplementationSummary),
	); err != nil {
		return domain.DevelopmentPlan{}, err
	}

	plan.Title = strings.TrimSpace(in.Title)
	plan.Objective = strings.TrimSpace(in.Objective)
	plan.Requirements = strings.TrimSpace(in.Requirements)
	plan.ImplementationSummary = strings.TrimSpace(in.ImplementationSummary)
	return plan, nil
}
