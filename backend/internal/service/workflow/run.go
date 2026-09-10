package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// CreateRunInput describes a new task run.
type CreateRunInput struct {
	TaskID domain.DevelopmentTaskID
}

// CreateRun creates a new task run in PENDING status.
// It does NOT spawn a session — call StartRun to begin execution.
// The task must be in READY status. No concurrent runs allowed per task.
func (s *Service) CreateRun(ctx context.Context, in CreateRunInput) (domain.TaskRun, error) {
	if in.TaskID == "" {
		return domain.TaskRun{}, ErrInvalidInput
	}

	task, ok, err := s.store.GetDevelopmentTask(ctx, in.TaskID)
	if err != nil {
		return domain.TaskRun{}, err
	}
	if !ok {
		return domain.TaskRun{}, ErrNotFound
	}
	if task.Status != domain.TaskStatusReady {
		return domain.TaskRun{}, ErrInvalidTransition
	}

	runs, err := s.store.ListTaskRunsByTask(ctx, in.TaskID)
	if err != nil {
		return domain.TaskRun{}, err
	}
	for _, r := range runs {
		if r.Status == domain.RunStatusPending || r.Status == domain.RunStatusRunning {
			return domain.TaskRun{}, ErrConflict
		}
	}

	now := s.now()
	run := domain.TaskRun{
		ID:            domain.TaskRunID(s.newID()),
		TaskID:        in.TaskID,
		Attempt:       len(runs) + 1,
		AgentRoleID:   task.AgentRoleID,
		Status:        domain.RunStatusPending,
		CreatedAt:     now,
	}

	if err := s.store.CreateTaskRun(ctx, run); err != nil {
		return domain.TaskRun{}, err
	}
	return run, nil
}

// StartRun spawns a session and transitions the run to RUNNING.
// Sequence: Resolve Role → Resolve Provider → Spawn → Bind → snapshot → Task→RUNNING → Run→RUNNING.
// If Spawn fails, the run stays PENDING and task stays READY (no state change).
// Phase 2.4: Three-level resolution with fail-first semantics (no silent fallback).
func (s *Service) StartRun(ctx context.Context, id domain.TaskRunID) (domain.TaskRun, error) {
	if s.runtime == nil {
		return domain.TaskRun{}, fmt.Errorf("workflow: no session runtime configured")
	}

	run, ok, err := s.store.GetTaskRun(ctx, id)
	if err != nil {
		return domain.TaskRun{}, err
	}
	if !ok {
		return domain.TaskRun{}, ErrNotFound
	}
	if run.Status != domain.RunStatusPending {
		return domain.TaskRun{}, ErrInvalidTransition
	}

	task, ok, err := s.store.GetDevelopmentTask(ctx, run.TaskID)
	if err != nil {
		return domain.TaskRun{}, err
	}
	if !ok {
		return domain.TaskRun{}, ErrNotFound
	}
	if task.Status != domain.TaskStatusReady {
		return domain.TaskRun{}, ErrInvalidTransition
	}

	stage, ok, err := s.store.GetDevelopmentStage(ctx, task.StageID)
	if err != nil {
		return domain.TaskRun{}, err
	}
	if !ok {
		return domain.TaskRun{}, ErrNotFound
	}

	plan, ok, err := s.store.GetDevelopmentPlan(ctx, stage.PlanID)
	if err != nil {
		return domain.TaskRun{}, err
	}
	if !ok {
		return domain.TaskRun{}, ErrNotFound
	}

	// STEP 1: Resolve Role (independent of Provider)
	role, err := s.resolveRole(ctx, task)
	if err != nil {
		return domain.TaskRun{}, err
	}

	// STEP 2: Resolve Provider (Task explicit > AgentRole default > System default)
	providerID, modelID, err := s.resolveProvider(ctx, task, role)
	if err != nil {
		return domain.TaskRun{}, err
	}

	// STEP 3: SystemPrompt (always if role != nil, regardless of Provider source)
	systemPrompt := ""
	if role != nil && role.SystemPrompt != "" {
		systemPrompt = role.SystemPrompt
	}

	prompt := task.Description
	if prompt == "" {
		prompt = task.Title
	}

	cfg := ports.SpawnConfig{
		ProjectID:       plan.ProjectID,
		Kind:            domain.SessionKind("worker"),
		Harness:         domain.AgentHarness("claude-code"),
		Prompt:          prompt,
		SystemPrompt:    systemPrompt,
		ProviderID:      providerID,
		ProviderModelID: modelID,
		DisplayName:     task.Title,
	}

	session, err := s.runtime.SpawnSession(ctx, cfg)
	if err != nil {
		return domain.TaskRun{}, fmt.Errorf("workflow: spawn session: %w", err)
	}

	// Bind session_id (one-shot: WHERE session_id='')
	if err := s.store.BindTaskRunSession(ctx, run.ID, session.ID); err != nil {
		return domain.TaskRun{}, fmt.Errorf("workflow: bind session: %w", err)
	}
	run.SessionID = session.ID

	// Runtime snapshot: write actual SessionRecord truth into TaskRun
	snapProviderID := session.Metadata.ProviderID
	snapModelID := session.Metadata.ProviderModelID
	snapDisplayName := session.Metadata.ProviderDisplayName
	snapModelName := session.Metadata.ProviderModelName
	snapHarness := string(session.Harness)
	if snapProviderID == "" {
		snapProviderID = providerID
	}
	if snapModelID == "" {
		snapModelID = modelID
	}
	if err := s.store.UpdateTaskRunSnapshot(ctx, run.ID, session.ID,
		snapProviderID, snapModelID, snapDisplayName, snapModelName, snapHarness); err != nil {
		return domain.TaskRun{}, fmt.Errorf("workflow: snapshot session: %w", err)
	}
	run.ProviderID = snapProviderID
	run.ProviderModelID = snapModelID
	run.ProviderDisplayName = snapDisplayName
	run.ProviderModelName = snapModelName
	run.ExecutorType = snapHarness

	// Task → RUNNING (COALESCE: only sets started_at if currently NULL)
	now := s.now()
	_ = s.store.UpdateDevelopmentTaskStatus(ctx, run.TaskID, domain.TaskStatusRunning, &now, nil)

	// Run → RUNNING
	if err := s.store.UpdateTaskRunStatus(ctx, run.ID, domain.RunStatusRunning, "", "", &now, nil); err != nil {
		return domain.TaskRun{}, err
	}
	run.Status = domain.RunStatusRunning
	run.StartedAt = &now

	return run, nil
}

// CancelRun transitions a run to CANCELLED and kills the session if active.
// PENDING: cancel directly (no session).
// RUNNING: must Kill session. If Kill fails and session still alive, return error.
func (s *Service) CancelRun(ctx context.Context, id domain.TaskRunID) (domain.TaskRun, error) {
	run, ok, err := s.store.GetTaskRun(ctx, id)
	if err != nil {
		return domain.TaskRun{}, err
	}
	if !ok {
		return domain.TaskRun{}, ErrNotFound
	}
	if err := domain.ValidTaskRunTransition(run.Status, domain.RunStatusCancelled); err != nil {
		return domain.TaskRun{}, ErrInvalidTransition
	}

	// For RUNNING runs, must ensure session is dead before marking cancelled.
	if run.Status == domain.RunStatusRunning && run.SessionID != "" && s.runtime != nil {
		killed, killErr := s.runtime.KillSession(ctx, run.SessionID)
		if killErr != nil || !killed {
			// Verify session is actually terminated despite Kill failure
			session, sessOK, sessErr := s.store.GetSession(ctx, run.SessionID)
			if sessErr != nil || !sessOK || !session.IsTerminated {
				return domain.TaskRun{}, fmt.Errorf("workflow: kill session failed and session still alive: %w", killErr)
			}
		}
	}

	now := s.now()
	if err := s.store.UpdateTaskRunStatus(ctx, run.ID, domain.RunStatusCancelled, "", "", nil, &now); err != nil {
		return domain.TaskRun{}, err
	}
	run.Status = domain.RunStatusCancelled
	run.FinishedAt = &now
	return run, nil
}

// GetRun retrieves a task run by ID.
func (s *Service) GetRun(ctx context.Context, id domain.TaskRunID) (domain.TaskRun, error) {
	run, ok, err := s.store.GetTaskRun(ctx, id)
	if err != nil {
		return domain.TaskRun{}, err
	}
	if !ok {
		return domain.TaskRun{}, ErrNotFound
	}
	return run, nil
}

// ListRunsByTask lists all runs for a task, ordered by attempt.
func (s *Service) ListRunsByTask(ctx context.Context, taskID domain.DevelopmentTaskID) ([]domain.TaskRun, error) {
	return s.store.ListTaskRunsByTask(ctx, taskID)
}

// TerminationToRunStatus maps a session's TerminationReason to a TaskRunStatus.
// ABSOLUTE RULE: process-exited → SUCCEEDED is FORBIDDEN.
func TerminationToRunStatus(reason string) domain.TaskRunStatus {
	switch reason {
	case "session-end":
		return domain.RunStatusSucceeded
	case "explicit":
		return domain.RunStatusCancelled
	case "process-exited", "reaper", "":
		return domain.RunStatusFailed
	default:
		return domain.RunStatusFailed
	}
}

// ReconcileRunningRuns checks all RUNNING (and orphan PENDING+session_id) runs
// and finalizes those whose session has terminated.
func (s *Service) ReconcileRunningRuns(ctx context.Context) (reconciled int, err error) {
	runs, err := s.store.ListTaskRunsByStatus(ctx, domain.RunStatusRunning)
	if err != nil {
		return 0, err
	}

	now := s.now()
	for _, run := range runs {
		if run.SessionID == "" {
			continue
		}

		session, ok, err := s.store.GetSession(ctx, run.SessionID)
		if err != nil {
			slog.Error("workflow: reconcile get session", "run", run.ID, "session", run.SessionID, "err", err)
			continue
		}
		if !ok || !session.IsTerminated {
			continue
		}

		finalStatus := TerminationToRunStatus(session.Metadata.TerminationReason)
		resultSummary := string(finalStatus)
		errorMsg := ""
		if finalStatus == domain.RunStatusFailed {
			errorMsg = "session terminated: " + session.Metadata.TerminationReason
		}

		if err := s.store.UpdateTaskRunStatus(ctx, run.ID, finalStatus, resultSummary, errorMsg, nil, &now); err != nil {
			slog.Error("workflow: reconcile update run", "run", run.ID, "err", err)
			continue
		}
		reconciled++
	}

	return reconciled, nil
}

// ReconcileInterval is the default reconcile tick interval.
const ReconcileInterval = 30 * time.Second

// StartCompletionObserver starts a background goroutine that periodically
// calls ReconcileRunningRuns. It returns immediately; the goroutine stops
// when ctx is cancelled.
func (s *Service) StartCompletionObserver(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = ReconcileInterval
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				n, err := s.ReconcileRunningRuns(ctx)
				if err != nil {
					slog.Error("workflow: reconcile error", "err", err)
				} else if n > 0 {
					slog.Info("workflow: reconciled runs", "count", n)
				}
			}
		}
	}()
}
