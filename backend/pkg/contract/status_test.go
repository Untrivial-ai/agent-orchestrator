package contract_test

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

var statusNow = time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

func session(activity contract.ActivityState) contract.SessionFacts {
	return contract.SessionFacts{
		Activity:       activity,
		LastActivityAt: statusNow,
		HasSignal:      true,
	}
}

func TestDeriveStatusPrecedence(t *testing.T) {
	tests := []struct {
		name    string
		session contract.SessionFacts
		prs     []contract.PRFacts
		want    contract.SessionStatus
	}{
		{"terminated", contract.SessionFacts{IsTerminated: true}, nil, contract.StatusTerminated},
		{"terminated merged", contract.SessionFacts{IsTerminated: true}, []contract.PRFacts{{Merged: true}}, contract.StatusMerged},
		{"terminated closed only", contract.SessionFacts{IsTerminated: true}, []contract.PRFacts{{Closed: true}}, contract.StatusTerminated},
		{"terminated merged with open pr", contract.SessionFacts{IsTerminated: true}, []contract.PRFacts{{Merged: true}, {Merged: false}}, contract.StatusTerminated},
		{"terminated all merged", contract.SessionFacts{IsTerminated: true}, []contract.PRFacts{{Merged: true}, {Merged: true}}, contract.StatusMerged},
		{"active before PR", session(contract.ActivityActive), []contract.PRFacts{{CI: contract.CIFailing}}, contract.StatusWorking},
		{"exited before PR", session(contract.ActivityExited), []contract.PRFacts{{Mergeability: contract.MergeMergeable}}, contract.StatusExited},
		{"waiting before PR", session(contract.ActivityWaitingInput), []contract.PRFacts{{CI: contract.CIFailing}}, contract.StatusNeedsInput},
		{"blocked before PR", session(contract.ActivityBlocked), []contract.PRFacts{{CI: contract.CIFailing}}, contract.StatusNeedsInput},
		{"PR before idle", session(contract.ActivityIdle), []contract.PRFacts{{CI: contract.CIFailing}}, contract.StatusCIFailed},
		{"idle", session(contract.ActivityIdle), nil, contract.StatusIdle},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := contract.DeriveStatus(tt.session, tt.prs, statusNow, 90*time.Second)
			if got != tt.want {
				t.Fatalf("DeriveStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDeriveStatusNoSignalRules(t *testing.T) {
	const grace = 90 * time.Second
	silent := contract.SessionFacts{
		Activity:       contract.ActivityIdle,
		LastActivityAt: statusNow.Add(-2 * grace),
	}

	tests := []struct {
		name           string
		session        contract.SessionFacts
		signalExpected bool
		now            time.Time
		want           contract.SessionStatus
	}{
		{"past grace", silent, true, statusNow, contract.StatusNoSignal},
		{"signal not expected", silent, false, statusNow, contract.StatusIdle},
		{"signal received", withSignal(silent), true, statusNow, contract.StatusIdle},
		{"at boundary", silent, true, silent.LastActivityAt.Add(grace), contract.StatusIdle},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			facts := tt.session
			facts.SignalExpected = tt.signalExpected
			got := contract.DeriveStatus(facts, nil, tt.now, grace)
			if got != tt.want {
				t.Fatalf("DeriveStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDeriveSCMStatusPipelineAndWorstWins(t *testing.T) {
	tests := []struct {
		name string
		prs  []contract.PRFacts
		want contract.SessionStatus
	}{
		{"closed ignored", []contract.PRFacts{{Closed: true}}, ""},
		{"merged", []contract.PRFacts{{Merged: true}}, contract.StatusMerged},
		{"open", []contract.PRFacts{{}}, contract.StatusPROpen},
		{"review pending", []contract.PRFacts{{Review: contract.ReviewRequired}}, contract.StatusReviewPending},
		{"review pending with provider merge blocker", []contract.PRFacts{{Review: contract.ReviewRequired, Mergeability: contract.MergeBlocked}}, contract.StatusReviewPending},
		{"approved", []contract.PRFacts{{Review: contract.ReviewApproved}}, contract.StatusApproved},
		{"mergeable", []contract.PRFacts{{Mergeability: contract.MergeMergeable}}, contract.StatusMergeable},
		{"merge blocked", []contract.PRFacts{{Mergeability: contract.MergeBlocked}}, contract.StatusPROpen},
		{"merge blocked with approved review", []contract.PRFacts{{Mergeability: contract.MergeBlocked, Review: contract.ReviewApproved}}, contract.StatusPROpen},
		{"changes requested", []contract.PRFacts{{Review: contract.ReviewChangesRequest}}, contract.StatusChangesRequested},
		{"review comments", []contract.PRFacts{{ReviewComments: true}}, contract.StatusChangesRequested},
		{"draft", []contract.PRFacts{{Draft: true}}, contract.StatusDraft},
		{"CI failed", []contract.PRFacts{{CI: contract.CIFailing}}, contract.StatusCIFailed},
		{
			"worst wins",
			[]contract.PRFacts{
				{URL: "a", SourceBranch: "a", TargetBranch: "main", Mergeability: contract.MergeMergeable},
				{URL: "b", SourceBranch: "b", TargetBranch: "main", CI: contract.CIFailing},
			},
			contract.StatusCIFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := contract.DeriveSCMStatus(tt.prs); got != tt.want {
				t.Fatalf("DeriveSCMStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStackRules(t *testing.T) {
	parent := contract.PRFacts{
		URL:          "parent",
		SourceBranch: "feature",
		TargetBranch: "main",
		Mergeability: contract.MergeMergeable,
	}
	child := contract.PRFacts{
		URL:          "child",
		SourceBranch: "feature/child",
		TargetBranch: "feature",
	}

	positions := contract.BuildStacks([]contract.PRFacts{parent, child})
	if positions["parent"].Blocked || !positions["parent"].BottomOfStack {
		t.Fatalf("parent position = %+v", positions["parent"])
	}
	if !positions["child"].Blocked || positions["child"].BottomOfStack {
		t.Fatalf("child position = %+v", positions["child"])
	}

	if got := contract.DeriveSCMStatus([]contract.PRFacts{parent, child}); got != contract.StatusMergeable {
		t.Fatalf("blocked child readiness was not suppressed: got %q", got)
	}
	child.CI = contract.CIFailing
	if got := contract.DeriveSCMStatus([]contract.PRFacts{parent, child}); got != contract.StatusCIFailed {
		t.Fatalf("blocked child problem was suppressed: got %q", got)
	}

	parent.Merged = true
	positions = contract.BuildStacks([]contract.PRFacts{parent, child})
	if positions["child"].Blocked {
		t.Fatal("merged parent still blocks child")
	}
}

func withSignal(facts contract.SessionFacts) contract.SessionFacts {
	facts.HasSignal = true
	return facts
}
