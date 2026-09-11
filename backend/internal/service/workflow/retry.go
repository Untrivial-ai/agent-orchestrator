package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// CreateRetryRunInput describes a retry for a rejected review.
type CreateRetryRunInput struct {
	PreviousRunID domain.TaskRunID
	Mode          string // "", "resume", "fresh"
}

// CreateRetryRun creates a new PENDING TaskRun that retries a previously rejected run.
// The previous run must be SUCCEEDED, have a rejected review with non-empty issues,
// and the task must be READY with no other pending/running runs.
func (s *Service) CreateRetryRun(ctx context.Context, in CreateRetryRunInput) (domain.TaskRun, error) {
	// Normalize mode.
	mode := strings.TrimSpace(in.Mode)
	if mode == "" {
		mode = "resume"
	}
	if mode != "resume" && mode != "fresh" {
		return domain.TaskRun{}, ErrInvalidInput
	}

	// PreviousRun must exist.
	prevRun, ok, err := s.store.GetTaskRun(ctx, in.PreviousRunID)
	if err != nil {
		return domain.TaskRun{}, err
	}
	if !ok {
		return domain.TaskRun{}, ErrNotFound
	}

	// PreviousRun must be SUCCEEDED.
	if prevRun.Status != domain.RunStatusSucceeded {
		return domain.TaskRun{}, ErrInvalidTransition
	}

	// Task must exist.
	task, ok, err := s.store.GetDevelopmentTask(ctx, prevRun.TaskID)
	if err != nil {
		return domain.TaskRun{}, err
	}
	if !ok {
		return domain.TaskRun{}, ErrNotFound
	}

	// Task must be READY.
	if task.Status != domain.TaskStatusReady {
		return domain.TaskRun{}, ErrInvalidTransition
	}

	// Review must exist and be REJECTED with non-empty issues.
	review, found, err := s.store.GetRunReviewByRunID(ctx, prevRun.ID)
	if err != nil {
		return domain.TaskRun{}, err
	}
	if !found {
		return domain.TaskRun{}, ErrNotFound
	}
	if review.Status != domain.RunReviewStatusRejected {
		return domain.TaskRun{}, ErrInvalidTransition
	}
	if strings.TrimSpace(review.Issues) == "" {
		return domain.TaskRun{}, ErrInvalidInput
	}

	// No concurrent PENDING/RUNNING runs for this task.
	existingRuns, err := s.store.ListTaskRunsByTask(ctx, prevRun.TaskID)
	if err != nil {
		return domain.TaskRun{}, err
	}
	for _, r := range existingRuns {
		if r.Status == domain.RunStatusPending || r.Status == domain.RunStatusRunning {
			return domain.TaskRun{}, ErrConflict
		}
	}

	// Compute attempt: MAX(attempt) + 1.
	maxAttempt := 0
	for _, r := range existingRuns {
		if r.Attempt > maxAttempt {
			maxAttempt = r.Attempt
		}
	}

	now := s.now()
	run := domain.TaskRun{
		ID:            domain.TaskRunID(s.newID()),
		TaskID:        prevRun.TaskID,
		Attempt:       maxAttempt + 1,
		AgentRoleID:   task.AgentRoleID,
		Status:        domain.RunStatusPending,
		PreviousRunID: prevRun.ID,
		RetryMode:     mode,
		CreatedAt:     now,
	}

	if err := s.store.CreateTaskRun(ctx, run); err != nil {
		return domain.TaskRun{}, err
	}
	return run, nil
}

// buildCorrectionPrompt builds the stable-identity correction prompt for a retry.
func buildCorrectionPrompt(retryRunID domain.TaskRunID, previousRunID domain.TaskRunID, reviewID domain.RunReviewID, issues string) string {
	return fmt.Sprintf("Workflow retry correction\nRetry Run: %s\nPrevious Run: %s\nReview: %s\n\nReview feedback:\n%s\n\nPlease continue from the existing session and address these findings.\nRe-run the relevant validation before finishing.",
		retryRunID, previousRunID, reviewID, issues)
}

// startRetryResume handles RESUME mode for a retry run.
// It restores/resumes the existing session, binds if needed, and sends the correction prompt.
func (s *Service) startRetryResume(ctx context.Context, run domain.TaskRun, prevRun domain.TaskRun) (domain.TaskRun, error) {
	if s.runtime == nil {
		return domain.TaskRun{}, fmt.Errorf("workflow: no session runtime configured")
	}

	// Determine target session.
	targetSessionID := domain.SessionID(string(run.SessionID))
	if targetSessionID == "" {
		targetSessionID = prevRun.SessionID
	}
	if targetSessionID == "" {
		return domain.TaskRun{}, ErrInvalidTransition
	}

	// Read session state.
	session, ok, err := s.store.GetSession(ctx, targetSessionID)
	if err != nil {
		return domain.TaskRun{}, fmt.Errorf("workflow: retry get session: %w", err)
	}
	if !ok {
		return domain.TaskRun{}, ErrNotFound
	}

	// Dispatch based on session state.
	if session.IsTerminated {
		// Restore terminated session.
		if _, err := s.runtime.RestoreSession(ctx, targetSessionID); err != nil {
			return domain.TaskRun{}, fmt.Errorf("workflow: retry restore: %w", err)
		}
	} else if session.Activity.State == domain.ActivityExited {
		// Agent exited but session still live — resume agent.
		if _, err := s.runtime.ResumeAgentSession(ctx, targetSessionID); err != nil {
			return domain.TaskRun{}, fmt.Errorf("workflow: retry resume agent: %w", err)
		}
	}
	// else: session live — no-op for restore/resume.

	// Bind if not yet bound.
	if run.SessionID == "" {
		if err := s.store.BindTaskRunSession(ctx, run.ID, targetSessionID); err != nil {
			return domain.TaskRun{}, fmt.Errorf("workflow: retry bind: %w", err)
		}
		run.SessionID = targetSessionID
	}

	// Build and send correction prompt with dedup.
	review, found, err := s.store.GetRunReviewByRunID(ctx, prevRun.ID)
	if err != nil {
		return domain.TaskRun{}, fmt.Errorf("workflow: retry get review: %w", err)
	}
	if !found {
		return domain.TaskRun{}, ErrNotFound
	}

	correctionPrompt := buildCorrectionPrompt(
		run.ID,
		prevRun.ID,
		review.ID,
		review.Issues,
	)

	// Dedup check.
	expectedDedup := CanonicalizePrompt(correctionPrompt)
	if session.Metadata.LatestUserPrompt != expectedDedup {
		if err := s.runtime.SendSession(ctx, targetSessionID, correctionPrompt); err != nil {
			return domain.TaskRun{}, fmt.Errorf("workflow: retry send: %w", err)
		}
		// Record for future dedup.
		s.store.RecordSessionLatestUserPrompt(ctx, targetSessionID, expectedDedup, s.now())
	}

	// Transition to RUNNING.
	now := s.now()
	if err := s.store.UpdateTaskRunStatus(ctx, run.ID, domain.RunStatusRunning, "", "", &now, nil); err != nil {
		return domain.TaskRun{}, err
	}
	run.Status = domain.RunStatusRunning
	run.StartedAt = &now

	_ = s.store.UpdateDevelopmentTaskStatus(ctx, run.TaskID, domain.TaskStatusRunning, &now, nil)

	return run, nil
}

// startRetryFresh handles FRESH mode for a retry run.
// It spawns a new session with current role/provider/prompt configuration.
func (s *Service) startRetryFresh(ctx context.Context, run domain.TaskRun, prevRun domain.TaskRun) (domain.TaskRun, error) {
	if s.runtime == nil {
		return domain.TaskRun{}, fmt.Errorf("workflow: no session runtime configured")
	}

	// Need task for title/description.
	task, ok, err := s.store.GetDevelopmentTask(ctx, run.TaskID)
	if err != nil {
		return domain.TaskRun{}, err
	}
	if !ok {
		return domain.TaskRun{}, ErrNotFound
	}

	// Need plan for project ID.
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

	// Resolve Role/Provider (same as normal StartRun).
	role, err := s.resolveRole(ctx, task)
	if err != nil {
		return domain.TaskRun{}, err
	}
	providerID, modelID, err := s.resolveProvider(ctx, task, role)
	if err != nil {
		return domain.TaskRun{}, err
	}

	systemPrompt := ""
	if role != nil && role.SystemPrompt != "" {
		systemPrompt = role.SystemPrompt
	}

	// Build prompt with review issues.
	review, found, err := s.store.GetRunReviewByRunID(ctx, prevRun.ID)
	if err != nil {
		return domain.TaskRun{}, fmt.Errorf("workflow: fresh get review: %w", err)
	}
	if !found {
		return domain.TaskRun{}, ErrNotFound
	}

	prompt := task.Description
	if prompt == "" {
		prompt = task.Title
	}
	retryPrefix := fmt.Sprintf("Workflow retry correction\nRetry Run: %s\nPrevious Run: %s\nReview: %s\n\nReview feedback:\n%s\n\nOriginal task:\n",
		run.ID, prevRun.ID, review.ID, review.Issues)
	prompt = retryPrefix + prompt

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
		// Run stays PENDING — no state change.
		return domain.TaskRun{}, fmt.Errorf("workflow: retry spawn: %w", err)
	}

	// Bind session.
	if err := s.store.BindTaskRunSession(ctx, run.ID, session.ID); err != nil {
		return domain.TaskRun{}, fmt.Errorf("workflow: retry bind: %w", err)
	}
	run.SessionID = session.ID

	// Snapshot.
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
		return domain.TaskRun{}, fmt.Errorf("workflow: retry snapshot: %w", err)
	}

	// Transition to RUNNING.
	now := s.now()
	if err := s.store.UpdateTaskRunStatus(ctx, run.ID, domain.RunStatusRunning, "", "", &now, nil); err != nil {
		return domain.TaskRun{}, err
	}
	run.Status = domain.RunStatusRunning
	run.StartedAt = &now

	_ = s.store.UpdateDevelopmentTaskStatus(ctx, run.TaskID, domain.TaskStatusRunning, &now, nil)

	return run, nil
}

// ReconcileWorkflowStateConsistency ensures task states match their latest run.
// Only the latest attempt run can drive task state compensation.
func (s *Service) ReconcileWorkflowStateConsistency(ctx context.Context) (compensated int, err error) {
	// Get all tasks that might need compensation: those in RUNNING status.
	// We check via their latest run.
	runningTasks := make(map[domain.DevelopmentTaskID]struct{})

	// Collect task IDs from RUNNING runs.
	runs, err := s.store.ListTaskRunsByStatus(ctx, domain.RunStatusRunning)
	if err != nil {
		return 0, err
	}
	for _, r := range runs {
		runningTasks[r.TaskID] = struct{}{}
	}

	// Also collect from SUCCEEDED/FAILED runs (they might have terminal runs
	// whose tasks are stuck in RUNNING).
	terminalRuns, err := s.store.ListTaskRunsByStatus(ctx, domain.RunStatusSucceeded)
	if err != nil {
		return 0, err
	}
	for _, r := range terminalRuns {
		runningTasks[r.TaskID] = struct{}{}
	}
	failedRuns, err := s.store.ListTaskRunsByStatus(ctx, domain.RunStatusFailed)
	if err != nil {
		return 0, err
	}
	for _, r := range failedRuns {
		runningTasks[r.TaskID] = struct{}{}
	}

	for taskID := range runningTasks {
		task, ok, err := s.store.GetDevelopmentTask(ctx, taskID)
		if err != nil || !ok {
			continue
		}

		// Only compensate RUNNING tasks.
		if task.Status != domain.TaskStatusRunning {
			continue
		}

		// Get latest run (highest attempt).
		latestRun, ok, err := s.store.GetLatestTaskRunByTask(ctx, taskID)
		if err != nil || !ok {
			continue
		}

		switch latestRun.Status {
		case domain.RunStatusSucceeded:
			if err := s.store.UpdateDevelopmentTaskStatus(ctx, taskID, domain.TaskStatusReview, nil, nil); err != nil {
				continue
			}
			compensated++
		case domain.RunStatusFailed:
			if err := s.store.UpdateDevelopmentTaskStatus(ctx, taskID, domain.TaskStatusReady, nil, nil); err != nil {
				continue
			}
			compensated++
		}
		// RUNNING, PENDING: no-op
	}

	return compensated, nil
}
