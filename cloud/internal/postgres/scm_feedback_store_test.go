package postgres

import (
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

func TestSCMFeedbackCandidatesMatchLocalReviewPolicy(t *testing.T) {
	pr := domain.PullRequest{ID: "pr-1", Repository: "acme/widgets", Number: 7, URL: "https://github.com/acme/widgets/pull/7", HeadSHA: "abc"}
	transition := domain.PullRequestTransition{Current: pr,
		NewReviews: []domain.PullRequestReview{
			{ProviderID: "commented", Author: "mohak", State: contract.ReviewNone, Body: "general note", AutoInjectReview: true},
			{ProviderID: "changes", Author: "alice", State: contract.ReviewChangesRequest, Body: "fix this", AutoInjectReview: true},
		},
		NewComments: []domain.PullRequestReviewComment{
			{ProviderID: "human", Author: "bob", Body: "rename\x1b[2J this", Path: "main.go", Line: 12, AutoInjectReview: true},
			{ProviderID: "disabled", Author: "carol", Body: "not sent", Path: "a.go", Line: 2, AutoInjectReview: false},
			{ProviderID: "bot", Author: "lint[bot]", Body: "bot", Path: "b.go", Line: 3, IsBot: true, AutoInjectReview: true},
		},
	}
	got := scmFeedbackCandidates(transition)
	if len(got) != 2 {
		t.Fatalf("candidates = %+v, want changes-requested + human inline comment", got)
	}
	joined := got[0].Message + got[1].Message
	if strings.Contains(joined, "general note") || strings.Contains(joined, "not sent") || strings.Contains(joined, "\x1b") {
		t.Fatalf("ineligible or unsafe feedback included: %q", joined)
	}
	if !strings.Contains(joined, "fix this") || !strings.Contains(joined, "main.go:12") {
		t.Fatalf("actionable feedback missing: %q", joined)
	}
}

func TestSCMFeedbackCandidatesDeduplicateConflictByHeadAndBase(t *testing.T) {
	previous := domain.PullRequest{ID: "pr", Mergeability: contract.MergeMergeable, HeadSHA: "head", BaseSHA: "base"}
	current := previous
	current.Mergeability = contract.MergeConflicting
	got := scmFeedbackCandidates(domain.PullRequestTransition{Previous: previous, Current: current})
	if len(got) != 1 || got[0].ApplicationKey != "conflict:pr:head:base" {
		t.Fatalf("candidates = %+v", got)
	}
}
