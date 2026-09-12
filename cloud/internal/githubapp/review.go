package githubapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

// triggerReview starts a best-effort review in a dedicated sandbox process.
func (s *Service) triggerReview(ctx context.Context, orgID, sessionID string, pr domain.PullRequest) {
	run, created, err := s.store.CreateReviewRun(ctx, orgID, pr.ID, sessionID, pr.HeadSHA)
	if err != nil {
		s.logger.Error("create review run", "error", err, "pull_request_id", pr.ID)
		return
	}
	if !created {
		return
	}
	if err := s.store.OpenReviewTerminal(ctx, orgID, sessionID, run.ID, reviewPrompt(run.ID, pr)); err != nil {
		s.logger.Error("open review terminal", "error", err, "pull_request_id", pr.ID, "review_run_id", run.ID)
		// A run is durable before the terminal is queued. Queue failures must
		// resolve that durable record too; otherwise every client truthfully
		// renders a review as running forever even though it never started.
		if _, failErr := s.store.FailReviewRun(ctx, orgID, run.ID, sessionID, err.Error()); failErr != nil {
			s.logger.Error("fail review run after terminal open failure",
				"error", failErr, "pull_request_id", pr.ID, "review_run_id", run.ID)
		}
		s.closeReviewTerminal(ctx, orgID, sessionID, run.ID)
	}
}

func reviewPrompt(reviewRunID string, pr domain.PullRequest) string {
	return fmt.Sprintf(
		"You are AO's automated reviewer for one pull request: %s, #%d: %q, "+
			"%s into %s. This is a fresh session with no prior context — you did not "+
			"write this change. Start by running `git diff %s...%s` (and `git log`, `git show` "+
			"as needed) in the current workspace to see exactly what changed, then review it "+
			"for correctness, quality, and bugs as a careful human reviewer would, not just a "+
			"summary of the diff.\n\n"+
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
