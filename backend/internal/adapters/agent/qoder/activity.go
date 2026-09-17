package qoder

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

// DeriveActivityState maps a Qoder hook event name onto AO's activity state.
// The second return value is false for events AO does not model, so callers
// leave the session's current state untouched. The hook payload is unused:
// Qoder's event name alone carries the state transition.
func DeriveActivityState(event string, _ []byte) (domain.ActivityState, bool) {
	switch event {
	case "session-start", "stop":
		return domain.ActivityIdle, true
	case "user-prompt-submit", "pre-tool-use", "post-tool-use", "post-tool-use-failure":
		return domain.ActivityActive, true
	case "permission-request":
		return domain.ActivityBlocked, true
	case "session-end":
		return domain.ActivityExited, true
	default:
		return "", false
	}
}
