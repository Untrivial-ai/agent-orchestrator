package junie

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

func DeriveActivityState(event string, _ []byte) (domain.ActivityState, bool) {
	switch event {
	case "user-prompt-submit", "pre-tool-use":
		return domain.ActivityActive, true
	case "permission-request":
		return domain.ActivityBlocked, true
	case "stop":
		return domain.ActivityIdle, true
	case "stop-failure":
		return domain.ActivityWaitingInput, true
	case "session-end":
		return domain.ActivityExited, true
	default:
		return "", false
	}
}
