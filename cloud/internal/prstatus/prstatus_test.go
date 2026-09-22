package prstatus

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

type scannerRetry struct {
	job     domain.PullRequestRefreshJob
	dueAt   time.Time
	message string
}

type scannerStore struct {
	jobs     []domain.PullRequestRefreshJob
	claimErr error
	claims   int
	retries  []scannerRetry
	retryErr error
}

func (s *scannerStore) ClaimPullRequestRefresh(context.Context, string, time.Time, time.Time, time.Duration) (domain.PullRequestRefreshJob, error) {
	s.claims++
	if s.claimErr != nil {
		return domain.PullRequestRefreshJob{}, s.claimErr
	}
	if len(s.jobs) == 0 {
		return domain.PullRequestRefreshJob{}, postgres.ErrNotFound
	}
	job := s.jobs[0]
	s.jobs = s.jobs[1:]
	return job, nil
}

func (s *scannerStore) RetryPullRequestRefresh(_ context.Context, job domain.PullRequestRefreshJob, dueAt time.Time, message string) error {
	s.retries = append(s.retries, scannerRetry{job: job, dueAt: dueAt, message: message})
	return s.retryErr
}

type scannerRefreshCall struct {
	ref     domain.PullRequestRef
	refresh domain.PullRequestRefreshContext
}

type scannerGitHub struct {
	calls []scannerRefreshCall
	err   error
}

func (g *scannerGitHub) RefreshPullRequestStatus(_ context.Context, ref domain.PullRequestRef, refresh domain.PullRequestRefreshContext) (domain.PullRequest, error) {
	g.calls = append(g.calls, scannerRefreshCall{ref: ref, refresh: refresh})
	return domain.PullRequest{}, g.err
}

func testScannerOptions(now time.Time) Options {
	return Options{
		WorkerID:      "scanner-1",
		SilenceGrace:  2 * time.Minute,
		LeaseDuration: 30 * time.Second,
		Now:           func() time.Time { return now },
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestScanOnceDoesNothingWithoutDueRecovery(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	store := &scannerStore{claimErr: postgres.ErrNotFound}
	github := &scannerGitHub{}
	if err := New(store, github, testScannerOptions(now)).ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(github.calls) != 0 || len(store.retries) != 0 {
		t.Fatalf("GitHub calls = %+v, retries = %+v", github.calls, store.retries)
	}
}

func TestScanOnceRefreshesOneClaimedJobWithLeaseContext(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	job := domain.PullRequestRefreshJob{
		Ref:    domain.PullRequestRef{ID: "pr-1", OrgID: "org-1", Provider: "github", Repository: "acme/widgets", Number: 7},
		Reason: domain.PullRequestRefreshWebhookFailed, AttemptCount: 1, LeaseOwner: "scanner-1",
	}
	store := &scannerStore{jobs: []domain.PullRequestRefreshJob{job}}
	github := &scannerGitHub{}
	if err := New(store, github, testScannerOptions(now)).ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(github.calls) != 1 || github.calls[0].ref != job.Ref {
		t.Fatalf("GitHub calls = %+v", github.calls)
	}
	wantRefresh := domain.PullRequestRefreshContext{Source: domain.PullRequestRefreshFallback, LeaseOwner: job.LeaseOwner}
	if github.calls[0].refresh != wantRefresh {
		t.Fatalf("refresh context = %+v, want %+v", github.calls[0].refresh, wantRefresh)
	}
	if len(store.retries) != 0 {
		t.Fatalf("retries = %+v, want none", store.retries)
	}
}

func TestScanOnceReschedulesFailureWithBoundedExponentialBackoff(t *testing.T) {
	tests := []struct {
		name    string
		attempt int
		want    time.Duration
	}{
		{name: "first retry", attempt: 1, want: time.Second},
		{name: "capped retry", attempt: 20, want: 5 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
			job := domain.PullRequestRefreshJob{Ref: domain.PullRequestRef{ID: "pr-1", OrgID: "org-1"}, AttemptCount: tt.attempt, LeaseOwner: "scanner-1"}
			store := &scannerStore{jobs: []domain.PullRequestRefreshJob{job}}
			refreshErr := errors.New("GitHub unavailable")
			github := &scannerGitHub{err: refreshErr}
			if err := New(store, github, testScannerOptions(now)).ScanOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(store.retries) != 1 || !store.retries[0].dueAt.Equal(now.Add(tt.want)) || store.retries[0].message != refreshErr.Error() || store.retries[0].job.Ref.ID != job.Ref.ID {
				t.Fatalf("retry = %+v, want due %s", store.retries, now.Add(tt.want))
			}
		})
	}
}

func TestScanOnceSurfacesLeaseFencingConflict(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	job := domain.PullRequestRefreshJob{Ref: domain.PullRequestRef{ID: "pr-1", OrgID: "org-1"}, AttemptCount: 1, LeaseOwner: "scanner-1"}
	store := &scannerStore{jobs: []domain.PullRequestRefreshJob{job}}
	github := &scannerGitHub{err: postgres.ErrConflict}
	err := New(store, github, testScannerOptions(now)).ScanOnce(context.Background())
	if !errors.Is(err, postgres.ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
	if len(store.retries) != 0 {
		t.Fatalf("retries = %+v, stale owner must not mutate fallback", store.retries)
	}
}

func TestScannerRunReturnsCleanlyWhenAlreadyCanceled(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	store := &scannerStore{}
	github := &scannerGitHub{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := New(store, github, testScannerOptions(now)).Run(ctx); err != nil {
		t.Fatal(err)
	}
	if store.claims != 0 || len(github.calls) != 0 {
		t.Fatalf("claims = %d, GitHub calls = %+v", store.claims, github.calls)
	}
}

func TestRepeatedScansDoNotRefreshHealthyPullRequests(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	store := &scannerStore{claimErr: postgres.ErrNotFound}
	github := &scannerGitHub{}
	scanner := New(store, github, testScannerOptions(now))
	if err := scanner.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := scanner.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.claims != 2 || len(github.calls) != 0 {
		t.Fatalf("claims = %d, GitHub calls = %+v", store.claims, github.calls)
	}
}
