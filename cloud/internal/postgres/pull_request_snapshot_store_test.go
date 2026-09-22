package postgres

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

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
	transition, err := store.ApplyPullRequestSnapshot(ctx, fixture.orgID, pr.ID, snapshot)
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
	if _, err := store.ApplyPullRequestSnapshot(ctx, fixture.orgID, pr.ID, first); err != nil {
		t.Fatal(err)
	}
	first.Reviews = nil
	first.Threads = nil
	first.Comments = nil
	if _, err := store.ApplyPullRequestSnapshot(ctx, fixture.orgID, pr.ID, first); err != nil {
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
