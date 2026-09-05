package workflow

import (
	"context"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// CreateTaskInput describes a new development task.
type CreateTaskInput struct {
	StageID           domain.DevelopmentStageID
	Sequence          int
	Title             string
	Description       string
	TaskType          string
	AcceptanceCriteria string
	AgentRoleID       domain.AgentRoleID
	ProviderID        domain.ProviderID
	ProviderModelID   domain.ProviderModelID
}

// CreateTask creates a new development task in PENDING status.
func (s *Service) CreateTask(ctx context.Context, in CreateTaskInput) (domain.DevelopmentTask, error) {
	if strings.TrimSpace(in.Title) == "" {
		return domain.DevelopmentTask{}, ErrInvalidInput
	}

	// Verify stage exists
	if _, ok, err := s.store.GetDevelopmentStage(ctx, in.StageID); err != nil {
		return domain.DevelopmentTask{}, err
	} else if !ok {
		return domain.DevelopmentTask{}, ErrNotFound
	}

	now := s.now()
	task := domain.DevelopmentTask{
		ID:                 domain.DevelopmentTaskID(s.newID()),
		StageID:            in.StageID,
		Sequence:           in.Sequence,
		Title:              strings.TrimSpace(in.Title),
		Description:        strings.TrimSpace(in.Description),
		TaskType:           strings.TrimSpace(in.TaskType),
		AcceptanceCriteria: strings.TrimSpace(in.AcceptanceCriteria),
		Status:             domain.TaskStatusPending,
		AgentRoleID:        in.AgentRoleID,
		ProviderID:         in.ProviderID,
		ProviderModelID:    in.ProviderModelID,
		CreatedAt:          now,
	}

	if err := s.store.CreateDevelopmentTask(ctx, task); err != nil {
		return domain.DevelopmentTask{}, err
	}
	return task, nil
}

// GetTask retrieves a development task by ID.
func (s *Service) GetTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
	task, ok, err := s.store.GetDevelopmentTask(ctx, id)
	if err != nil {
		return domain.DevelopmentTask{}, err
	}
	if !ok {
		return domain.DevelopmentTask{}, ErrNotFound
	}
	return task, nil
}

// ListTasksByStage lists all tasks for a stage.
func (s *Service) ListTasksByStage(ctx context.Context, stageID domain.DevelopmentStageID) ([]domain.DevelopmentTask, error) {
	return s.store.ListDevelopmentTasksByStage(ctx, stageID)
}

// ReadyTask transitions a task from PENDING or BLOCKED to READY.
func (s *Service) ReadyTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
	task, ok, err := s.store.GetDevelopmentTask(ctx, id)
	if err != nil {
		return domain.DevelopmentTask{}, err
	}
	if !ok {
		return domain.DevelopmentTask{}, ErrNotFound
	}

	if err := domain.ValidDevelopmentTaskTransition(task.Status, domain.TaskStatusReady); err != nil {
		return domain.DevelopmentTask{}, ErrInvalidTransition
	}

	if err := s.store.UpdateDevelopmentTaskStatus(ctx, id, domain.TaskStatusReady, nil, nil); err != nil {
		return domain.DevelopmentTask{}, err
	}

	task.Status = domain.TaskStatusReady
	return task, nil
}

// StartTask transitions a task from READY to RUNNING.
// Requires the parent stage to be IN_PROGRESS.
func (s *Service) StartTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
	task, ok, err := s.store.GetDevelopmentTask(ctx, id)
	if err != nil {
		return domain.DevelopmentTask{}, err
	}
	if !ok {
		return domain.DevelopmentTask{}, ErrNotFound
	}

	if err := domain.ValidDevelopmentTaskTransition(task.Status, domain.TaskStatusRunning); err != nil {
		return domain.DevelopmentTask{}, ErrInvalidTransition
	}

	// Verify stage is IN_PROGRESS
	stage, ok, err := s.store.GetDevelopmentStage(ctx, task.StageID)
	if err != nil {
		return domain.DevelopmentTask{}, err
	}
	if !ok {
		return domain.DevelopmentTask{}, ErrNotFound
	}
	if stage.Status != domain.StageStatusInProgress {
		return domain.DevelopmentTask{}, ErrInvalidTransition
	}

	now := s.now()
	if err := s.store.UpdateDevelopmentTaskStatus(ctx, id, domain.TaskStatusRunning, &now, nil); err != nil {
		return domain.DevelopmentTask{}, err
	}

	task.Status = domain.TaskStatusRunning
	task.StartedAt = &now
	return task, nil
}

// SubmitForReview transitions a task from RUNNING to REVIEW.
func (s *Service) SubmitForReview(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
	task, ok, err := s.store.GetDevelopmentTask(ctx, id)
	if err != nil {
		return domain.DevelopmentTask{}, err
	}
	if !ok {
		return domain.DevelopmentTask{}, ErrNotFound
	}

	if err := domain.ValidDevelopmentTaskTransition(task.Status, domain.TaskStatusReview); err != nil {
		return domain.DevelopmentTask{}, ErrInvalidTransition
	}

	if err := s.store.UpdateDevelopmentTaskStatus(ctx, id, domain.TaskStatusReview, nil, nil); err != nil {
		return domain.DevelopmentTask{}, err
	}

	task.Status = domain.TaskStatusReview
	return task, nil
}

// PassTask transitions a task from REVIEW to PASSED.
func (s *Service) PassTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
	task, ok, err := s.store.GetDevelopmentTask(ctx, id)
	if err != nil {
		return domain.DevelopmentTask{}, err
	}
	if !ok {
		return domain.DevelopmentTask{}, ErrNotFound
	}

	if err := domain.ValidDevelopmentTaskTransition(task.Status, domain.TaskStatusPassed); err != nil {
		return domain.DevelopmentTask{}, ErrInvalidTransition
	}

	now := s.now()
	if err := s.store.UpdateDevelopmentTaskStatus(ctx, id, domain.TaskStatusPassed, nil, &now); err != nil {
		return domain.DevelopmentTask{}, err
	}

	task.Status = domain.TaskStatusPassed
	task.CompletedAt = &now
	return task, nil
}

// RejectTask transitions a task from REVIEW to READY.
// This is NOT a REJECTED status - the task returns to READY for retry.
func (s *Service) RejectTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
	task, ok, err := s.store.GetDevelopmentTask(ctx, id)
	if err != nil {
		return domain.DevelopmentTask{}, err
	}
	if !ok {
		return domain.DevelopmentTask{}, ErrNotFound
	}

	if err := domain.ValidDevelopmentTaskTransition(task.Status, domain.TaskStatusReady); err != nil {
		return domain.DevelopmentTask{}, ErrInvalidTransition
	}

	if err := s.store.UpdateDevelopmentTaskStatus(ctx, id, domain.TaskStatusReady, nil, nil); err != nil {
		return domain.DevelopmentTask{}, err
	}

	task.Status = domain.TaskStatusReady
	return task, nil
}

// BlockTask transitions a task to BLOCKED.
func (s *Service) BlockTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
	task, ok, err := s.store.GetDevelopmentTask(ctx, id)
	if err != nil {
		return domain.DevelopmentTask{}, err
	}
	if !ok {
		return domain.DevelopmentTask{}, ErrNotFound
	}

	if err := domain.ValidDevelopmentTaskTransition(task.Status, domain.TaskStatusBlocked); err != nil {
		return domain.DevelopmentTask{}, ErrInvalidTransition
	}

	if err := s.store.UpdateDevelopmentTaskStatus(ctx, id, domain.TaskStatusBlocked, nil, nil); err != nil {
		return domain.DevelopmentTask{}, err
	}

	task.Status = domain.TaskStatusBlocked
	return task, nil
}

// UnblockTask transitions a task from BLOCKED to READY.
func (s *Service) UnblockTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
	task, ok, err := s.store.GetDevelopmentTask(ctx, id)
	if err != nil {
		return domain.DevelopmentTask{}, err
	}
	if !ok {
		return domain.DevelopmentTask{}, ErrNotFound
	}

	if err := domain.ValidDevelopmentTaskTransition(task.Status, domain.TaskStatusReady); err != nil {
		return domain.DevelopmentTask{}, ErrInvalidTransition
	}

	if err := s.store.UpdateDevelopmentTaskStatus(ctx, id, domain.TaskStatusReady, nil, nil); err != nil {
		return domain.DevelopmentTask{}, err
	}

	task.Status = domain.TaskStatusReady
	return task, nil
}

// CancelTask transitions a task to CANCELLED.
func (s *Service) CancelTask(ctx context.Context, id domain.DevelopmentTaskID) (domain.DevelopmentTask, error) {
	task, ok, err := s.store.GetDevelopmentTask(ctx, id)
	if err != nil {
		return domain.DevelopmentTask{}, err
	}
	if !ok {
		return domain.DevelopmentTask{}, ErrNotFound
	}

	if err := domain.ValidDevelopmentTaskTransition(task.Status, domain.TaskStatusCancelled); err != nil {
		return domain.DevelopmentTask{}, ErrInvalidTransition
	}

	now := s.now()
	if err := s.store.UpdateDevelopmentTaskStatus(ctx, id, domain.TaskStatusCancelled, nil, &now); err != nil {
		return domain.DevelopmentTask{}, err
	}

	task.Status = domain.TaskStatusCancelled
	task.CompletedAt = &now
	return task, nil
}

// AssignTask updates the agent role and provider assignment for a task.
func (s *Service) AssignTask(ctx context.Context, id domain.DevelopmentTaskID, roleID domain.AgentRoleID, providerID domain.ProviderID, modelID domain.ProviderModelID) (domain.DevelopmentTask, error) {
	task, ok, err := s.store.GetDevelopmentTask(ctx, id)
	if err != nil {
		return domain.DevelopmentTask{}, err
	}
	if !ok {
		return domain.DevelopmentTask{}, ErrNotFound
	}

	if err := s.store.UpdateDevelopmentTaskAssignment(ctx, id, roleID, providerID, modelID); err != nil {
		return domain.DevelopmentTask{}, err
	}

	task.AgentRoleID = roleID
	task.ProviderID = providerID
	task.ProviderModelID = modelID
	return task, nil
}
