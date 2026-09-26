package postgres

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

func TestPullRequestNotificationTransitionReadinessMatchesLocal(t *testing.T) {
	ready := domain.PullRequest{State: contract.PRStateOpen, CIState: contract.CIPassing, ReviewState: contract.ReviewApproved, Mergeability: contract.MergeMergeable}
	if !pullRequestReadyToMerge(ready, false) {
		t.Fatal("ready PR rejected")
	}
	for name, mutate := range map[string]func(*domain.PullRequest){
		"draft":        func(pr *domain.PullRequest) { pr.Draft = true },
		"ci":           func(pr *domain.PullRequest) { pr.CIState = contract.CIFailing },
		"review":       func(pr *domain.PullRequest) { pr.ReviewState = contract.ReviewChangesRequest },
		"mergeability": func(pr *domain.PullRequest) { pr.Mergeability = contract.MergeBlocked },
		"merged":       func(pr *domain.PullRequest) { pr.State = contract.PRStateMerged },
	} {
		pr := ready
		mutate(&pr)
		if pullRequestReadyToMerge(pr, false) {
			t.Errorf("%s PR considered ready", name)
		}
	}
	if pullRequestReadyToMerge(ready, true) {
		t.Fatal("unresolved human comment did not block readiness")
	}
}

func TestPullRequestNotificationTransitionKinds(t *testing.T) {
	open := domain.PullRequest{State: contract.PRStateOpen, CIState: contract.CIPassing, ReviewState: contract.ReviewNone, Mergeability: contract.MergeBlocked}
	ready := open
	ready.Mergeability = contract.MergeMergeable
	merged := ready
	merged.State = contract.PRStateMerged
	closed := open
	closed.State = contract.PRStateClosed
	for _, tt := range []struct {
		name                    string
		previous, current       domain.PullRequest
		wantCreate, wantResolve string
	}{
		{"ready", open, ready, "ready_to_merge", ""},
		{"blocked", ready, open, "", "ready_to_merge"},
		{"merged", ready, merged, "pr_merged", "ready_to_merge"},
		{"closed", open, closed, "pr_closed_unmerged", "ready_to_merge"},
		{"ci failure", open, func() domain.PullRequest { v := open; v.CIState = contract.CIFailing; return v }(), "", "ready_to_merge"},
	} {
		create, resolve := pullRequestNotificationTransition(tt.previous, tt.current, false, false)
		if create != tt.wantCreate || resolve != tt.wantResolve {
			t.Errorf("%s = create %q resolve %q, want %q/%q", tt.name, create, resolve, tt.wantCreate, tt.wantResolve)
		}
	}
}
