package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// The Kanban column may only be decided by a review pass against the PR's
// CURRENT head, so the SQL drops passes recorded for an earlier commit.
func TestListCurrentHeadReviewRunsForSessionDropsStaleSHAs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Now().UTC().Truncate(time.Second)

	if err := s.WriteSCMObservation(ctx, domain.PullRequest{
		URL: "pr/1", SessionID: r.ID, Number: 1, HeadSHA: "head2", UpdatedAt: now, ObservedAt: now,
	}, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertReview(ctx, domain.Review{ID: "rev", SessionID: r.ID, ProjectID: "mer", Harness: "claude-code", PRURL: "pr/1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	insert := func(id, sha string, status domain.ReviewRunStatus, verdict domain.ReviewVerdict) {
		t.Helper()
		if err := s.InsertReviewRun(ctx, domain.ReviewRun{
			ID: id, ReviewID: "rev", SessionID: r.ID, Harness: "claude-code",
			PRURL: "pr/1", TargetSHA: sha, Status: status, Verdict: verdict, CreatedAt: now,
		}); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	insert("stale", "head1", domain.ReviewRunComplete, domain.VerdictChangesRequested)
	insert("current", "head2", domain.ReviewRunRunning, domain.VerdictNone)

	runs, err := s.ListCurrentHeadReviewRunsForSession(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want only the current-head pass", runs)
	}
	if runs[0].PRURL != "pr/1" || runs[0].Status != domain.ReviewRunRunning {
		t.Fatalf("run = %+v, want the running head2 pass", runs[0])
	}
}

// The aggregate review_decision mixes AO's own provider reviews with everyone
// else's, so the PR facts must expose the human-only verdicts separately. AO's
// review is matched by the review id it recorded when it posted.
func TestListPRFactsForSessionSplitsExternalReviewVerdicts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Now().UTC().Truncate(time.Second)

	reviews := []domain.PullRequestReview{
		{ID: "gh-ao", Author: "ao", State: domain.ReviewApproved, SubmittedAt: now},
		{ID: "gh-bot", Author: "coderabbit", State: domain.ReviewApproved, IsBot: true, SubmittedAt: now},
	}
	write := func(url string, extra []domain.PullRequestReview) {
		t.Helper()
		if err := s.WriteSCMObservation(ctx, domain.PullRequest{
			URL: url, SessionID: r.ID, Number: 1, Review: domain.ReviewApproved, HeadSHA: "head1", UpdatedAt: now, ObservedAt: now,
		}, nil, append(append([]domain.PullRequestReview(nil), reviews...), extra...), nil, nil, ports.ReviewWriteReplace); err != nil {
			t.Fatalf("write %s: %v", url, err)
		}
	}
	write("pr/ao-only", nil)
	write("pr/human", []domain.PullRequestReview{{ID: "gh-human", Author: "maintainer", State: domain.ReviewChangesRequest, SubmittedAt: now}})

	// AO's own provider review, recorded by id on its review run.
	if err := s.UpsertReview(ctx, domain.Review{ID: "rev", SessionID: r.ID, ProjectID: "mer", Harness: "claude-code", PRURL: "pr/ao-only", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertReviewRun(ctx, domain.ReviewRun{
		ID: "run", ReviewID: "rev", SessionID: r.ID, Harness: "claude-code", PRURL: "pr/ao-only",
		TargetSHA: "head1", Status: domain.ReviewRunComplete, Verdict: domain.VerdictApproved,
		GithubReviewID: "gh-ao", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	facts, err := s.ListPRFactsForSession(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	byURL := map[string]domain.PRFacts{}
	for _, f := range facts {
		byURL[f.URL] = f
	}
	if got := byURL["pr/ao-only"]; got.ExternalApproved || got.ExternalChangesRequested {
		t.Fatalf("ao-authored and bot reviews leaked into external verdicts: %+v", got)
	}
	if got := byURL["pr/human"]; got.ExternalApproved || !got.ExternalChangesRequested {
		t.Fatalf("human changes request lost: %+v", got)
	}
}

// A reviewer who requests changes and later approves leaves both rows in
// pr_reviews. Only their latest verdict may count: the superseded changes
// request must not outlive it, or an auto-inject-review session stays pinned to
// "validating" forever while nobody is actually waiting on the worker.
func TestListPRFactsForSessionUsesEachReviewerLatestVerdict(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Now().UTC().Truncate(time.Second)

	write := func(url string, reviews []domain.PullRequestReview) {
		t.Helper()
		if err := s.WriteSCMObservation(ctx, domain.PullRequest{
			// review_required, not approved: the aggregate decision cannot mask a
			// stale changes request here (e.g. a second approval is still required).
			URL: url, SessionID: r.ID, Number: 1, Review: domain.ReviewRequired,
			HeadSHA: "head1", UpdatedAt: now, ObservedAt: now,
		}, nil, reviews, nil, nil, ports.ReviewWriteReplace); err != nil {
			t.Fatalf("write %s: %v", url, err)
		}
	}

	write("pr/superseded", []domain.PullRequestReview{
		{ID: "gh-1", Author: "maintainer", State: domain.ReviewChangesRequest, SubmittedAt: now},
		{ID: "gh-2", Author: "maintainer", State: domain.ReviewApproved, SubmittedAt: now.Add(time.Hour)},
	})
	write("pr/standing", []domain.PullRequestReview{
		{ID: "gh-3", Author: "maintainer", State: domain.ReviewApproved, SubmittedAt: now},
		{ID: "gh-4", Author: "maintainer", State: domain.ReviewChangesRequest, SubmittedAt: now.Add(time.Hour)},
	})
	// Two reviewers disagreeing: each still speaks for themselves.
	write("pr/split", []domain.PullRequestReview{
		{ID: "gh-5", Author: "maintainer", State: domain.ReviewApproved, SubmittedAt: now},
		{ID: "gh-6", Author: "second-reviewer", State: domain.ReviewChangesRequest, SubmittedAt: now},
	})

	facts, err := s.ListPRFactsForSession(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	byURL := map[string]domain.PRFacts{}
	for _, f := range facts {
		byURL[f.URL] = f
	}
	if got := byURL["pr/superseded"]; got.ExternalChangesRequested || !got.ExternalApproved {
		t.Fatalf("approval did not supersede the earlier changes request: %+v", got)
	}
	if got := byURL["pr/standing"]; !got.ExternalChangesRequested || got.ExternalApproved {
		t.Fatalf("changes request did not supersede the earlier approval: %+v", got)
	}
	if got := byURL["pr/split"]; !got.ExternalChangesRequested || !got.ExternalApproved {
		t.Fatalf("two reviewers should each keep their own verdict: %+v", got)
	}
}

// AO's own provider review must not dismiss a human verdict, even though it is
// posted under a GitHub account AO cannot distinguish from a person by name.
func TestListPRFactsForSessionKeepsHumanVerdictOverLaterAOReview(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Now().UTC().Truncate(time.Second)

	if err := s.WriteSCMObservation(ctx, domain.PullRequest{
		URL: "pr/1", SessionID: r.ID, Number: 1, Review: domain.ReviewRequired,
		HeadSHA: "head1", UpdatedAt: now, ObservedAt: now,
	}, nil, []domain.PullRequestReview{
		{ID: "gh-human", Author: "maintainer", State: domain.ReviewChangesRequest, SubmittedAt: now},
		{ID: "gh-ao", Author: "maintainer", State: domain.ReviewApproved, SubmittedAt: now.Add(time.Hour)},
	}, nil, nil, ports.ReviewWriteReplace); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertReview(ctx, domain.Review{ID: "rev", SessionID: r.ID, ProjectID: "mer", Harness: "claude-code", PRURL: "pr/1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertReviewRun(ctx, domain.ReviewRun{
		ID: "run", ReviewID: "rev", SessionID: r.ID, Harness: "claude-code", PRURL: "pr/1",
		TargetSHA: "head1", Status: domain.ReviewRunComplete, Verdict: domain.VerdictApproved,
		GithubReviewID: "gh-ao", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	facts, err := s.ListPRFactsForSession(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Fatalf("facts = %d, want 1", len(facts))
	}
	if facts[0].ExternalApproved || !facts[0].ExternalChangesRequested {
		t.Fatalf("AO's own approval dismissed a human changes request: %+v", facts[0])
	}
}
