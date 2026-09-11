package githubapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

// TriggerReview starts one review in a dedicated agent terminal. Opening or
// claiming a PR intentionally does not invoke it: cloud must follow the same
// explicit "Run review" action as local sessions.
func (s *Service) TriggerReview(ctx context.Context, orgID, sessionID string, pr domain.PullRequest) (domain.ReviewRun, bool, error) {
	run, created, err := s.store.CreateReviewRun(ctx, orgID, pr.ID, sessionID, pr.HeadSHA)
	if err != nil {
		return domain.ReviewRun{}, false, err
	}
	if !created {
		return run, false, nil
	}
	if err := s.store.OpenReviewTerminal(ctx, orgID, sessionID, run.ID, reviewPrompt(run.ID, pr)); err != nil {
		// A run is durable before the terminal is queued. Queue failures must
		// resolve that durable record too; otherwise every client truthfully
		// renders a review as running forever even though it never started.
		_, _ = s.store.FailReviewRun(ctx, orgID, run.ID, sessionID, err.Error())
		s.closeReviewTerminal(ctx, orgID, sessionID, run.ID)
		return domain.ReviewRun{}, false, err
	}
	return run, true, nil
}

// CancelReviews makes cancellation durable before requesting terminal
// teardown, so a disconnected worker cannot leave a stuck running review.
func (s *Service) CancelReviews(ctx context.Context, orgID, sessionID string) ([]domain.ReviewRun, error) {
	runs, err := s.store.CancelRunningReviewRunsBySession(ctx, orgID, sessionID)
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		s.closeReviewTerminal(ctx, orgID, sessionID, run.ID)
	}
	return runs, nil
}

func reviewPrompt(reviewRunID string, pr domain.PullRequest) string {
	return fmt.Sprintf(
		"You are AO's automated reviewer for one pull request: %s, #%d: %q, "+
			"%s into %s. This is a fresh session with no prior context — you did not "+
			"write this change. Start by running `git diff %s...%s` (and `git log`, `git show` "+
			"as needed) in the current workspace to see exactly what changed, then review it "+
			"for correctness bugs, missing error handling, security issues, test coverage, and "+
			"clear deviations from the surrounding code's conventions. Prefer a few high-confidence "+
			"findings over nitpicks. Do not edit files, push commits, or modify the branch.\n\n"+
			"When you are done, submit your verdict by POSTing to $AO_REVIEW_SOCKET: $AO_REVIEW_HELP\n\n"+
			"Use reviewRunId %q, verdict \"approved\" if the change looks correct and ready to merge, "+
			"or \"changes_requested\" if you found problems that should be fixed first, and a body "+
			"explaining your findings.",
		pr.Repository, pr.Number, pr.Title, pr.SourceBranch, pr.TargetBranch,
		pr.TargetBranch, pr.SourceBranch, reviewRunID,
	)
}

// SubmitReview posts and records a review session's verdict.
func (s *Service) SubmitReview(
	ctx context.Context,
	orgID, sessionID, reviewRunID string,
	result domain.SubmitReviewResult,
) (domain.ReviewRun, error) {
	if !result.Verdict.Valid() {
		return domain.ReviewRun{}, fmt.Errorf("%w: verdict must be approved or changes_requested", postgres.ErrInvalid)
	}
	body := strings.TrimSpace(result.Body)
	if body == "" {
		return domain.ReviewRun{}, fmt.Errorf("%w: a review body is required", postgres.ErrInvalid)
	}
	run, err := s.store.ReviewRunPullRequest(ctx, orgID, reviewRunID)
	if err != nil {
		return domain.ReviewRun{}, err
	}
	if run.ReviewSessionID != sessionID {
		return domain.ReviewRun{}, postgres.ErrForbidden
	}
	if run.Status != contract.AOReviewRunRunning {
		return domain.ReviewRun{}, fmt.Errorf("%w: this review has already been resolved", postgres.ErrInvalid)
	}
	owner, repo, ok := strings.Cut(run.PullRequestRepository, "/")
	if !ok || owner == "" || repo == "" {
		return domain.ReviewRun{}, postgres.ErrInvalid
	}
	installationID, repositoryID, err := s.store.GitHubInstallationForRepository(ctx, orgID, run.PullRequestRepository)
	if err != nil {
		return s.failReview(ctx, orgID, sessionID, reviewRunID, err)
	}
	access, err := s.client.repositoryWriteToken(ctx, installationID, repositoryID)
	if err != nil {
		return s.failReview(ctx, orgID, sessionID, reviewRunID, err)
	}
	providerReviewID, err := s.client.CreatePullRequestReview(
		ctx, access.Token, owner, repo, run.PullRequestNumber, body,
	)
	if err != nil {
		return s.failReview(ctx, orgID, sessionID, reviewRunID, err)
	}
	delivered, err := s.store.CompleteAndDeliverReviewRun(
		ctx, orgID, reviewRunID, sessionID,
		domain.SubmitReviewResult{Verdict: result.Verdict, Body: body},
		formatProviderReviewID(providerReviewID),
	)
	if err != nil {
		return domain.ReviewRun{}, err
	}
	s.closeReviewTerminal(ctx, orgID, sessionID, reviewRunID)
	return delivered, nil
}

func (s *Service) failReview(
	ctx context.Context, orgID, sessionID, reviewRunID string, cause error,
) (domain.ReviewRun, error) {
	failed, failErr := s.store.FailReviewRun(ctx, orgID, reviewRunID, sessionID, cause.Error())
	s.closeReviewTerminal(ctx, orgID, sessionID, reviewRunID)
	if failErr == nil {
		return failed, cause
	}
	return domain.ReviewRun{}, cause
}

func (s *Service) closeReviewTerminal(ctx context.Context, orgID, sessionID, reviewRunID string) {
	if err := s.store.CloseReviewTerminal(ctx, orgID, sessionID, reviewRunID); err != nil {
		s.logger.Error("close review terminal", "error", err, "review_run_id", reviewRunID)
	}
}

func formatProviderReviewID(id int64) string {
	if id <= 0 {
		return ""
	}
	return fmt.Sprintf("%d", id)
}
