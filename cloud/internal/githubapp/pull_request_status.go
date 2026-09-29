package githubapp

import (
	"context"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

const (
	// mergeabilityRetryDelay must stay comfortably above the fallback scan interval
	// (default 30s) so a re-armed PR is not re-claimed on the very next tick — that
	// would tight-loop a single PR and, with a one-row-per-tick claim, starve every
	// other refresh. Webhooks and on-demand refreshes are the fast path; this is
	// only the backstop for the brief window where GitHub is still computing.
	mergeabilityRetryDelay = 90 * time.Second
	// mergeabilityRetryWindow bounds the retry. GitHub settles mergeability within
	// seconds, so an open PR still unknown this long after its last provider update
	// is a GitHub anomaly, not a pending computation — stop re-arming and leave it
	// to the next webhook / on-demand refresh instead of looping.
	mergeabilityRetryWindow = 15 * time.Minute
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
	s.scheduleMergeabilityRetry(ctx, ref, transition.Current)
	return transition.Current, nil
}

// scheduleMergeabilityRetry re-arms the fallback refresh when an open PR's
// mergeability is still unknown. GitHub computes mergeability asynchronously, and
// even the REST fallback can return null on the first read right after a push, so
// one refresh is not always enough. Scheduling a near-term follow-up makes the PR
// resolve quickly and — because the scanner claims by due_at ascending — jump the
// queue. It self-terminates: once mergeability resolves, ApplyPullRequestSnapshot
// clears the fallback and this stops re-arming. Bounded by PR age so a
// permanently-unknown PR is left to normal silence polling instead of looping.
func (s *Service) scheduleMergeabilityRetry(ctx context.Context, ref domain.PullRequestRef, current domain.PullRequest) {
	if current.State != contract.PRStateOpen || current.Mergeability != contract.MergeUnknown {
		return
	}
	// Fail closed: without a provider timestamp we cannot bound the retry window, so
	// do NOT re-arm (a nil timestamp must never open an unbounded loop). Fall back to
	// the creation time when the update time is missing.
	last := current.UpdatedAtProvider
	if last == nil {
		last = current.CreatedAtProvider
	}
	if last == nil || time.Since(*last) > mergeabilityRetryWindow {
		return
	}
	dueAt := time.Now().UTC().Add(mergeabilityRetryDelay)
	if err := s.store.SchedulePullRequestRefresh(
		ctx, ref.OrgID, ref.ID, domain.PullRequestRefreshWebhookSilent, dueAt, "mergeability pending",
	); err != nil {
		s.logger.Warn("schedule mergeability retry",
			"org_id", ref.OrgID, "pull_request_id", ref.ID,
			"repository", ref.Repository, "number", ref.Number, "error", err)
		return
	}
	// Surface the retry so a PR that never resolves is observable rather than silent.
	s.logger.Debug("mergeability still unknown; scheduled retry",
		"org_id", ref.OrgID, "pull_request_id", ref.ID,
		"repository", ref.Repository, "number", ref.Number, "due_at", dueAt)
}
