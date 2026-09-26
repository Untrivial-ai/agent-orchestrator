package mimocode

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

// DeriveActivityState maps MiMo Code hook events to AO activity states.
func DeriveActivityState(event string, _ []byte) (domain.ActivityState, bool) {
	switch event {
	case "session-start", "user-prompt-submit", "active":
		return domain.ActivityActive, true
	case "permission-blocked":
		return domain.ActivityBlocked, true
	case "stop":
		return domain.ActivityIdle, true
	default:
		return "", false
	}
}
