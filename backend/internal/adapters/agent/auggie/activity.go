package auggie

import (
	"encoding/json"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// DeriveActivityState maps Auggie lifecycle callbacks onto normalized AO
// activity. A Stop caused by an error or the iteration limit requires attention;
// normal completion and interruption leave the session idle.
func DeriveActivityState(event string, payload []byte) (domain.ActivityState, bool) {
	switch event {
	case "session-start", "pre-tool-use", "post-tool-use":
		return domain.ActivityActive, true
	case "stop":
		var native struct {
			Cause string `json:"agent_stop_cause"`
		}
		if err := json.Unmarshal(payload, &native); err != nil {
			return "", false
		}
		switch native.Cause {
		case "end_turn", "interrupted":
			return domain.ActivityIdle, true
		case "error", "max_iterations":
			return domain.ActivityWaitingInput, true
		default:
			return "", false
		}
	case "session-end":
		return domain.ActivityExited, true
	default:
		return "", false
	}
}
