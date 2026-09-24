package commandcode

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

// DeriveActivityState maps Command Code's available lifecycle hooks onto AO
// activity. Command Code exposes no permission-request hook, so the adapter
// cannot distinguish a tool approval prompt from other in-turn work.
func DeriveActivityState(event string, _ []byte) (domain.ActivityState, bool) {
	switch event {
	case "session-start", "pre-tool-use", "post-tool-use":
		return domain.ActivityActive, true
	case "stop":
		return domain.ActivityIdle, true
	default:
		return "", false
	}
}
