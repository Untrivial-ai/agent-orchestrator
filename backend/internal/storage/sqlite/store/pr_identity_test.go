package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestWriteSCMObservationReconcilesTransferredPRAndPreservesRelatedRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "repo")
	session, err := s.CreateSession(ctx, sampleRecord("repo"))
	if err != nil {
		t.Fatal(err)
	}

	oldURL := "https://github.com/old-owner/repo/pull/7"
	currentURL := "https://github.com/new-owner/repo/pull/7"
	oldAt := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	currentAt := oldAt.Add(time.Hour)
	oldPR := domain.PullRequest{
		URL:          oldURL,
		SessionID:    session.ID,
		Number:       7,
		Provider:     "github",
		Host:         "github.com",
		Repo:         "old-owner/repo",
		HeadSHA:      "old-head",
		Title:        "stale title",
		Mergeability: domain.MergeConflicting,
		UpdatedAt:    oldAt,
	}
	if err := s.WriteSCMObservation(ctx, oldPR,
		[]domain.PullRequestCheck{{Name: "old-check", CommitHash: "old-head", Status: domain.PRCheckFailed, CreatedAt: oldAt}},
		[]domain.PullRequestReview{{ID: "old-review", Author: "alice", State: domain.ReviewChangesRequest, SubmittedAt: oldAt}},
		[]domain.PullRequestReviewThread{{ThreadID: "old-thread", Path: "old.go", UpdatedAt: oldAt}},
		[]domain.PullRequestComment{{ThreadID: "old-thread", ID: "old-comment", Body: "old comment", CreatedAt: oldAt}},
		ports.ReviewWriteReplace,
	); err != nil {
		t.Fatal(err)
	}

	canonicalPR := domain.PullRequest{
		URL:          currentURL,
		SessionID:    session.ID,
		Number:       7,
		Provider:     "github",
		Host:         "github.com",
		Repo:         "new-owner/repo",
		HeadSHA:      "current-head",
		Title:        "current title",
		Mergeability: domain.MergeBlocked,
		UpdatedAt:    currentAt,
	}
	if err := s.WriteSCMObservation(ctx, canonicalPR,
		[]domain.PullRequestCheck{{Name: "current-check", CommitHash: "current-head", Status: domain.PRCheckPassed, CreatedAt: currentAt}},
		[]domain.PullRequestReview{{ID: "current-review", Author: "bob", State: domain.ReviewApproved, SubmittedAt: currentAt}},
		[]domain.PullRequestReviewThread{{ThreadID: "current-thread", Path: "current.go", UpdatedAt: currentAt}},
		[]domain.PullRequestComment{{ThreadID: "current-thread", ID: "current-comment", Body: "current comment", CreatedAt: currentAt}},
		ports.ReviewWriteReplace,
	); err != nil {
		t.Fatal(err)
	}

	canonicalPR.ProviderID = "PR_stable_7"
	canonicalPR.URLAlias = oldURL
	canonicalPR.UpdatedAt = currentAt.Add(time.Minute)
	if err := s.WriteSCMObservation(ctx, canonicalPR, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
		t.Fatal(err)
	}

	prs, err := s.ListPRsBySession(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 {
		t.Fatalf("PR rows = %d, want one canonical row: %+v", len(prs), prs)
	}
	if prs[0].URL != currentURL || prs[0].ProviderID != "PR_stable_7" || prs[0].HeadSHA != "current-head" || prs[0].Mergeability != domain.MergeBlocked {
		t.Fatalf("canonical PR = %+v, want current provider observation", prs[0])
	}
	byAlias, ok, err := s.GetPR(ctx, oldURL)
	if err != nil || !ok || byAlias.URL != currentURL {
		t.Fatalf("GetPR(old URL) = %+v, ok=%v, err=%v; want canonical URL", byAlias, ok, err)
	}

	checks, err := s.ListChecks(ctx, currentURL)
	if err != nil || len(checks) != 2 {
		t.Fatalf("checks = %+v, err=%v; want both histories", checks, err)
	}
	reviews, err := s.ListPRReviews(ctx, currentURL)
	if err != nil || len(reviews) != 2 {
		t.Fatalf("reviews = %+v, err=%v; want both histories", reviews, err)
	}
	threads, err := s.ListPRReviewThreads(ctx, currentURL)
	if err != nil || len(threads) != 2 {
		t.Fatalf("threads = %+v, err=%v; want both histories", threads, err)
	}
	comments, err := s.ListPRComments(ctx, currentURL)
	if err != nil || len(comments) != 2 {
		t.Fatalf("comments = %+v, err=%v; want both histories", comments, err)
	}
}

func TestClaimPRChecksStableIdentityOwnerBeforeReconciliation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "repo")
	owner, err := s.CreateSession(ctx, sampleRecord("repo"))
	if err != nil {
		t.Fatal(err)
	}
	claimant, err := s.CreateSession(ctx, sampleRecord("repo"))
	if err != nil {
		t.Fatal(err)
	}
	oldURL := "https://github.com/old-owner/repo/pull/9"
	currentURL := "https://github.com/new-owner/repo/pull/9"
	if err := s.WriteSCMObservation(ctx, domain.PullRequest{
		URL: oldURL, SessionID: owner.ID, Number: 9, Provider: "github", Host: "github.com", ProviderID: "PR_stable_9", UpdatedAt: time.Now().UTC(),
	}, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
		t.Fatal(err)
	}

	outcome, err := s.ClaimPR(ctx, domain.PullRequest{
		URL: currentURL, URLAlias: oldURL, SessionID: claimant.ID, Number: 9, Provider: "github", Host: "github.com", ProviderID: "PR_stable_9", UpdatedAt: time.Now().UTC(),
	}, nil, nil, nil, nil, ports.ReviewWritePreserve, false)
	if !errors.Is(err, ports.ErrPRClaimedByActiveSession) {
		t.Fatalf("ClaimPR error = %v, want active-owner guard", err)
	}
	if outcome.PreviousOwner != owner.ID || outcome.OwnerTerminated {
		t.Fatalf("ClaimPR outcome = %+v, want active owner %s", outcome, owner.ID)
	}
	prs, err := s.ListPRsBySession(ctx, owner.ID)
	if err != nil || len(prs) != 1 || prs[0].URL != oldURL {
		t.Fatalf("owner PRs after rejected claim = %+v, err=%v", prs, err)
	}
}

func TestWriteSCMObservationReconcilesByStableIdentityWithoutURLAlias(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "repo")
	session, err := s.CreateSession(ctx, sampleRecord("repo"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	oldURL := "https://github.com/old-owner/example/pull/12"
	currentURL := "https://github.com/new-owner/example/pull/12"
	for _, pr := range []domain.PullRequest{
		{URL: oldURL, SessionID: session.ID, Number: 12, Provider: "github", Host: "github.com", ProviderID: "PR_stable_12", HeadSHA: "old", UpdatedAt: now},
		{URL: currentURL, SessionID: session.ID, Number: 12, Provider: "github", Host: "github.com", ProviderID: "PR_stable_12", HeadSHA: "current", UpdatedAt: now.Add(time.Minute)},
	} {
		if err := s.WriteSCMObservation(ctx, pr, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
			t.Fatal(err)
		}
	}
	prs, err := s.ListPRsBySession(ctx, session.ID)
	if err != nil || len(prs) != 1 || prs[0].URL != currentURL || prs[0].HeadSHA != "current" {
		t.Fatalf("PRs = %+v, err=%v; want current canonical observation", prs, err)
	}
}

func TestWriteSCMObservationKeepsUnrelatedSameNumberPRsSeparate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "repo")
	session, err := s.CreateSession(ctx, sampleRecord("repo"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, pr := range []domain.PullRequest{
		{URL: "https://github.com/acme/api/pull/7", SessionID: session.ID, Number: 7, Provider: "github", Host: "github.com", ProviderID: "PR_api_7", UpdatedAt: now},
		{URL: "https://github.com/acme/web/pull/7", SessionID: session.ID, Number: 7, Provider: "github", Host: "github.com", ProviderID: "PR_web_7", UpdatedAt: now.Add(time.Minute)},
		{URL: "https://github.com/legacy/api/pull/7", SessionID: session.ID, Number: 7, Provider: "github", Host: "github.com", UpdatedAt: now.Add(2 * time.Minute)},
	} {
		if err := s.WriteSCMObservation(ctx, pr, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
			t.Fatal(err)
		}
	}
	prs, err := s.ListPRsBySession(ctx, session.ID)
	if err != nil || len(prs) != 3 {
		t.Fatalf("PR rows = %+v, err=%v; want distinct provider identities and conservative legacy row", prs, err)
	}
}

func TestWriteSCMObservationDoesNotEraseKnownProviderIdentity(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "repo")
	session, err := s.CreateSession(ctx, sampleRecord("repo"))
	if err != nil {
		t.Fatal(err)
	}
	url := "https://github.com/acme/example/pull/8"
	withIdentity := domain.PullRequest{URL: url, SessionID: session.ID, Number: 8, Provider: "github", Host: "github.com", ProviderID: "PR_stable_8", UpdatedAt: time.Now().UTC()}
	if err := s.WriteSCMObservation(ctx, withIdentity, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
		t.Fatal(err)
	}
	withoutIdentity := withIdentity
	withoutIdentity.ProviderID = ""
	withoutIdentity.UpdatedAt = withoutIdentity.UpdatedAt.Add(time.Minute)
	if err := s.WriteSCMObservation(ctx, withoutIdentity, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetPR(ctx, url)
	if err != nil || !ok || got.ProviderID != "PR_stable_8" {
		t.Fatalf("GetPR = %+v, ok=%v, err=%v; want stable identity preserved", got, ok, err)
	}
}

func TestLegacyPRWriteResolvesStoredURLAlias(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "repo")
	session, err := s.CreateSession(ctx, sampleRecord("repo"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	oldURL := "https://github.com/old-owner/example/pull/30"
	currentURL := "https://github.com/new-owner/example/pull/30"
	if err := s.WriteSCMObservation(ctx, domain.PullRequest{
		URL: currentURL, URLAlias: oldURL, SessionID: session.ID, Number: 30, Provider: "github", Host: "github.com", ProviderID: "PR_stable_30", UpdatedAt: now,
	}, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
		t.Fatal(err)
	}
	if err := s.WritePR(ctx, domain.PullRequest{
		URL: oldURL, SessionID: session.ID, Number: 30, CI: domain.CIFailing, UpdatedAt: now.Add(time.Minute),
	}, nil, nil); err != nil {
		t.Fatal(err)
	}
	prs, err := s.ListPRsBySession(ctx, session.ID)
	if err != nil || len(prs) != 1 || prs[0].URL != currentURL || prs[0].CI != domain.CIFailing {
		t.Fatalf("PRs after legacy alias write = %+v, err=%v; want one updated canonical row", prs, err)
	}
}

func TestClaimPRResolvesStoredURLAliasBeforeOwnerGuard(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "repo")
	owner, err := s.CreateSession(ctx, sampleRecord("repo"))
	if err != nil {
		t.Fatal(err)
	}
	claimant, err := s.CreateSession(ctx, sampleRecord("repo"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	oldURL := "https://github.com/old-owner/example/pull/31"
	currentURL := "https://github.com/new-owner/example/pull/31"
	if err := s.WriteSCMObservation(ctx, domain.PullRequest{
		URL: currentURL, URLAlias: oldURL, SessionID: owner.ID, Number: 31, Provider: "github", Host: "github.com", ProviderID: "PR_stable_31", UpdatedAt: now,
	}, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
		t.Fatal(err)
	}
	outcome, err := s.ClaimPR(ctx, domain.PullRequest{
		URL: oldURL, SessionID: claimant.ID, Number: 31, UpdatedAt: now.Add(time.Minute),
	}, nil, nil, nil, nil, ports.ReviewWritePreserve, false)
	if !errors.Is(err, ports.ErrPRClaimedByActiveSession) || outcome.PreviousOwner != owner.ID {
		t.Fatalf("ClaimPR = outcome %+v, err=%v; want canonical active-owner guard", outcome, err)
	}
}

func TestPRReconciliationRepointsNotificationAndReviewHistory(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "repo")
	session, err := s.CreateSession(ctx, sampleRecord("repo"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	oldURL := "https://github.com/old-owner/example/pull/21"
	currentURL := "https://github.com/new-owner/example/pull/21"
	for _, pr := range []domain.PullRequest{
		{URL: oldURL, SessionID: session.ID, Number: 21, Provider: "github", Host: "github.com", UpdatedAt: now},
		{URL: currentURL, SessionID: session.ID, Number: 21, Provider: "github", Host: "github.com", UpdatedAt: now.Add(time.Minute)},
	} {
		if err := s.WriteSCMObservation(ctx, pr, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
			t.Fatal(err)
		}
	}
	for _, notification := range []domain.NotificationRecord{
		{ID: "old-notification", SessionID: session.ID, ProjectID: "repo", PRURL: oldURL, Type: domain.NotificationReadyToMerge, Title: "old", Status: domain.NotificationUnread, CreatedAt: now},
		{ID: "current-notification", SessionID: session.ID, ProjectID: "repo", PRURL: currentURL, Type: domain.NotificationReadyToMerge, Title: "current", Status: domain.NotificationUnread, CreatedAt: now.Add(time.Minute)},
	} {
		if _, created, err := s.CreateNotification(ctx, notification); err != nil || !created {
			t.Fatalf("CreateNotification(%s) = created=%v err=%v", notification.ID, created, err)
		}
	}
	review := domain.Review{ID: "review-21", SessionID: session.ID, ProjectID: "repo", Harness: domain.ReviewerCodex, PRURL: oldURL, CreatedAt: now, UpdatedAt: now}
	if err := s.UpsertReview(ctx, review); err != nil {
		t.Fatal(err)
	}
	for _, run := range []domain.ReviewRun{
		{ID: "old-run", ReviewID: review.ID, SessionID: session.ID, Harness: review.Harness, PRURL: oldURL, TargetSHA: "old-head", Status: domain.ReviewRunComplete, Verdict: domain.VerdictApproved, CreatedAt: now},
		{ID: "current-run", ReviewID: review.ID, SessionID: session.ID, Harness: review.Harness, PRURL: currentURL, TargetSHA: "current-head", Status: domain.ReviewRunComplete, Verdict: domain.VerdictApproved, CreatedAt: now.Add(time.Minute)},
	} {
		if err := s.InsertReviewRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.WriteSCMObservation(ctx, domain.PullRequest{
		URL: currentURL, URLAlias: oldURL, SessionID: session.ID, Number: 21, Provider: "github", Host: "github.com", ProviderID: "PR_stable_21", UpdatedAt: now.Add(2 * time.Minute),
	}, nil, nil, nil, nil, ports.ReviewWritePreserve); err != nil {
		t.Fatal(err)
	}

	notifications, err := s.ListNotifications(ctx, domain.NotificationListAll, time.Time{}, "", 10)
	if err != nil || len(notifications) != 2 {
		t.Fatalf("notifications = %+v, err=%v", notifications, err)
	}
	for _, notification := range notifications {
		if notification.PRURL != currentURL {
			t.Fatalf("notification %s PR URL = %q, want %q", notification.ID, notification.PRURL, currentURL)
		}
	}
	storedReview, ok, err := s.GetReviewByID(ctx, review.ID)
	if err != nil || !ok || storedReview.PRURL != currentURL {
		t.Fatalf("review = %+v, ok=%v, err=%v", storedReview, ok, err)
	}
	runs, err := s.ListReviewRunsBySession(ctx, session.ID)
	if err != nil || len(runs) != 2 {
		t.Fatalf("review runs = %+v, err=%v", runs, err)
	}
	for _, run := range runs {
		if run.PRURL != currentURL {
			t.Fatalf("review run %s PR URL = %q, want %q", run.ID, run.PRURL, currentURL)
		}
	}
}
