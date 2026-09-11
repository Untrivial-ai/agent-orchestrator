package httpapi

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

func TestReviewStateForPullRequest(t *testing.T) {
	open := domain.PullRequest{State: contract.PRStateOpen, HeadSHA: "head"}
	for _, test := range []struct {
		name string
		pr   domain.PullRequest
		runs []domain.ReviewRunPullRequest
		want string
	}{
		{name: "open PR needs review", pr: open, want: "needs_review"},
		{name: "draft is ineligible", pr: domain.PullRequest{State: contract.PRStateOpen, Draft: true, HeadSHA: "head"}, want: "ineligible"},
		{name: "closed is ineligible", pr: domain.PullRequest{State: contract.PRStateClosed, HeadSHA: "head"}, want: "ineligible"},
		{name: "running current head", pr: open, runs: []domain.ReviewRunPullRequest{{ReviewRun: domain.ReviewRun{TargetSHA: "head", Status: contract.AOReviewRunRunning}}}, want: "running"},
		{name: "approved current head", pr: open, runs: []domain.ReviewRunPullRequest{{ReviewRun: domain.ReviewRun{TargetSHA: "head", Status: contract.AOReviewRunDelivered, Verdict: contract.AOReviewVerdictApproved}}}, want: "up_to_date"},
		{name: "changes requested current head", pr: open, runs: []domain.ReviewRunPullRequest{{ReviewRun: domain.ReviewRun{TargetSHA: "head", Status: contract.AOReviewRunDelivered, Verdict: contract.AOReviewVerdictChangesRequested}}}, want: "changes_requested"},
		{name: "stale run needs new review", pr: open, runs: []domain.ReviewRunPullRequest{{ReviewRun: domain.ReviewRun{TargetSHA: "old", Status: contract.AOReviewRunDelivered, Verdict: contract.AOReviewVerdictApproved}}}, want: "needs_review"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := reviewStateForPullRequest(test.pr, test.runs); got != test.want {
				t.Fatalf("reviewStateForPullRequest() = %q, want %q", got, test.want)
			}
		})
	}
}
