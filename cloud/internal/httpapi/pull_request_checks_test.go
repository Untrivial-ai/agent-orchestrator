package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

func TestPullRequestFailingChecksFromStoredSnapshot(t *testing.T) {
	checks := json.RawMessage(`[{"name":"unit","status":"completed","conclusion":"failure","html_url":"https://example.test/unit"},{"name":"lint","status":"completed","conclusion":"success"}]`)
	got := pullRequestFailingChecks(checks)
	if len(got) != 1 || got[0].Name != "unit" || got[0].Status != "failed" || got[0].URL != "https://example.test/unit" {
		t.Fatalf("failing checks = %#v", got)
	}
}

func TestPullRequestSummaryIncludesSubmittedAndInlineReviews(t *testing.T) {
	pr := domain.PullRequest{ID: "pr", URL: "https://github.com/acme/widgets/pull/7", Repository: "acme/widgets", Number: 7, State: contract.PRStateOpen, CIState: contract.CIPassing, ReviewState: contract.ReviewNone, Mergeability: contract.MergeBlocked}
	snapshot := domain.PullRequestSnapshot{
		Reviews: []domain.PullRequestReview{{ProviderID: "R1", Author: "mohak", State: contract.ReviewNone, Body: "looks good with one note", URL: "https://github.com/r1", AutoInjectReview: true}},
		Comments: []domain.PullRequestReviewComment{
			{ProviderID: "C1", Author: "alice", Body: "rename this", URL: "https://github.com/c1", Path: "main.go", Line: 12, AutoInjectReview: true},
			{ProviderID: "C2", Author: "bob", Body: "done", URL: "https://github.com/c2", Path: "old.go", Line: 2, Resolved: true, AutoInjectReview: false},
		},
	}
	got := toPullRequestSummaryResponse(pr, snapshot)
	if len(got.Review.Reviews) != 1 || got.Review.Reviews[0].Body != "looks good with one note" {
		t.Fatalf("reviews = %+v", got.Review.Reviews)
	}
	if !got.Review.HasUnresolvedHumanComments || len(got.Review.UnresolvedBy) != 1 || got.Review.UnresolvedBy[0].Links[0].File != "main.go" {
		t.Fatalf("review summary = %+v", got.Review)
	}
	if len(got.Review.ResolvedBy) != 1 || got.Review.ResolvedBy[0].Links[0].AutoInjectReview {
		t.Fatalf("resolved = %+v", got.Review.ResolvedBy)
	}
}
