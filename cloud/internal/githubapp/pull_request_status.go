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
	if _, _, ok := strings.Cut(ref.Repository, "/"); !ok {
		return domain.PullRequest{}, postgres.ErrInvalid
	}
	snapshot, err := s.FetchPullRequestSnapshot(ctx, ref)
	if err != nil {
		return domain.PullRequest{}, err
	}
	transition, err := s.store.ApplyPullRequestSnapshot(ctx, ref.OrgID, ref.ID, snapshot)
	if err != nil {
		return domain.PullRequest{}, err
	}
	return transition.Current, nil
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
		return contract.CIPassing
	}
	pending := false
	for _, check := range checks {
		if check.Status != "completed" {
			pending = true
			continue
		}
		switch check.Conclusion {
		case "failure", "timed_out", "action_required", "startup_failure", "cancelled":
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
