package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

func TestRetryCIFeedbackReturnsItemToReadyQueue(t *testing.T) {
	store, _, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	pullRequest, err := store.CreatePullRequestRecord(
		ctx, fixture.orgID, fixture.sessionID, "github", "octo/widgets", "octocat", 20,
		"https://github.test/octo/widgets/pull/20", "feature", "main", "sha-20",
		"Retry feedback", 1, 0, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := domain.PullRequestSnapshot{
		URL: pullRequest.URL, Title: pullRequest.Title, Author: pullRequest.Author,
		SourceBranch: pullRequest.SourceBranch, TargetBranch: pullRequest.TargetBranch,
		Observation: domain.PullRequestObservation{
			State: contract.PRStateOpen, HeadSHA: pullRequest.HeadSHA,
			CIState: contract.CIPassing, ReviewState: contract.ReviewNone,
			Mergeability: contract.MergeMergeable,
		},
		Threads: []domain.PullRequestReviewThread{{ProviderID: "thread-1", Path: "main.go", Line: 7}},
		Comments: []domain.PullRequestReviewComment{{
			ProviderID: "comment-1", ThreadProviderID: "thread-1", Author: "reviewer",
			Body: "please rename this", Path: "main.go", Line: 7,
		}},
	}
	if _, err := store.ApplyPullRequestSnapshot(ctx, fixture.orgID, pullRequest.ID, snapshot); err != nil {
		t.Fatal(err)
	}

	claimed, ok, err := store.ClaimCIFeedback(ctx, "dispatcher-1", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim feedback: ok=%v err=%v", ok, err)
	}
	if claimed.WorkerID != fixture.workerID || claimed.WorkerEpoch != fixture.epoch {
		t.Fatalf("claimed worker = %q epoch %d, want %q epoch %d", claimed.WorkerID, claimed.WorkerEpoch, fixture.workerID, fixture.epoch)
	}
	if err := store.RetryCIFeedback(ctx, claimed.ID, "dispatcher-1", "worker disconnected", time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("retry feedback: %v", err)
	}
	retried, ok, err := store.ClaimCIFeedback(ctx, "dispatcher-2", time.Minute)
	if err != nil || !ok {
		t.Fatalf("reclaim feedback: ok=%v err=%v", ok, err)
	}
	if retried.ID != claimed.ID || retried.AttemptCount != claimed.AttemptCount+1 {
		t.Fatalf("retried feedback = %+v, want id %q attempt %d", retried, claimed.ID, claimed.AttemptCount+1)
	}
}

func TestCIFailureNotificationPersistsAndResolvesUnderRecipientRLS(t *testing.T) {
	store, _, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	pullRequest, err := store.CreatePullRequestRecord(
		ctx, fixture.orgID, fixture.sessionID, "github", "octo/widgets", "octocat", 21,
		"https://github.test/octo/widgets/pull/21", "feature", "main", "sha-21",
		"CI feedback", 1, 0, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	observation := domain.PullRequestObservation{
		State: contract.PRStateOpen, HeadSHA: "sha-21", CIState: contract.CIFailing,
		ReviewState: contract.ReviewNone, Mergeability: contract.MergeMergeable,
		Checks: json.RawMessage(`[{"name":"tests","status":"completed","conclusion":"failure"}]`),
	}
	if _, err := store.UpdatePullRequestObservation(ctx, fixture.orgID, pullRequest.ID, observation); err != nil {
		t.Fatalf("record CI failure: %v", err)
	}
	principal := domain.Principal{UserID: fixture.userID, Provider: "local"}
	page, err := store.ListNotifications(ctx, principal, fixture.orgID, domain.NotificationFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Type != "ci_failed" || page.Items[0].ResolvedAt != nil {
		t.Fatalf("failing notification = %+v", page.Items)
	}

	observation.CIState = contract.CIPassing
	observation.Checks = json.RawMessage(`[]`)
	if _, err := store.UpdatePullRequestObservation(ctx, fixture.orgID, pullRequest.ID, observation); err != nil {
		t.Fatalf("resolve CI failure: %v", err)
	}
	page, err = store.ListNotifications(ctx, principal, fixture.orgID, domain.NotificationFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ResolvedAt == nil {
		t.Fatalf("resolved notification = %+v", page.Items)
	}
}

func TestCIFailureApplicationKeyIsStablePerHead(t *testing.T) {
	pr := domain.PullRequest{ID: "pr-1", HeadSHA: "abc", CIState: contract.CIFailing}
	if got := ciFailureApplicationKey(pr); got != "ci-failure:pr-1:abc" {
		t.Fatalf("key = %q", got)
	}
}

func TestCIFailureMessageNamesFailingChecks(t *testing.T) {
	pr := domain.PullRequest{
		Repository: "ao/repo",
		Number:     42,
		Checks: json.RawMessage(`[
			{"name":"unit tests","conclusion":"failure","html_url":"https://example.test/unit"},
			{"name":"lint","conclusion":"success"}
		]`),
	}
	message := ciFailureMessage(pr)
	if !strings.Contains(message, "unit tests") || !strings.Contains(message, "https://example.test/unit") {
		t.Fatalf("message missing failing check details: %q", message)
	}
	if strings.Contains(message, "lint") {
		t.Fatalf("message includes passing check: %q", message)
	}
}

func TestShouldCreateCIFailureEffectOnlyOnTransition(t *testing.T) {
	passing := domain.PullRequest{HeadSHA: "sha-1", CIState: contract.CIPassing}
	failing := domain.PullRequest{HeadSHA: "sha-1", CIState: contract.CIFailing}
	if !shouldCreateCIFailureEffect(passing, failing) {
		t.Fatal("passing to failing should create an effect")
	}
	if shouldCreateCIFailureEffect(failing, failing) {
		t.Fatal("unchanged failure must not create another effect")
	}
	nextHeadFailing := domain.PullRequest{HeadSHA: "sha-2", CIState: contract.CIFailing}
	if !shouldCreateCIFailureEffect(failing, nextHeadFailing) {
		t.Fatal("a new failing head should create a new effect")
	}
}

func TestShouldResolveCIFailureEffectOnRecovery(t *testing.T) {
	failing := domain.PullRequest{CIState: contract.CIFailing}
	passing := domain.PullRequest{CIState: contract.CIPassing}
	if !shouldResolveCIFailureEffect(failing, passing) {
		t.Fatal("failing to passing should resolve the active notification")
	}
	if shouldResolveCIFailureEffect(passing, passing) {
		t.Fatal("unchanged passing state must not emit a resolution")
	}
}
