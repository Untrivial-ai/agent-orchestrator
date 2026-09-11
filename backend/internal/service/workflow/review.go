package workflow

import (
	"context"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// CreateRunReview creates a RunReview for a SUCCEEDED run whose task is in REVIEW status.
func (s *Service) CreateRunReview(ctx context.Context, runID domain.TaskRunID, source domain.RunReviewSource, summary, issues string) (domain.RunReview, error) {
	if !source.Valid() {
		return domain.RunReview{}, ErrInvalidInput
	}

	// Run must exist and be SUCCEEDED.
	run, ok, err := s.store.GetTaskRun(ctx, runID)
	if err != nil {
		return domain.RunReview{}, err
	}
	if !ok {
		return domain.RunReview{}, ErrNotFound
	}
	if run.Status != domain.RunStatusSucceeded {
		return domain.RunReview{}, ErrInvalidTransition
	}

	// Parent task must be in REVIEW status.
	task, ok, err := s.store.GetDevelopmentTask(ctx, run.TaskID)
	if err != nil {
		return domain.RunReview{}, err
	}
	if !ok {
		return domain.RunReview{}, ErrNotFound
	}
	if task.Status != domain.TaskStatusReview {
		return domain.RunReview{}, ErrInvalidTransition
	}

	// One review per run (business check; DB UNIQUE is the concurrency backstop).
	if _, found, err := s.store.GetRunReviewByRunID(ctx, runID); err != nil {
		return domain.RunReview{}, err
	} else if found {
		return domain.RunReview{}, ErrConflict
	}

	now := s.now()
	review := domain.RunReview{
		ID:        domain.RunReviewID(s.newID()),
		RunID:     runID,
		Source:    source,
		Status:    domain.RunReviewStatusPending,
		Summary:   strings.TrimSpace(summary),
		Issues:    strings.TrimSpace(issues),
		CreatedAt: now,
	}

	if err := s.store.CreateRunReview(ctx, review); err != nil {
		return domain.RunReview{}, err
	}
	return review, nil
}

// GetRunReview retrieves a RunReview by ID.
func (s *Service) GetRunReview(ctx context.Context, id domain.RunReviewID) (domain.RunReview, error) {
	review, ok, err := s.store.GetRunReview(ctx, id)
	if err != nil {
		return domain.RunReview{}, err
	}
	if !ok {
		return domain.RunReview{}, ErrNotFound
	}
	return review, nil
}

// ListRunReviewsByRun lists all reviews for a run.
// Returns ErrNotFound if the run does not exist; returns empty slice if the run exists but has no reviews.
func (s *Service) ListRunReviewsByRun(ctx context.Context, runID domain.TaskRunID) ([]domain.RunReview, error) {
	if _, ok, err := s.store.GetTaskRun(ctx, runID); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrNotFound
	}
	return s.store.ListRunReviewsByRun(ctx, runID)
}

// PassRunReview atomically transitions a review to PASSED and its parent task to PASSED.
func (s *Service) PassRunReview(ctx context.Context, reviewID domain.RunReviewID) (domain.RunReview, error) {
	review, ok, err := s.store.GetRunReview(ctx, reviewID)
	if err != nil {
		return domain.RunReview{}, err
	}
	if !ok {
		return domain.RunReview{}, ErrNotFound
	}
	if review.Status.IsTerminal() {
		return domain.RunReview{}, ErrInvalidTransition
	}

	completedAt := s.now()
	if err := s.store.PassRunReviewTx(ctx, reviewID, completedAt); err != nil {
		return domain.RunReview{}, err
	}

	review.Status = domain.RunReviewStatusPassed
	review.CompletedAt = &completedAt
	return review, nil
}

// RejectRunReview atomically transitions a review to REJECTED and its parent task to READY.
// Issues resolution: prefer rejectIssues, fallback to review.Issues (from Create time).
// Effective issues must be non-empty after trimming.
func (s *Service) RejectRunReview(ctx context.Context, reviewID domain.RunReviewID, rejectIssues string) (domain.RunReview, error) {
	review, ok, err := s.store.GetRunReview(ctx, reviewID)
	if err != nil {
		return domain.RunReview{}, err
	}
	if !ok {
		return domain.RunReview{}, ErrNotFound
	}
	if review.Status.IsTerminal() {
		return domain.RunReview{}, ErrInvalidTransition
	}

	effectiveIssues := strings.TrimSpace(rejectIssues)
	if effectiveIssues == "" {
		effectiveIssues = strings.TrimSpace(review.Issues)
	}
	if effectiveIssues == "" {
		return domain.RunReview{}, ErrInvalidInput
	}

	completedAt := s.now()
	if err := s.store.RejectRunReviewTx(ctx, reviewID, completedAt, effectiveIssues); err != nil {
		return domain.RunReview{}, err
	}

	review.Status = domain.RunReviewStatusRejected
	review.Issues = effectiveIssues
	review.CompletedAt = &completedAt
	return review, nil
}
