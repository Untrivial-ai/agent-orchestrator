package session

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/activitydispatch"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

func TestLaunchReadinessCapability(t *testing.T) {
	for _, tt := range []struct {
		name    string
		mode    domain.SessionMode
		harness domain.AgentHarness
		tracked bool
	}{
		{"complete TUI", domain.SessionModeTUI, domain.HarnessCodex, true},
		{"completion-only TUI", domain.SessionModeTUI, domain.HarnessAider, false},
		{"version-dependent TUI", domain.SessionModeTUI, domain.HarnessContinue, false},
		{"no-hook TUI", domain.SessionModeTUI, domain.HarnessCrush, false},
		{"Chat without terminal hooks", domain.SessionModeChat, domain.HarnessCrush, true},
		{"Chat with terminal hooks", domain.SessionModeChat, domain.HarnessCodex, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			capable := activitydispatch.FullySupportsHarness(tt.harness)
			for _, age := range []time.Duration{time.Second, noSignalGrace + time.Second} {
				rec := silentRec(age)
				rec.Mode = tt.mode
				rec.Harness = tt.harness
				rec.LaunchReadiness = domain.LaunchReadiness{State: domain.LaunchReadinessLaunching, UpdatedAt: statusNow.Add(-age)}
				want := domain.StatusIdle
				if tt.tracked {
					want = domain.StatusStarting
					if age > noSignalGrace {
						want = domain.StatusNoSignal
					}
				}
				if got := deriveStatus(rec, nil, statusNow, capable); got != want {
					t.Fatalf("age=%s: status=%s want=%s", age, got, want)
				}
				if !tt.tracked {
					prs := statusPR(domain.PRFacts{Mergeability: domain.MergeMergeable})
					legacy := rec
					legacy.LaunchReadiness = domain.LaunchReadiness{}
					if got, want := deriveKanbanPresentation(rec, prs, nil, statusNow, capable), deriveKanbanPresentation(legacy, prs, nil, statusNow, capable); got != want {
						t.Fatalf("silent harness displaced PR presentation: got=%+v want=%+v", got, want)
					}
				}
				for _, evidence := range []struct {
					state   domain.LaunchReadinessState
					want    domain.SessionStatus
					display contract.DisplayStatus
				}{
					{domain.LaunchReadinessNeedsInput, domain.StatusNeedsInput, contract.DisplayNeedsInputToStart},
					{domain.LaunchReadinessLaunchFailed, domain.StatusLaunchFailed, contract.DisplayLaunchFailed},
					{domain.LaunchReadinessResumeInvalid, domain.StatusResumeInvalid, contract.DisplayResumeInvalid},
				} {
					rec.LaunchReadiness.State = evidence.state
					if got := deriveStatus(rec, nil, statusNow, capable); got != evidence.want {
						t.Fatalf("explicit evidence hidden: status=%s want=%s", got, evidence.want)
					}
					if got := deriveKanbanPresentation(rec, nil, nil, statusNow, capable); got.DisplayStatus != evidence.display {
						t.Fatalf("explicit evidence hidden on board: %+v", got)
					}
				}
			}
		})
	}
}
