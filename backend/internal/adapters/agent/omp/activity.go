package omp

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

// DeriveActivityState maps callbacks from AO's managed OMP extension onto the
// durable activity states used by session status derivation.
func DeriveActivityState(event string, _ []byte) (domain.ActivityState, bool) {
	switch event {
	case "session-start", "stop":
		return domain.ActivityIdle, true
	case "user-prompt-submit", "permission-resolved":
		return domain.ActivityActive, true
	case "permission-request":
		return domain.ActivityWaitingInput, true
	case "session-end":
		return domain.ActivityExited, true
	default:
		return "", false
	}
}
