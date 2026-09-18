package worker

import (
	"encoding/json"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

// ActivityEvent is an explicit lifecycle signal emitted by a coding-agent hook.
type ActivityEvent struct {
	Harness        string                 `json:"harness"`
	Event          string                 `json:"event"`
	State          contract.ActivityState `json:"state"`
	ToolName       string                 `json:"toolName,omitempty"`
	ToolUseID      string                 `json:"toolUseId,omitempty"`
	AgentSessionID string                 `json:"agentSessionId,omitempty"`
}

const maxActivityCorrelationLength = 256

// DeriveActivity maps native hook callbacks to AO's durable activity states.
func DeriveActivity(
	harness string,
	event string,
	payload []byte,
) (contract.ActivityState, bool) {
	switch harness {
	case "opencode":
		return deriveOpenCodeActivity(event)
	default:
		return "", false
	}
}

// ActivityEventFromHook derives an activity event and retains bounded tool
// correlation facts used to clear only the permission dialog that completed.
func ActivityEventFromHook(
	harness string,
	event string,
	payload []byte,
) (ActivityEvent, bool) {
	var native struct {
		ToolName            string `json:"tool_name"`
		ToolUseID           string `json:"tool_use_id"`
		SessionID           string `json:"session_id"`
		SessionIDCamel      string `json:"sessionId"`
		ConversationID      string `json:"conversation_id"`
		ConversationIDCamel string `json:"conversationId"`
	}
	_ = json.Unmarshal(payload, &native)
	if len(native.ToolName) > maxActivityCorrelationLength {
		native.ToolName = ""
	}
	if len(native.ToolUseID) > maxActivityCorrelationLength {
		native.ToolUseID = ""
	}
	agentSessionID := firstNonEmpty(
		native.SessionID,
		native.SessionIDCamel,
		native.ConversationID,
		native.ConversationIDCamel,
	)
	if len(agentSessionID) > maxActivityCorrelationLength {
		agentSessionID = ""
	}
	state, hasActivity := DeriveActivity(harness, event, payload)
	if !hasActivity && agentSessionID == "" {
		return ActivityEvent{}, false
	}
	return ActivityEvent{
		Harness:        harness,
		Event:          event,
		State:          state,
		ToolName:       native.ToolName,
		ToolUseID:      native.ToolUseID,
		AgentSessionID: agentSessionID,
	}, true
}

// ValidActivityEvent ensures workers cannot pair a real hook name with an
// impossible state when reporting activity to the control plane.
func ValidActivityEvent(event ActivityEvent) bool {
	if len(event.ToolName) > maxActivityCorrelationLength ||
		len(event.ToolUseID) > maxActivityCorrelationLength ||
		len(event.AgentSessionID) > maxActivityCorrelationLength {
		return false
	}
	switch event.Harness {
	case "opencode":
		switch event.Event {
		case "session-start":
			// A created session carries its native id immediately, so the first
			// opencode event is only a valid active signal once it can be
			// correlated to a resumable conversation.
			return event.State == contract.ActivityActive && event.AgentSessionID != ""
		case "user-prompt-submit", "active":
			return event.State == contract.ActivityActive
		case "stop":
			return event.State == contract.ActivityIdle
		case "permission-blocked":
			return event.State == contract.ActivityBlocked
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// deriveOpenCodeActivity maps the opencode plugin's normalized events onto AO
// activity states, mirroring the desktop opencode adapter: "session-start",
// "user-prompt-submit" and "active" mark a live turn, "stop" marks the turn as
// finished, and "permission-blocked" marks an awaiting-approval dialog.
func deriveOpenCodeActivity(event string) (contract.ActivityState, bool) {
	switch event {
	case "session-start", "user-prompt-submit", "active":
		return contract.ActivityActive, true
	case "stop":
		return contract.ActivityIdle, true
	case "permission-blocked":
		return contract.ActivityBlocked, true
	default:
		return "", false
	}
}