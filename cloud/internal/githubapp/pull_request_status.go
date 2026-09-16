package githubapp

import (
	"context"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

// RefreshPullRequestStatus refreshes a pull request's durable GitHub status.
func (s *Service) RefreshPullRequestStatus(
	ctx context.Context,
	ref domain.PullRequestRef,
) (domain.PullRequest, error) {
	owner, repo, ok := strings.Cut(ref.Repository, "/")
	if !ok || owner == "" || repo == "" {
		return domain.PullRequest{}, postgres.ErrInvalid
	}
	installationID, repositoryID, err := s.store.GitHubInstallationForRepository(ctx, ref.OrgID, ref.Repository)
	if err != nil {
		return domain.PullRequest{}, err
	}
	access, err := s.client.statusReadToken(ctx, installationID, repositoryID)
	if err != nil {
		return domain.PullRequest{}, err
	}
	observation, err := observePullRequest(ctx, s.client, access.Token, owner, repo, ref.Number)
	if err != nil {
		return domain.PullRequest{}, err
	}
	return s.store.UpdatePullRequestObservation(ctx, ref.OrgID, ref.ID, observation)
}

// observePullRequest fetches one pull request's lifecycle, CI, review, and
// mergeability with any token that can read it (installation or PAT).
func observePullRequest(
	ctx context.Context,
	client *Client,
	token, owner, repo string,
	number int,
) (domain.PullRequestObservation, error) {
	detail, err := client.GetPullRequest(ctx, token, owner, repo, number)
	if err != nil {
		return domain.PullRequestObservation{}, err
	}
	var checks []CheckRun
	if detail.Head.SHA != "" {
		checks, err = client.ListCheckRuns(ctx, token, owner, repo, detail.Head.SHA)
		if err != nil {
			return domain.PullRequestObservation{}, err
		}
	}
	reviews, err := client.ListPullRequestReviews(ctx, token, owner, repo, number)
	if err != nil {
		return domain.PullRequestObservation{}, err
	}
	return domain.PullRequestObservation{
		State:        pullRequestLifecycleState(detail),
		Draft:        detail.Draft,
		HeadSHA:      detail.Head.SHA,
		Additions:    detail.Additions,
		Deletions:    detail.Deletions,
		ChangedFiles: detail.ChangedFiles,
		CIState:      aggregateCIState(checks),
		ReviewState:  aggregateReviewState(reviews),
		Mergeability: mapMergeability(detail),
	}, nil
}

func pullRequestLifecycleState(detail PullRequestDetail) contract.PRState {
	switch {
	case detail.Merged:
		return contract.PRStateMerged
	case detail.State == "closed":
		return contract.PRStateClosed
	case detail.Draft:
		return contract.PRStateDraft
	default:
		return contract.PRStateOpen
	}
}

func aggregateCIState(checks []CheckRun) contract.CIState {
	if len(checks) == 0 {
		return contract.CIUnknown
	}
	pending := false
	for _, check := range checks {
		if check.Status != "completed" {
			pending = true
			continue
		}
		switch check.Conclusion {
		case "failure", "timed_out", "action_required", "startup_failure":
			return contract.CIFailing
		case "success", "neutral", "skipped":
			continue
		default:
			pending = true
		}
	}
	if pending {
		return contract.CIPending
	}
	return contract.CIPassing
}

// aggregateReviewState uses each reviewer's latest decisive or dismissed review.
func aggregateReviewState(reviews []PullRequestReview) contract.ReviewDecision {
	latest := map[string]PullRequestReview{}
	for _, review := range reviews {
		switch review.State {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
		default:
			continue
		}
		reviewer := strings.TrimSpace(review.User.Login)
		if reviewer == "" {
			reviewer = "unknown"
		}
		current, ok := latest[reviewer]
		if !ok || reviewAfter(review, current) {
			latest[reviewer] = review
		}
	}
	approved := false
	for _, review := range latest {
		switch review.State {
		case "CHANGES_REQUESTED":
			return contract.ReviewChangesRequest
		case "APPROVED":
			approved = true
		}
	}
	if approved {
		return contract.ReviewApproved
	}
	return contract.ReviewNone
}

func reviewAfter(a, b PullRequestReview) bool {
	if a.SubmittedAt.IsZero() || b.SubmittedAt.IsZero() {
		return a.SubmittedAt.IsZero() == b.SubmittedAt.IsZero() && a.ID > b.ID
	}
	if a.SubmittedAt.Equal(b.SubmittedAt) {
		return a.ID > b.ID
	}
	return a.SubmittedAt.After(b.SubmittedAt)
}

func mapMergeability(detail PullRequestDetail) contract.Mergeability {
	switch detail.MergeableState {
	case "dirty":
		return contract.MergeConflicting
	case "blocked", "behind":
		return contract.MergeBlocked
	case "unstable":
		return contract.MergeUnstable
	case "clean":
		return contract.MergeMergeable
	default:
		return contract.MergeUnknown
	}
}
