package postgres

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

func TestApplyPullRequestSnapshotNormalizesNullChecksToArray(t *testing.T) {
	tests := []struct {
		name   string
		checks json.RawMessage
		number int
	}{
		{name: "nil", number: 41},
		{name: "json null", checks: json.RawMessage("null"), number: 42},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, _, fixture := openNotificationTestStore(t)
			ctx := context.Background()
			pr, err := store.CreatePullRequestRecord(ctx, fixture.orgID, fixture.sessionID,
				"github", "octo/widgets", "owner", tt.number, "https://github.test/octo/widgets/pull/41",
				"feature", "main", "head", "Title", 0, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := domain.PullRequestSnapshot{
				URL: pr.URL, Title: pr.Title, Author: pr.Author, SourceBranch: pr.SourceBranch, TargetBranch: pr.TargetBranch,
				Observation: domain.PullRequestObservation{
					State: contract.PRStateOpen, HeadSHA: "head", CIState: contract.CIPassing,
					ReviewState: contract.ReviewNone, Mergeability: contract.MergeMergeable, Checks: tt.checks,
				},
			}
			if _, err := store.ApplyPullRequestSnapshot(ctx, fixture.orgID, pr.ID, snapshot, domain.PullRequestRefreshContext{}); err != nil {
				t.Fatalf("ApplyPullRequestSnapshot() error = %v; JSON null must not violate ao_pull_requests_checks_check", err)
			}
			got, err := store.PullRequestSnapshot(ctx, fixture.orgID, pr.ID)
			if err != nil {
				t.Fatal(err)
			}
			if string(got.Observation.Checks) != "[]" {
				t.Fatalf("checks = %s, want []", got.Observation.Checks)
			}
		})
	}
}

func TestApplyPullRequestSnapshotPersistsCommentedReviewAndInlineFeedback(t *testing.T) {
	store, _, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	pr, err := store.CreatePullRequestRecord(ctx, fixture.orgID, fixture.sessionID,
		"github", "octo/widgets", "owner", 31, "https://github.test/octo/widgets/pull/31",
		"feature", "main", "old", "Old title", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	snapshot := domain.PullRequestSnapshot{
		Title: "Updated title", URL: pr.URL, Author: "owner", AuthorAvatarURL: "https://avatars/owner",
		SourceBranch: "feature", TargetBranch: "main", BaseSHA: "base", ReviewsPartial: false,
		Observation: domain.PullRequestObservation{
			State: contract.PRStateOpen, HeadSHA: "new", CIState: contract.CIPassing,
			ReviewState: contract.ReviewNone, Mergeability: contract.MergeBlocked,
		},
		Reviews:  []domain.PullRequestReview{{ProviderID: "R1", DatabaseID: 11, Author: "mohak", State: contract.ReviewNone, Body: "one note", URL: "https://github.test/r1"}},
		Threads:  []domain.PullRequestReviewThread{{ProviderID: "T1", Path: "main.go", Line: 12}},
		Comments: []domain.PullRequestReviewComment{{ProviderID: "C1", ThreadProviderID: "T1", ReviewProviderID: "11", Author: "mohak", Body: "rename this", Path: "main.go", Line: 12}},
	}
	transition, err := store.ApplyPullRequestSnapshot(ctx, fixture.orgID, pr.ID, snapshot, domain.PullRequestRefreshContext{})
	if err != nil {
		t.Fatal(err)
	}
	if transition.Previous.HeadSHA != "old" || transition.Current.HeadSHA != "new" || len(transition.NewComments) != 1 {
		t.Fatalf("transition = %+v", transition)
	}
	got, err := store.PullRequestSnapshot(ctx, fixture.orgID, pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Reviews) != 1 || got.Reviews[0].Body != "one note" || len(got.Comments) != 1 || got.Comments[0].Body != "rename this" {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestApplyPullRequestSnapshotCompleteRefreshRemovesMissingFeedback(t *testing.T) {
	store, _, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	pr, err := store.CreatePullRequestRecord(ctx, fixture.orgID, fixture.sessionID,
		"github", "octo/widgets", "owner", 32, "https://github.test/octo/widgets/pull/32",
		"feature", "main", "head", "Title", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	first := domain.PullRequestSnapshot{
		URL: pr.URL, Title: pr.Title, Author: pr.Author, SourceBranch: pr.SourceBranch, TargetBranch: pr.TargetBranch,
		Observation: domain.PullRequestObservation{State: contract.PRStateOpen, HeadSHA: "head", CIState: contract.CIPassing, ReviewState: contract.ReviewNone, Mergeability: contract.MergeBlocked},
		Reviews:     []domain.PullRequestReview{{ProviderID: "R1", Author: "alice", State: contract.ReviewNone}},
		Threads:     []domain.PullRequestReviewThread{{ProviderID: "T1", Path: "a.go", Line: 1}},
		Comments:    []domain.PullRequestReviewComment{{ProviderID: "C1", ThreadProviderID: "T1", Author: "alice", Body: "fix", Path: "a.go", Line: 1}},
	}
	if _, err := store.ApplyPullRequestSnapshot(ctx, fixture.orgID, pr.ID, first, domain.PullRequestRefreshContext{}); err != nil {
		t.Fatal(err)
	}
	first.Reviews = nil
	first.Threads = nil
	first.Comments = nil
	if _, err := store.ApplyPullRequestSnapshot(ctx, fixture.orgID, pr.ID, first, domain.PullRequestRefreshContext{}); err != nil {
		t.Fatal(err)
	}
	got, err := store.PullRequestSnapshot(ctx, fixture.orgID, pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Reviews) != 0 || len(got.Threads) != 0 || len(got.Comments) != 0 {
		t.Fatalf("stale feedback remains: %+v", got)
	}
}

func TestPRFactsBySessionIncludesUnresolvedHumanReviewComments(t *testing.T) {
	store, _, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	pr, err := store.CreatePullRequestRecord(ctx, fixture.orgID, fixture.sessionID,
		"github", "octo/widgets", "owner", 33, "https://github.test/octo/widgets/pull/33",
		"feature", "main", "head", "Title", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := domain.PullRequestSnapshot{
		URL: pr.URL, Title: pr.Title, Author: pr.Author, SourceBranch: pr.SourceBranch, TargetBranch: pr.TargetBranch,
		Observation: domain.PullRequestObservation{State: contract.PRStateOpen, HeadSHA: "head", CIState: contract.CIPassing, ReviewState: contract.ReviewNone, Mergeability: contract.MergeMergeable},
		Threads:     []domain.PullRequestReviewThread{{ProviderID: "T1", Path: "main.go", Line: 7}},
		Comments:    []domain.PullRequestReviewComment{{ProviderID: "C1", ThreadProviderID: "T1", Author: "alice", Body: "fix this", Path: "main.go", Line: 7}},
	}
	if _, err := store.ApplyPullRequestSnapshot(ctx, fixture.orgID, pr.ID, snapshot, domain.PullRequestRefreshContext{}); err != nil {
		t.Fatal(err)
	}

	facts, err := store.PRFactsBySession(ctx, fixture.orgID, []string{fixture.sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(facts[fixture.sessionID]) != 1 || !facts[fixture.sessionID][0].ReviewComments {
		t.Fatalf("facts = %+v, want unresolved review comments", facts[fixture.sessionID])
	}
}

func TestApplyPullRequestSnapshotNotifiesAndResolvesReviewFeedback(t *testing.T) {
	store, _, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	principal := domain.Principal{UserID: fixture.userID, Provider: "local"}
	pr, err := store.CreatePullRequestRecord(ctx, fixture.orgID, fixture.sessionID,
		"github", "octo/widgets", "owner", 34, "https://github.test/octo/widgets/pull/34",
		"feature", "main", "head", "Title", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := domain.PullRequestSnapshot{
		URL: pr.URL, Title: pr.Title, Author: pr.Author, SourceBranch: pr.SourceBranch, TargetBranch: pr.TargetBranch,
		Observation: domain.PullRequestObservation{State: contract.PRStateOpen, HeadSHA: "head", CIState: contract.CIPassing, ReviewState: contract.ReviewNone, Mergeability: contract.MergeMergeable},
		Threads:     []domain.PullRequestReviewThread{{ProviderID: "T1", Path: "main.go", Line: 7}},
		Comments:    []domain.PullRequestReviewComment{{ProviderID: "C1", ThreadProviderID: "T1", Author: "alice", Body: "fix this", Path: "main.go", Line: 7}},
	}
	if _, err := store.ApplyPullRequestSnapshot(ctx, fixture.orgID, pr.ID, snapshot, domain.PullRequestRefreshContext{}); err != nil {
		t.Fatal(err)
	}

	page, err := store.ListNotifications(ctx, principal, fixture.orgID, domain.NotificationFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	feedback := notificationByType(page.Items, "review_feedback")
	if feedback == nil || feedback.Status != string(domain.NotificationStatusUnread) || feedback.ResolvedAt != nil {
		t.Fatalf("review feedback notification = %+v, notifications = %+v", feedback, page.Items)
	}

	snapshot.Threads[0].Resolved = true
	snapshot.Comments[0].Resolved = true
	if _, err := store.ApplyPullRequestSnapshot(ctx, fixture.orgID, pr.ID, snapshot, domain.PullRequestRefreshContext{}); err != nil {
		t.Fatal(err)
	}
	page, err = store.ListNotifications(ctx, principal, fixture.orgID, domain.NotificationFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	feedback = notificationByType(page.Items, "review_feedback")
	if feedback == nil || feedback.ResolvedAt == nil {
		t.Fatalf("resolved review feedback notification = %+v, notifications = %+v", feedback, page.Items)
	}
}

func TestApplyPullRequestSnapshotBackfillsMissingReviewFeedbackNotification(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	principal := domain.Principal{UserID: fixture.userID, Provider: "local"}
	pr, err := store.CreatePullRequestRecord(ctx, fixture.orgID, fixture.sessionID,
		"github", "octo/widgets", "owner", 35, "https://github.test/octo/widgets/pull/35",
		"feature", "main", "head", "Title", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := domain.PullRequestSnapshot{
		URL: pr.URL, Title: pr.Title, Author: pr.Author, SourceBranch: pr.SourceBranch, TargetBranch: pr.TargetBranch,
		Observation: domain.PullRequestObservation{State: contract.PRStateOpen, HeadSHA: "head", CIState: contract.CIPassing, ReviewState: contract.ReviewNone, Mergeability: contract.MergeMergeable},
		Threads:     []domain.PullRequestReviewThread{{ProviderID: "T1", Path: "main.go", Line: 7}},
		Comments:    []domain.PullRequestReviewComment{{ProviderID: "C1", ThreadProviderID: "T1", Author: "alice", Body: "fix this", Path: "main.go", Line: 7}},
	}
	if _, err := store.ApplyPullRequestSnapshot(ctx, fixture.orgID, pr.ID, snapshot, domain.PullRequestRefreshContext{}); err != nil {
		t.Fatal(err)
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('ao.service','control-plane',true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM ao_notifications WHERE pull_request_id=$1 AND type='review_feedback'`, pr.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ApplyPullRequestSnapshot(ctx, fixture.orgID, pr.ID, snapshot, domain.PullRequestRefreshContext{}); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListNotifications(ctx, principal, fixture.orgID, domain.NotificationFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if feedback := notificationByType(page.Items, "review_feedback"); feedback == nil {
		t.Fatalf("notifications = %+v, want backfilled review feedback", page.Items)
	}
}

func notificationByType(notifications []domain.Notification, kind string) *domain.Notification {
	for index := range notifications {
		if notifications[index].Type == kind {
			return &notifications[index]
		}
	}
	return nil
}
