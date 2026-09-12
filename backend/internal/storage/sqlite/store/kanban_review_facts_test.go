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

func TestListCurrentHeadReviewRunsForSessionKeepsLatestRunPerHarness(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Now().UTC().Truncate(time.Second)

	if err := s.WriteSCMObservation(ctx, domain.PullRequest{
		URL: "pr/1", SessionID: r.ID, Number: 1, HeadSHA: "head1", UpdatedAt: now, ObservedAt: now,
	}, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertReview(ctx, domain.Review{
		ID: "rev", SessionID: r.ID, ProjectID: "mer", Harness: "claude-code",
		PRURL: "pr/1", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	insert := func(id string, harness domain.ReviewerHarness, createdAt time.Time, verdict domain.ReviewVerdict) {
		t.Helper()
		if err := s.InsertReviewRun(ctx, domain.ReviewRun{
			ID: id, ReviewID: "rev", SessionID: r.ID, Harness: harness,
			PRURL: "pr/1", TargetSHA: "head1", Status: domain.ReviewRunComplete,
			Verdict: verdict, CreatedAt: createdAt,
		}); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	insert("old", "claude-code", now, domain.VerdictChangesRequested)
	insert("other", "codex", now.Add(time.Second), domain.VerdictApproved)
	insert("new", "claude-code", now.Add(2*time.Second), domain.VerdictApproved)

	runs, err := s.ListCurrentHeadReviewRunsForSession(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %+v, want latest run per harness", runs)
	}

	byHarness := map[domain.ReviewerHarness]domain.ReviewVerdict{}
	for _, run := range runs {
		byHarness[run.Harness] = run.Verdict
	}
	if byHarness["claude-code"] != domain.VerdictApproved || byHarness["codex"] != domain.VerdictApproved {
		t.Fatalf("runs = %+v, want latest approved run per harness", runs)
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

func TestListPRFactsForSessionSplitsExternalCommentsFromAOInjectedComments(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Now().UTC().Truncate(time.Second)

	if err := s.UpsertReview(ctx, domain.Review{
		ID: "rev-ao", SessionID: r.ID, ProjectID: "mer", Harness: "claude-code",
		PRURL: "pr/ao", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertReviewRun(ctx, domain.ReviewRun{
		ID: "run-ao", ReviewID: "rev-ao", SessionID: r.ID, Harness: "claude-code",
		PRURL: "pr/ao", TargetSHA: "head1", Status: domain.ReviewRunComplete,
		Verdict: domain.VerdictChangesRequested, GithubReviewID: "gh-ao", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	write := func(url string, comments []domain.PullRequestComment) {
		t.Helper()
		if err := s.WriteSCMObservation(ctx, domain.PullRequest{
			URL: url, SessionID: r.ID, Number: 1, Review: domain.ReviewRequired,
			HeadSHA: "head1", UpdatedAt: now, ObservedAt: now,
		}, nil, nil, []domain.PullRequestReviewThread{
			{ThreadID: "th-" + url, Path: "main.go", Line: 7, Resolved: false, UpdatedAt: now},
		}, comments, ports.ReviewWriteReplace); err != nil {
			t.Fatalf("write %s: %v", url, err)
		}
	}

	write("pr/external-on", []domain.PullRequestComment{{
		ThreadID: "th-pr/external-on", ID: "c-ext-on", ReviewID: "gh-human-on", Author: "maintainer", Body: "please fix",
		Resolved: false, IsBot: false, CreatedAt: now, AutoInjectReview: true,
	}})
	write("pr/external-off", []domain.PullRequestComment{{
		ThreadID: "th-pr/external-off", ID: "c-ext-off", ReviewID: "gh-human-off", Author: "maintainer", Body: "please fix",
		Resolved: false, IsBot: false, CreatedAt: now, AutoInjectReview: false,
	}})
	write("pr/ao", []domain.PullRequestComment{{
		ThreadID: "th-pr/ao", ID: "c-ao", ReviewID: "gh-ao", Author: "ao", Body: "handled automatically",
		Resolved: false, IsBot: false, CreatedAt: now, AutoInjectReview: true,
	}})

	facts, err := s.ListPRFactsForSession(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	byURL := map[string]domain.PRFacts{}
	for _, f := range facts {
		byURL[f.URL] = f
	}
	if got := byURL["pr/external-on"]; !got.ReviewComments || !got.ExternalComments {
		t.Fatalf("external unresolved comment should stay external when auto inject is on: %+v", got)
	}
	if got := byURL["pr/external-off"]; !got.ReviewComments || !got.ExternalComments {
		t.Fatalf("external unresolved comment should stay external when auto inject is off: %+v", got)
	}
	if got := byURL["pr/ao"]; !got.ReviewComments || got.ExternalComments {
		t.Fatalf("AO-authored comment should not surface as external input: %+v", got)
	}
}

func TestListPRFactsForSessionIgnoresHumanVerdictsFromAnOlderHead(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	r, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Now().UTC().Truncate(time.Second)

	write := func(url, headSHA string, reviews []domain.PullRequestReview) {
		t.Helper()
		if err := s.WriteSCMObservation(ctx, domain.PullRequest{
			URL: url, SessionID: r.ID, Number: 1, Review: domain.ReviewApproved,
			HeadSHA: headSHA, UpdatedAt: now, ObservedAt: now,
		}, nil, reviews, nil, nil, ports.ReviewWriteReplace); err != nil {
			t.Fatalf("write %s: %v", url, err)
		}
	}

	write("pr/stale", "head2", []domain.PullRequestReview{
		{ID: "gh-stale", Author: "maintainer", State: domain.ReviewApproved, SubmittedAt: now, TargetSHA: "head1"},
	})
	write("pr/current", "head2", []domain.PullRequestReview{
		{ID: "gh-current", Author: "maintainer", State: domain.ReviewApproved, SubmittedAt: now, TargetSHA: "head2"},
	})
	write("pr/legacy", "head2", []domain.PullRequestReview{
		{ID: "gh-legacy", Author: "maintainer", State: domain.ReviewApproved, SubmittedAt: now, TargetSHA: ""},
	})

	facts, err := s.ListPRFactsForSession(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	byURL := map[string]domain.PRFacts{}
	for _, f := range facts {
		byURL[f.URL] = f
	}
	if got := byURL["pr/stale"]; got.ExternalApproved {
		t.Fatalf("stale approval on an older head counted for current readiness: %+v", got)
	}
	if got := byURL["pr/current"]; !got.ExternalApproved {
		t.Fatalf("current-head approval was lost: %+v", got)
	}
	if got := byURL["pr/legacy"]; !got.ExternalApproved {
		t.Fatalf("legacy approval with empty target_sha should remain compatible: %+v", got)
	}
}

func TestListCurrentHeadReviewRunsForSessionsKeepsLatestRunPerHarness(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	first, _ := s.CreateSession(ctx, sampleRecord("mer"))
	second, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Now().UTC().Truncate(time.Second)

	seedReview := func(rec domain.SessionRecord) {
		t.Helper()
		prURL := "pr/" + string(rec.ID)
		reviewID := "rev-" + string(rec.ID)
		if err := s.WriteSCMObservation(ctx, domain.PullRequest{
			URL: prURL, SessionID: rec.ID, Number: 1, HeadSHA: "head1", UpdatedAt: now, ObservedAt: now,
		}, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
			t.Fatal(err)
		}
		if err := s.UpsertReview(ctx, domain.Review{
			ID: reviewID, SessionID: rec.ID, ProjectID: "mer", Harness: "claude-code",
			PRURL: prURL, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	seedReview(first)
	seedReview(second)

	insert := func(id string, sessionID domain.SessionID, reviewID string, harness domain.ReviewerHarness, createdAt time.Time, verdict domain.ReviewVerdict) {
		t.Helper()
		if err := s.InsertReviewRun(ctx, domain.ReviewRun{
			ID: id, ReviewID: reviewID, SessionID: sessionID, Harness: harness,
			PRURL: "pr/" + string(sessionID), TargetSHA: "head1", Status: domain.ReviewRunComplete,
			Verdict: verdict, CreatedAt: createdAt,
		}); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	insert("run-old", first.ID, "rev-"+string(first.ID), "claude-code", now, domain.VerdictChangesRequested)
	insert("run-new", first.ID, "rev-"+string(first.ID), "codex", now.Add(time.Second), domain.VerdictApproved)
	insert("run-other", first.ID, "rev-"+string(first.ID), "claude-code", now.Add(2*time.Second), domain.VerdictApproved)
	insert("run-second", second.ID, "rev-"+string(second.ID), "claude-code", now, domain.VerdictApproved)

	got, err := s.ListCurrentHeadReviewRunsForSessions(ctx, []domain.SessionID{first.ID, second.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got[first.ID]) != 2 || len(got[second.ID]) != 1 {
		t.Fatalf("batched runs = %+v", got)
	}

	byHarness := map[domain.ReviewerHarness]domain.ReviewVerdict{}
	for _, run := range got[first.ID] {
		if run.SessionID != first.ID {
			t.Fatalf("run session = %q, want %q", run.SessionID, first.ID)
		}
		byHarness[run.Harness] = run.Verdict
	}
	if byHarness["claude-code"] != domain.VerdictApproved || byHarness["codex"] != domain.VerdictApproved {
		t.Fatalf("first session runs = %+v, want latest per harness", got[first.ID])
	}
	if got[second.ID][0].Verdict != domain.VerdictApproved {
		t.Fatalf("second session verdict = %+v, want approved", got[second.ID][0])
	}
}

func TestListPRFactsForSessionsIgnoreOlderHeadHumanVerdicts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	first, _ := s.CreateSession(ctx, sampleRecord("mer"))
	second, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Now().UTC().Truncate(time.Second)

	if err := s.WriteSCMObservation(ctx, domain.PullRequest{
		URL: "pr/stale", SessionID: first.ID, Number: 1, Review: domain.ReviewApproved,
		HeadSHA: "head2", UpdatedAt: now, ObservedAt: now,
	}, nil, []domain.PullRequestReview{
		{ID: "gh-stale", Author: "maintainer", State: domain.ReviewApproved, SubmittedAt: now, TargetSHA: "head1"},
	}, nil, nil, ports.ReviewWriteReplace); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSCMObservation(ctx, domain.PullRequest{
		URL: "pr/current", SessionID: second.ID, Number: 2, Review: domain.ReviewApproved,
		HeadSHA: "head2", UpdatedAt: now, ObservedAt: now,
	}, nil, []domain.PullRequestReview{
		{ID: "gh-current", Author: "maintainer", State: domain.ReviewApproved, SubmittedAt: now, TargetSHA: "head2"},
	}, nil, nil, ports.ReviewWriteReplace); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListPRFactsForSessions(ctx, []domain.SessionID{first.ID, second.ID})
	if err != nil {
		t.Fatal(err)
	}
	if facts := got[first.ID]; len(facts) != 1 || facts[0].ExternalApproved {
		t.Fatalf("stale batch approval counted: %+v", facts)
	}
	if facts := got[second.ID]; len(facts) != 1 || !facts[0].ExternalApproved {
		t.Fatalf("current-head batch approval lost: %+v", facts)
	}
}

func TestListPRFactsForSessionsSplitExternalCommentsFromAOInjectedComments(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	first, _ := s.CreateSession(ctx, sampleRecord("mer"))
	second, _ := s.CreateSession(ctx, sampleRecord("mer"))
	third, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Now().UTC().Truncate(time.Second)

	if err := s.WriteSCMObservation(ctx, domain.PullRequest{
		URL: "pr/external-on", SessionID: first.ID, Number: 1, Review: domain.ReviewRequired,
		HeadSHA: "head1", UpdatedAt: now, ObservedAt: now,
	}, nil, nil, []domain.PullRequestReviewThread{
		{ThreadID: "th-external-on", Path: "main.go", Line: 5, Resolved: false, UpdatedAt: now},
	}, []domain.PullRequestComment{{
		ThreadID: "th-external-on", ID: "c-ext-on", ReviewID: "gh-human-on", Author: "maintainer", Body: "please fix",
		Resolved: false, IsBot: false, CreatedAt: now, AutoInjectReview: true,
	}}, ports.ReviewWriteReplace); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSCMObservation(ctx, domain.PullRequest{
		URL: "pr/external-off", SessionID: second.ID, Number: 2, Review: domain.ReviewRequired,
		HeadSHA: "head1", UpdatedAt: now, ObservedAt: now,
	}, nil, nil, []domain.PullRequestReviewThread{
		{ThreadID: "th-external-off", Path: "main.go", Line: 8, Resolved: false, UpdatedAt: now},
	}, []domain.PullRequestComment{{
		ThreadID: "th-external-off", ID: "c-ext-off", ReviewID: "gh-human-off", Author: "maintainer", Body: "please fix",
		Resolved: false, IsBot: false, CreatedAt: now, AutoInjectReview: false,
	}}, ports.ReviewWriteReplace); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertReview(ctx, domain.Review{
		ID: "rev-ao", SessionID: third.ID, ProjectID: "mer", Harness: "claude-code",
		PRURL: "pr/ao", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertReviewRun(ctx, domain.ReviewRun{
		ID: "run-ao", ReviewID: "rev-ao", SessionID: third.ID, Harness: "claude-code",
		PRURL: "pr/ao", TargetSHA: "head1", Status: domain.ReviewRunComplete,
		Verdict: domain.VerdictChangesRequested, GithubReviewID: "gh-ao", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSCMObservation(ctx, domain.PullRequest{
		URL: "pr/ao", SessionID: third.ID, Number: 3, Review: domain.ReviewRequired,
		HeadSHA: "head1", UpdatedAt: now, ObservedAt: now,
	}, nil, nil, []domain.PullRequestReviewThread{
		{ThreadID: "th-ao", Path: "main.go", Line: 11, Resolved: false, UpdatedAt: now},
	}, []domain.PullRequestComment{{
		ThreadID: "th-ao", ID: "c-ao", ReviewID: "gh-ao", Author: "ao", Body: "handled automatically",
		Resolved: false, IsBot: false, CreatedAt: now, AutoInjectReview: false,
	}}, ports.ReviewWriteReplace); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListPRFactsForSessions(ctx, []domain.SessionID{first.ID, second.ID, third.ID})
	if err != nil {
		t.Fatal(err)
	}
	if facts := got[first.ID]; len(facts) != 1 || !facts[0].ExternalComments {
		t.Fatalf("external batch comments lost when auto inject is on: %+v", facts)
	}
	if facts := got[second.ID]; len(facts) != 1 || !facts[0].ExternalComments {
		t.Fatalf("external batch comments lost when auto inject is off: %+v", facts)
	}
	if facts := got[third.ID]; len(facts) != 1 || facts[0].ExternalComments || !facts[0].ReviewComments {
		t.Fatalf("AO batch comments should stay non-external while review comments remain true: %+v", facts)
	}
}
