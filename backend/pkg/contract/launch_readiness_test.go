package contract_test

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

func TestLaunchReadinessPresentation(t *testing.T) {
	const grace = 90 * time.Second
	for _, tt := range []struct {
		name      string
		readiness contract.LaunchReadinessState
		age       time.Duration
		want      contract.SessionStatus
		display   contract.DisplayStatus
	}{
		{"starting", contract.LaunchReadinessLaunching, 0, contract.StatusStarting, contract.DisplayStarting},
		{"grace boundary", contract.LaunchReadinessLaunching, grace, contract.StatusStarting, contract.DisplayStarting},
		{"silent", contract.LaunchReadinessLaunching, grace + time.Second, contract.StatusNoSignal, contract.DisplayNoSignal},
		{"needs input", contract.LaunchReadinessNeedsInput, 2 * grace, contract.StatusNeedsInput, contract.DisplayNeedsInputToStart},
		{"failed", contract.LaunchReadinessLaunchFailed, 0, contract.StatusLaunchFailed, contract.DisplayLaunchFailed},
		{"invalid resume", contract.LaunchReadinessResumeInvalid, 0, contract.StatusResumeInvalid, contract.DisplayResumeInvalid},
	} {
		t.Run(tt.name, func(t *testing.T) {
			facts := contract.SessionFacts{Activity: contract.ActivityIdle, LastActivityAt: statusNow, Readiness: contract.LaunchReadiness{State: tt.readiness, UpdatedAt: statusNow.Add(-tt.age), SignalExpected: true}}
			if got := contract.DeriveStatus(facts, []contract.PRFacts{{Mergeability: contract.MergeMergeable}}, statusNow, grace); got != tt.want {
				t.Fatalf("status=%s want=%s", got, tt.want)
			}
			board := contract.DeriveKanbanPresentation(contract.KanbanSessionFacts{SessionFacts: facts}, []contract.KanbanPRFacts{{}}, statusNow, grace)
			if board.Column != contract.KanbanBuilding || board.DisplayStatus != tt.display {
				t.Fatalf("board=%+v", board)
			}
			facts.IsTerminated = true
			if got := contract.DeriveStatus(facts, nil, statusNow, grace); got != contract.StatusTerminated {
				t.Fatalf("terminated status=%s", got)
			}
		})
	}
}
