package githubapp

import (
	"context"
	"strings"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

// RefreshPullRequestStatus refreshes a pull request's durable GitHub status.
func (s *Service) RefreshPullRequestStatus(
	ctx context.Context,
	ref domain.PullRequestRef,
	refresh domain.PullRequestRefreshContext,
) (domain.PullRequest, error) {
	if _, _, ok := strings.Cut(ref.Repository, "/"); !ok {
		return domain.PullRequest{}, postgres.ErrInvalid
	}
	snapshot, err := s.FetchPullRequestSnapshot(ctx, ref)
	if err != nil {
		return domain.PullRequest{}, err
	}
	transition, err := s.store.ApplyPullRequestSnapshot(ctx, ref.OrgID, ref.ID, snapshot, refresh)
	if err != nil {
		return domain.PullRequest{}, err
	}
	return transition.Current, nil
}
