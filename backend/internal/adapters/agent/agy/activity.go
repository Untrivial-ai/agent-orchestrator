package agy

import (
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// DeriveActivityState maps an Agy hook event onto an AO activity state. The
// bool is false when the event carries no activity signal.
//
// event is the AO hook sub-command name installed in the managed AGY hook:
// "pre-invocation", "post-tool-use", or "stop".
func DeriveActivityState(event string, _ []byte) (domain.ActivityState, bool) {
	switch event {
	case "pre-invocation", "post-tool-use":
		return domain.ActivityActive, true
	case "stop":
		return domain.ActivityIdle, true
	default:
		return "", false
	}
}
