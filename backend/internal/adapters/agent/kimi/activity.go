package kimi

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

// DeriveActivityState maps Kimi lifecycle hooks onto AO activity facts.
// PermissionResult clears the waiting-input state as soon as Kimi resumes the
// turn after the user answers a permission request.
func DeriveActivityState(event string, _ []byte) (domain.ActivityState, bool) {
	switch event {
	case "session-start", "user-prompt-submit", "permission-result":
		return domain.ActivityActive, true
	case "permission-request":
		return domain.ActivityWaitingInput, true
	case "stop":
		return domain.ActivityIdle, true
	default:
		return "", false
	}
}
