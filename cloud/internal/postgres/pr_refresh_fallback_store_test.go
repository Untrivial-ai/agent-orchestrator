package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSchedulePullRequestRefreshDeduplicatesAtEarliestDueTime(t *testing.T) {
	store, _, fixture := openNotificationTestStore(t)
	pr := createRefreshTestPullRequest(t, store, fixture, 51)
	now := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := store.SchedulePullRequestRefresh(context.Background(), fixture.orgID, pr.ID, domain.PullRequestRefreshWebhookFailed, now.Add(time.Minute), "first"); err != nil {
		t.Fatal(err)
	}
	if err := store.SchedulePullRequestRefresh(context.Background(), fixture.orgID, pr.ID, domain.PullRequestRefreshWebhookFailed, now.Add(2*time.Minute), "second"); err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimPullRequestRefresh(context.Background(), "worker-one", now.Add(90*time.Second), now.Add(2*time.Minute), 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if job.Ref.ID != pr.ID || job.Reason != domain.PullRequestRefreshWebhookFailed || job.AttemptCount != 1 || job.LeaseOwner != "worker-one" {
		t.Fatalf("job = %+v", job)
	}
}

func TestCreatePullRequestRecordInitializesRefreshState(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	pr := createRefreshTestPullRequest(t, store, fixture, 52)
	dueAt, reason, attempts, leaseOwner, leaseUntil, lastError := readRefreshState(t, admin, fixture.orgID, pr.ID)
	if dueAt != nil || reason != "" || attempts != 0 || leaseOwner != "" || leaseUntil != nil || lastError != "" {
		t.Fatalf("initial fallback state = due:%v reason:%q attempts:%d owner:%q lease:%v error:%q", dueAt, reason, attempts, leaseOwner, leaseUntil, lastError)
	}
}

func TestClaimPullRequestRefreshHonorsSilenceGrace(t *testing.T) {
	tests := []struct {
		name      string
		observed  time.Duration
		wantClaim bool
	}{
		{name: "inside grace", observed: -119 * time.Second},
		{name: "outside grace", observed: -121 * time.Second, wantClaim: true},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, admin, fixture := openNotificationTestStore(t)
			pr := createRefreshTestPullRequest(t, store, fixture, 60+index)
			now := time.Date(2002, 1, 1, 0, 0, 0, 0, time.UTC)
			setPullRequestRefreshFacts(t, admin, fixture.orgID, pr.ID, contract.PRStateOpen, now.Add(tt.observed))

			job, err := store.ClaimPullRequestRefresh(context.Background(), "silence-worker", now, now.Add(30*time.Second), 2*time.Minute)
			if !tt.wantClaim {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("error = %v, want ErrNotFound", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if job.Ref.ID != pr.ID || job.Reason != domain.PullRequestRefreshWebhookSilent {
				t.Fatalf("job = %+v", job)
			}
		})
	}
}

func TestClaimPullRequestRefreshFencesAndReclaimsLeases(t *testing.T) {
	store, _, fixture := openNotificationTestStore(t)
	pr := createRefreshTestPullRequest(t, store, fixture, 71)
	now := time.Date(2003, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.SchedulePullRequestRefresh(context.Background(), fixture.orgID, pr.ID, domain.PullRequestRefreshWebhookFailed, now, "failed"); err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimPullRequestRefresh(context.Background(), "worker-one", now, now.Add(30*time.Second), 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if first.Ref.ID != pr.ID {
		t.Fatalf("first job = %+v", first)
	}
	if _, err := store.ClaimPullRequestRefresh(context.Background(), "worker-two", now.Add(20*time.Second), now.Add(50*time.Second), 2*time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("active lease error = %v, want ErrNotFound", err)
	}
	reclaimed, err := store.ClaimPullRequestRefresh(context.Background(), "worker-two", now.Add(31*time.Second), now.Add(time.Minute), 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.Ref.ID != pr.ID || reclaimed.LeaseOwner != "worker-two" || reclaimed.AttemptCount != 2 {
		t.Fatalf("reclaimed job = %+v", reclaimed)
	}
}

func TestClaimPullRequestRefreshConcurrentReplicasClaimOnce(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	pr := createRefreshTestPullRequest(t, store, fixture, 72)
	now := time.Date(2003, 2, 1, 0, 0, 0, 0, time.UTC)
	if err := store.SchedulePullRequestRefresh(context.Background(), fixture.orgID, pr.ID, domain.PullRequestRefreshWebhookFailed, now, "failed"); err != nil {
		t.Fatal(err)
	}

	lockTx, err := admin.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback(context.Background())
	if _, err := lockTx.Exec(context.Background(), `SELECT set_config('ao.org_id', $1, true)`, fixture.orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := lockTx.Exec(context.Background(), `SELECT id FROM ao_pull_requests WHERE org_id=$1 AND id=$2 FOR UPDATE`, fixture.orgID, pr.ID); err != nil {
		t.Fatal(err)
	}

	type result struct {
		job domain.PullRequestRefreshJob
		err error
	}
	started := make(chan struct{}, 2)
	results := make(chan result, 2)
	for _, owner := range []string{"replica-one", "replica-two"} {
		go func(owner string) {
			started <- struct{}{}
			job, err := store.ClaimPullRequestRefresh(context.Background(), owner, now, now.Add(30*time.Second), 2*time.Minute)
			results <- result{job: job, err: err}
		}(owner)
	}
	<-started
	<-started
	time.Sleep(100 * time.Millisecond)
	if err := lockTx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}

	successes, notFound := 0, 0
	owners := map[string]bool{}
	for range 2 {
		got := <-results
		switch {
		case got.err == nil:
			successes++
			owners[got.job.LeaseOwner] = true
		case errors.Is(got.err, ErrNotFound):
			notFound++
		default:
			t.Fatalf("claim error = %v", got.err)
		}
	}
	if successes != 1 || notFound != 1 || len(owners) != 1 {
		t.Fatalf("successes = %d, not found = %d, owners = %v; want one leased replica", successes, notFound, owners)
	}
}

func TestApplyPullRequestSnapshotWebhookSuccessClearsRacingFallbackLease(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	pr := createRefreshTestPullRequest(t, store, fixture, 81)
	now := time.Date(2004, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.SchedulePullRequestRefresh(context.Background(), fixture.orgID, pr.ID, domain.PullRequestRefreshWebhookFailed, now, "failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimPullRequestRefresh(context.Background(), "fallback-worker", now, now.Add(30*time.Second), 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	snapshot := refreshTestSnapshot(pr, "new-head")
	transition, err := store.ApplyPullRequestSnapshot(context.Background(), fixture.orgID, pr.ID, snapshot, domain.PullRequestRefreshContext{Source: domain.PullRequestRefreshWebhook})
	if err != nil {
		t.Fatal(err)
	}
	if transition.Current.HeadSHA != "new-head" {
		t.Fatalf("current head = %q", transition.Current.HeadSHA)
	}
	dueAt, reason, attempts, leaseOwner, leaseUntil, lastError := readRefreshState(t, admin, fixture.orgID, pr.ID)
	if dueAt != nil || reason != "" || attempts != 0 || leaseOwner != "" || leaseUntil != nil || lastError != "" {
		t.Fatalf("fallback state = due:%v reason:%q attempts:%d owner:%q lease:%v error:%q", dueAt, reason, attempts, leaseOwner, leaseUntil, lastError)
	}
}

func TestApplyPullRequestSnapshotFallbackRequiresMatchingLease(t *testing.T) {
	store, _, fixture := openNotificationTestStore(t)
	pr := createRefreshTestPullRequest(t, store, fixture, 82)
	now := time.Date(2005, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.SchedulePullRequestRefresh(context.Background(), fixture.orgID, pr.ID, domain.PullRequestRefreshWebhookFailed, now, "failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimPullRequestRefresh(context.Background(), "lease-owner", now, now.Add(30*time.Second), 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	_, err := store.ApplyPullRequestSnapshot(context.Background(), fixture.orgID, pr.ID, refreshTestSnapshot(pr, "must-rollback"), domain.PullRequestRefreshContext{Source: domain.PullRequestRefreshFallback, LeaseOwner: "wrong-owner"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
	got, err := store.PullRequestSnapshot(context.Background(), fixture.orgID, pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Observation.HeadSHA != pr.HeadSHA {
		t.Fatalf("head = %q, want rollback to %q", got.Observation.HeadSHA, pr.HeadSHA)
	}
}

func TestWebhookFailureFencesOlderFallbackLease(t *testing.T) {
	store, _, fixture := openNotificationTestStore(t)
	pr := createRefreshTestPullRequest(t, store, fixture, 85)
	now := time.Date(2005, 4, 1, 0, 0, 0, 0, time.UTC)
	if err := store.SchedulePullRequestRefresh(context.Background(), fixture.orgID, pr.ID, domain.PullRequestRefreshWebhookFailed, now, "first failure"); err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimPullRequestRefresh(context.Background(), "old-lease", now, now.Add(30*time.Second), 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SchedulePullRequestRefresh(context.Background(), fixture.orgID, pr.ID, domain.PullRequestRefreshWebhookFailed, now.Add(time.Second), "newer webhook failure"); err != nil {
		t.Fatal(err)
	}
	_, err = store.ApplyPullRequestSnapshot(context.Background(), fixture.orgID, pr.ID, refreshTestSnapshot(pr, "stale-fallback-head"), domain.PullRequestRefreshContext{Source: domain.PullRequestRefreshFallback, LeaseOwner: job.LeaseOwner})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
	got, err := store.PullRequestSnapshot(context.Background(), fixture.orgID, pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Observation.HeadSHA != pr.HeadSHA {
		t.Fatalf("head = %q, want rollback to %q", got.Observation.HeadSHA, pr.HeadSHA)
	}
}

func TestApplyPullRequestSnapshotFallbackClearsMatchingLease(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	pr := createRefreshTestPullRequest(t, store, fixture, 83)
	now := time.Date(2005, 2, 1, 0, 0, 0, 0, time.UTC)
	if err := store.SchedulePullRequestRefresh(context.Background(), fixture.orgID, pr.ID, domain.PullRequestRefreshWebhookFailed, now, "failed"); err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimPullRequestRefresh(context.Background(), "lease-owner", now, now.Add(30*time.Second), 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyPullRequestSnapshot(context.Background(), fixture.orgID, pr.ID, refreshTestSnapshot(pr, "fallback-head"), domain.PullRequestRefreshContext{Source: domain.PullRequestRefreshFallback, LeaseOwner: job.LeaseOwner}); err != nil {
		t.Fatal(err)
	}
	dueAt, reason, attempts, leaseOwner, leaseUntil, lastError := readRefreshState(t, admin, fixture.orgID, pr.ID)
	if dueAt != nil || reason != "" || attempts != 0 || leaseOwner != "" || leaseUntil != nil || lastError != "" {
		t.Fatalf("fallback state = due:%v reason:%q attempts:%d owner:%q lease:%v error:%q", dueAt, reason, attempts, leaseOwner, leaseUntil, lastError)
	}
}

func TestRetryPullRequestRefreshReschedulesMatchingLease(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	pr := createRefreshTestPullRequest(t, store, fixture, 84)
	now := time.Date(2005, 3, 1, 0, 0, 0, 0, time.UTC)
	if err := store.SchedulePullRequestRefresh(context.Background(), fixture.orgID, pr.ID, domain.PullRequestRefreshWebhookFailed, now, "failed"); err != nil {
		t.Fatal(err)
	}
	job, err := store.ClaimPullRequestRefresh(context.Background(), "lease-owner", now, now.Add(30*time.Second), 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	retryAt := now.Add(time.Minute)
	message := strings.Repeat("x", 1200)
	if err := store.RetryPullRequestRefresh(context.Background(), job, retryAt, message); err != nil {
		t.Fatal(err)
	}
	dueAt, reason, attempts, leaseOwner, leaseUntil, lastError := readRefreshState(t, admin, fixture.orgID, pr.ID)
	if dueAt == nil || !dueAt.Equal(retryAt) || reason != string(domain.PullRequestRefreshWebhookFailed) || attempts != 1 || leaseOwner != "" || leaseUntil != nil || len(lastError) != 1000 {
		t.Fatalf("fallback state = due:%v reason:%q attempts:%d owner:%q lease:%v error length:%d", dueAt, reason, attempts, leaseOwner, leaseUntil, len(lastError))
	}
	if _, err := store.ClaimPullRequestRefresh(context.Background(), "second-worker", now.Add(30*time.Second), now.Add(time.Minute), 2*time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("early claim error = %v, want ErrNotFound", err)
	}
	retried, err := store.ClaimPullRequestRefresh(context.Background(), "second-worker", retryAt, retryAt.Add(30*time.Second), 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Ref.ID != pr.ID || retried.AttemptCount != 2 {
		t.Fatalf("retried job = %+v", retried)
	}
}

func TestClaimPullRequestRefreshExcludesClosedAndMergedPullRequests(t *testing.T) {
	for index, state := range []contract.PRState{contract.PRStateClosed, contract.PRStateMerged} {
		t.Run(string(state), func(t *testing.T) {
			store, admin, fixture := openNotificationTestStore(t)
			pr := createRefreshTestPullRequest(t, store, fixture, 90+index)
			now := time.Date(2006, 1, 1, 0, 0, 0, 0, time.UTC)
			setPullRequestRefreshFacts(t, admin, fixture.orgID, pr.ID, state, now.Add(-time.Hour))
			if err := store.SchedulePullRequestRefresh(context.Background(), fixture.orgID, pr.ID, domain.PullRequestRefreshWebhookFailed, now, "failed"); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ClaimPullRequestRefresh(context.Background(), "worker", now, now.Add(30*time.Second), 2*time.Minute); !errors.Is(err, ErrNotFound) {
				t.Fatalf("error = %v, want ErrNotFound", err)
			}
		})
	}
}

func createRefreshTestPullRequest(t *testing.T, store *Store, fixture notificationFixture, number int) domain.PullRequest {
	t.Helper()
	pr, err := store.CreatePullRequestRecord(context.Background(), fixture.orgID, fixture.sessionID,
		"github", "octo/widgets", "owner", number, "https://github.test/octo/widgets/pull/refresh",
		"feature", "main", "old-head", "Title", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return pr
}

func refreshTestSnapshot(pr domain.PullRequest, headSHA string) domain.PullRequestSnapshot {
	return domain.PullRequestSnapshot{
		URL: pr.URL, Title: pr.Title, Author: pr.Author, SourceBranch: pr.SourceBranch, TargetBranch: pr.TargetBranch,
		Observation: domain.PullRequestObservation{
			State: contract.PRStateOpen, HeadSHA: headSHA, CIState: contract.CIPassing,
			ReviewState: contract.ReviewNone, Mergeability: contract.MergeMergeable,
		},
	}
}

func setPullRequestRefreshFacts(t *testing.T, admin *pgxpool.Pool, orgID, pullRequestID string, state contract.PRState, observedAt time.Time) {
	t.Helper()
	tx, err := admin.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(context.Background(), `SELECT set_config('ao.org_id', $1, true)`, orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `UPDATE ao_pull_requests SET state=$3, observed_at=$4 WHERE org_id=$1 AND id=$2`, orgID, pullRequestID, string(state), observedAt); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func readRefreshState(t *testing.T, admin *pgxpool.Pool, orgID, pullRequestID string) (*time.Time, string, int, string, *time.Time, string) {
	t.Helper()
	tx, err := admin.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(context.Background(), `SELECT set_config('ao.org_id', $1, true)`, orgID); err != nil {
		t.Fatal(err)
	}
	var dueAt, leaseUntil *time.Time
	var reason, leaseOwner, lastError string
	var attempts int
	if err := tx.QueryRow(context.Background(), `SELECT due_at,reason,attempt_count,lease_owner,lease_until,last_error FROM ao_pr_refresh_fallbacks WHERE org_id=$1 AND pull_request_id=$2`, orgID, pullRequestID).Scan(&dueAt, &reason, &attempts, &leaseOwner, &leaseUntil, &lastError); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			t.Fatal("fallback state missing")
		}
		t.Fatal(err)
	}
	return dueAt, reason, attempts, leaseOwner, leaseUntil, lastError
}
