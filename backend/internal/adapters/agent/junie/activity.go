package junie

import (
	"encoding/json"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// NativeSessionID accepts only an exact, restorable Junie identity. Validate
// before generic metadata normalization can trim or remove control characters.
func NativeSessionID(payload []byte) string {
	var event struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(payload, &event) != nil || validateNativeSessionID(event.SessionID) != nil {
		return ""
	}
	return event.SessionID
}

// DeriveActivityState maps Junie's experimental TUI callbacks. Startup carries
// identity only. PermissionRequest is understood for conformance tests but is
// deliberately not installed until observation is proven safe on a stable build.
func DeriveActivityState(event string, payload []byte) (domain.ActivityState, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(payload, &object) != nil || object == nil {
		return "", false
	}
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
