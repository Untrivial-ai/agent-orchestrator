// Package prstatus recovers pull request refreshes when GitHub webhooks fail
// or remain silent beyond the configured grace period.
package prstatus

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

type Store interface {
	ClaimPullRequestRefresh(context.Context, string, time.Time, time.Time, time.Duration) (domain.PullRequestRefreshJob, error)
	RetryPullRequestRefresh(context.Context, domain.PullRequestRefreshJob, time.Time, string) error
}

type GitHub interface {
	RefreshPullRequestStatus(context.Context, domain.PullRequestRef, domain.PullRequestRefreshContext) (domain.PullRequest, error)
}

type Options struct {
	Interval      time.Duration
	WorkerID      string
	SilenceGrace  time.Duration
	LeaseDuration time.Duration
	Now           func() time.Time
	Logger        *slog.Logger
}

const (
	DefaultInterval      = 30 * time.Second
	DefaultSilenceGrace  = 2 * time.Minute
	DefaultLeaseDuration = 30 * time.Second
	maxRetryBackoff      = 5 * time.Minute
)

type Scanner struct {
	store   Store
	github  GitHub
	options Options
	log     *slog.Logger
}

func New(store Store, github GitHub, options Options) *Scanner {
	if options.Interval <= 0 {
		options.Interval = DefaultInterval
	}
	if options.WorkerID == "" {
		options.WorkerID = "pull-request-fallback-scanner"
	}
	if options.SilenceGrace <= 0 {
		options.SilenceGrace = DefaultSilenceGrace
	}
	if options.LeaseDuration <= 0 {
		options.LeaseDuration = DefaultLeaseDuration
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	return &Scanner{store: store, github: github, options: options, log: options.Logger}
}

func (s *Scanner) Run(ctx context.Context) error {
	if ctx.Err() != nil {
		return nil
	}
	if err := s.ScanOnce(ctx); err != nil && ctx.Err() == nil {
		s.log.Error("pull request fallback scan failed", "error", err)
	}
	ticker := time.NewTicker(s.options.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.ScanOnce(ctx); err != nil && ctx.Err() == nil {
				s.log.Error("pull request fallback scan failed", "error", err)
			}
		}
	}
}

func (s *Scanner) ScanOnce(ctx context.Context) error {
	if ctx.Err() != nil {
		return nil
	}
	now := s.options.Now().UTC()
	job, err := s.store.ClaimPullRequestRefresh(
		ctx,
		s.options.WorkerID,
		now,
		now.Add(s.options.LeaseDuration),
		s.options.SilenceGrace,
	)
	if errors.Is(err, postgres.ErrNotFound) {
		return nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}

	refresh := domain.PullRequestRefreshContext{
		Source:     domain.PullRequestRefreshFallback,
		LeaseOwner: job.LeaseOwner,
	}
	if _, err := s.github.RefreshPullRequestStatus(ctx, job.Ref, refresh); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, postgres.ErrConflict) {
			return err
		}
		retryAt := now.Add(retryBackoff(job.AttemptCount))
		if retryErr := s.store.RetryPullRequestRefresh(ctx, job, retryAt, err.Error()); retryErr != nil {
			return errors.Join(err, retryErr)
		}
		s.log.Warn("pull request fallback refresh retry scheduled",
			"org_id", job.Ref.OrgID,
			"pull_request_id", job.Ref.ID,
			"repository", job.Ref.Repository,
			"number", job.Ref.Number,
			"reason", job.Reason,
			"attempt_count", job.AttemptCount,
			"error", err,
		)
		return nil
	}
	s.log.Info("pull request fallback refresh succeeded",
		"org_id", job.Ref.OrgID,
		"pull_request_id", job.Ref.ID,
		"repository", job.Ref.Repository,
		"number", job.Ref.Number,
		"reason", job.Reason,
		"attempt_count", job.AttemptCount,
	)
	return nil
}

func retryBackoff(attempt int) time.Duration {
	backoff := time.Second
	for current := 1; current < attempt && backoff < maxRetryBackoff; current++ {
		backoff *= 2
		if backoff >= maxRetryBackoff {
			return maxRetryBackoff
		}
	}
	return backoff
}
