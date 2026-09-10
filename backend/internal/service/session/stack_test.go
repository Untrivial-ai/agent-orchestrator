package session

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// live builds an idle, non-terminated session that has already signaled, so the
// derived status is governed purely by its PRs.
func live() domain.SessionRecord {
	return statusRec(domain.ActivityIdle, false)
}

func TestBuildStacksMarksBlockedChildren(t *testing.T) {
	// #142 (root → main), #143 stacked on #142, #144 stacked on #143.
	prs := []domain.PRFacts{
		{URL: "p142", SourceBranch: "ao/abc", TargetBranch: "main"},
		{URL: "p143", SourceBranch: "ao/abc/auth", TargetBranch: "ao/abc"},
		{URL: "p144", SourceBranch: "ao/abc/tests", TargetBranch: "ao/abc/auth"},
	}
	st := buildStacks(prs)
	if st["p142"].Blocked || !st["p142"].BottomOfStack {
		t.Fatalf("root should be unblocked bottom-of-stack, got %+v", st["p142"])
	}
	if !st["p143"].Blocked || st["p143"].BottomOfStack {
		t.Fatalf("middle should be blocked, got %+v", st["p143"])
	}
	if !st["p144"].Blocked {
		t.Fatalf("top should be blocked, got %+v", st["p144"])
	}
}

func TestBuildStacksMergedParentUnblocksChild(t *testing.T) {
	prs := []domain.PRFacts{
		{URL: "p142", SourceBranch: "ao/abc", TargetBranch: "main", Merged: true},
		{URL: "p143", SourceBranch: "ao/abc/auth", TargetBranch: "ao/abc"},
	}
	st := buildStacks(prs)
	if st["p143"].Blocked {
		t.Fatal("child should be unblocked once parent is merged")
	}
}

func TestDeriveStatusWorstWinsAcrossIndependentPRs(t *testing.T) {
	// Two independent open PRs (both target main): mergeable vs ci_failed.
	// CI failure is more urgent, so the session reports ci_failed.
	prs := []domain.PRFacts{
		{URL: "a", SourceBranch: "ao/a", TargetBranch: "main", Mergeability: domain.MergeMergeable},
		{URL: "b", SourceBranch: "ao/b", TargetBranch: "main", CI: domain.CIFailing},
	}
	if got := deriveStatus(live(), prs, statusNow, true); got != domain.StatusCIFailed {
		t.Fatalf("got %q want ci_failed", got)
	}
}

func TestDeriveStatusAllMergeableReportsMergeable(t *testing.T) {
	prs := []domain.PRFacts{
		{URL: "a", SourceBranch: "ao/a", TargetBranch: "main", Mergeability: domain.MergeMergeable},
		{URL: "b", SourceBranch: "ao/b", TargetBranch: "main", Mergeability: domain.MergeMergeable},
	}
	if got := deriveStatus(live(), prs, statusNow, true); got != domain.StatusMergeable {
		t.Fatalf("got %q want mergeable", got)
	}
}

func TestDeriveStatusStackedChildExemptFromAggregation(t *testing.T) {
	// Root mergeable; blocked child is pr_open. Child is exempt, so the session
	// reports mergeable rather than being dragged down to pr_open.
	prs := []domain.PRFacts{
		{URL: "root", SourceBranch: "ao/abc", TargetBranch: "main", Mergeability: domain.MergeMergeable},
		{URL: "child", SourceBranch: "ao/abc/x", TargetBranch: "ao/abc"},
	}
	if got := deriveStatus(live(), prs, statusNow, true); got != domain.StatusMergeable {
		t.Fatalf("got %q want mergeable (child exempt)", got)
	}
}

func TestDeriveStatusMergedParentOpenChildStaysOnChild(t *testing.T) {
	// Parent merged, child now unblocked and review_pending: still alive, status
	// follows the open child.
	prs := []domain.PRFacts{
		{URL: "root", SourceBranch: "ao/abc", TargetBranch: "main", Merged: true},
		{URL: "child", SourceBranch: "ao/abc/x", TargetBranch: "main", Review: domain.ReviewRequired},
	}
	if got := deriveStatus(live(), prs, statusNow, true); got != domain.StatusReviewPending {
		t.Fatalf("got %q want review_pending", got)
	}
}

func TestDeriveStatusAllMergedReportsMerged(t *testing.T) {
	prs := []domain.PRFacts{
		{URL: "a", Merged: true},
		{URL: "b", Merged: true},
	}
	if got := deriveStatus(live(), prs, statusNow, true); got != domain.StatusMerged {
		t.Fatalf("got %q want merged", got)
	}
}

func TestDeriveStatusAllClosedNoneMergedFallsToActivity(t *testing.T) {
	prs := []domain.PRFacts{
		{URL: "a", Closed: true},
		{URL: "b", Closed: true},
	}
	if got := deriveStatus(statusRec(domain.ActivityActive, false), prs, statusNow, true); got != domain.StatusWorking {
		t.Fatalf("got %q want working", got)
	}
}

func TestDeriveStatusEmptyPRsUsesActivity(t *testing.T) {
	if got := deriveStatus(statusRec(domain.ActivityActive, false), nil, statusNow, true); got != domain.StatusWorking {
		t.Fatalf("got %q want working", got)
	}
}

func TestDeriveStatusDegenerateAllBlockedStillAggregates(t *testing.T) {
	// Two PRs each targeting the other's source branch (no visible root). The
	// fallback aggregates across all so the session never goes dark.
	prs := []domain.PRFacts{
		{URL: "a", SourceBranch: "x", TargetBranch: "y", CI: domain.CIFailing},
		{URL: "b", SourceBranch: "y", TargetBranch: "x", Mergeability: domain.MergeMergeable},
	}
	if got := deriveStatus(live(), prs, statusNow, true); got != domain.StatusCIFailed {
		t.Fatalf("got %q want ci_failed (degenerate fallback)", got)
	}
}
