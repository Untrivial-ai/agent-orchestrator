package contract

import "time"

// LaunchReadinessState identifies startup evidence independently of display status.
type LaunchReadinessState string

// Launch readiness states mirror the daemon's durable facts.
const (
	LaunchReadinessLaunching     LaunchReadinessState = "launching"
	LaunchReadinessReady         LaunchReadinessState = "ready"
	LaunchReadinessNeedsInput    LaunchReadinessState = "needs_input"
	LaunchReadinessLaunchFailed  LaunchReadinessState = "launch_failed"
	LaunchReadinessResumeInvalid LaunchReadinessState = "resume_invalid"
)

// LaunchReadiness supplies the startup facts needed for status derivation.
type LaunchReadiness struct {
	State          LaunchReadinessState
	UpdatedAt      time.Time
	SignalExpected bool
}

// Ready preserves legacy presentation when startup evidence cannot be expected.
func (r LaunchReadiness) Ready() bool {
	return r.State == "" || r.State == LaunchReadinessReady ||
		(r.State == LaunchReadinessLaunching && !r.SignalExpected)
}

func readinessStatus(session SessionFacts, now time.Time, grace time.Duration) (SessionStatus, bool) {
	switch session.Readiness.State {
	case LaunchReadinessLaunching:
		if !session.Readiness.SignalExpected {
			return "", false
		}
		startedAt := session.Readiness.UpdatedAt
		if startedAt.IsZero() {
			startedAt = session.LastActivityAt
		}
		if !startedAt.IsZero() && now.Sub(startedAt) > grace {
			return StatusNoSignal, true
		}
		return StatusStarting, true
	case LaunchReadinessNeedsInput:
		return StatusNeedsInput, true
	case LaunchReadinessLaunchFailed:
		return StatusLaunchFailed, true
	case LaunchReadinessResumeInvalid:
		return StatusResumeInvalid, true
	default:
		return "", false
	}
}
