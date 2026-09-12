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
	case "claude-code":
		return deriveClaudeActivity(event, payload)
	case "codex":
		return deriveCodexActivity(event)
	case "cursor":
		return deriveStandardActivity(event)
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
	case "claude-code":
		switch event.Event {
		case "session-start":
			return event.State == "" && event.AgentSessionID != ""
		case "user-prompt-submit", "pre-tool-use", "post-tool-use", "post-tool-use-failure":
			return event.State == contract.ActivityActive
		case "permission-request":
			return event.State == contract.ActivityBlocked
		case "stop":
			return event.State == contract.ActivityIdle
		case "notification":
			return event.State == contract.ActivityIdle ||
				event.State == contract.ActivityWaitingInput ||
				event.State == contract.ActivityBlocked
		case "session-end":
			return event.State == contract.ActivityExited
		}
	case "codex":
		switch event.Event {
		case "session-start":
			return event.State == "" && event.AgentSessionID != ""
		case "user-prompt-submit":
			return event.State == contract.ActivityActive
		case "permission-request":
			return event.State == contract.ActivityWaitingInput
		case "stop":
			return event.State == contract.ActivityIdle
		}
	case "cursor":
		switch event.Event {
		case "session-start", "user-prompt-submit":
			return event.State == contract.ActivityActive
		case "permission-request":
			return event.State == contract.ActivityWaitingInput
		case "stop":
			return event.State == contract.ActivityIdle
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

func deriveClaudeActivity(
	event string,
	payload []byte,
) (contract.ActivityState, bool) {
	switch event {
	case "user-prompt-submit", "pre-tool-use", "post-tool-use", "post-tool-use-failure":
		return contract.ActivityActive, true
	case "permission-request":
		return contract.ActivityBlocked, true
	case "stop":
		return contract.ActivityIdle, true
	case "notification":
		var notification struct {
			Type string `json:"notification_type"`
		}
		_ = json.Unmarshal(payload, &notification)
		switch notification.Type {
		case "idle_prompt", "agent_completed":
			return contract.ActivityIdle, true
		case "agent_needs_input":
			return contract.ActivityWaitingInput, true
		case "permission_prompt":
			return contract.ActivityBlocked, true
		}
	case "session-end":
		var ended struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(payload, &ended)
		if ended.Reason != "clear" && ended.Reason != "resume" {
			return contract.ActivityExited, true
		}
	}
	return "", false
}

func deriveCodexActivity(event string) (contract.ActivityState, bool) {
	switch event {
	case "user-prompt-submit":
		return contract.ActivityActive, true
	case "permission-request":
		return contract.ActivityWaitingInput, true
	case "stop":
		return contract.ActivityIdle, true
	default:
		return "", false
	}
}

func deriveStandardActivity(event string) (contract.ActivityState, bool) {
	switch event {
	case "session-start", "user-prompt-submit":
		return contract.ActivityActive, true
	case "permission-request":
		return contract.ActivityWaitingInput, true
	case "stop":
		return contract.ActivityIdle, true
	default:
		return "", false
	}
}
