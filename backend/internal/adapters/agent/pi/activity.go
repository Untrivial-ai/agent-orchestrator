package pi

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

// DeriveActivityState maps the lifecycle callbacks emitted by AO's managed Pi
// extension onto normalized activity. session-start is idle because Pi emits it
// before any prompt starts; before_agent_start supplies the active transition.
func DeriveActivityState(event string, _ []byte) (domain.ActivityState, bool) {
	switch event {
	case "session-start", "stop":
		return domain.ActivityIdle, true
	case "user-prompt-submit":
		return domain.ActivityActive, true
	case "session-end":
		return domain.ActivityExited, true
	default:
		return "", false
	}
}
