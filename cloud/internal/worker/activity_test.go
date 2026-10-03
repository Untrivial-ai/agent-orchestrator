package worker

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

func TestActivityEventFromHookCarriesConversationFacts(t *testing.T) {
	event, ok := ActivityEventFromHook("codex", "user-prompt-submit", []byte(`{
		"session_id":"native-1",
		"prompt":"from the terminal"
	}`))
	if !ok {
		t.Fatal("hook event was not accepted")
	}
	if event.AgentSessionID != "native-1" || event.LatestUserPrompt != "from the terminal" {
		t.Fatalf("event = %+v, want native identity and prompt", event)
	}
}

func TestActivityEventFromHookCarriesAssistantFactOnStop(t *testing.T) {
	event, ok := ActivityEventFromHook("codex", "stop", []byte(`{
		"sessionId":"native-1",
		"lastAssistantMessage":"reply from the terminal"
	}`))
	if !ok {
		t.Fatal("hook event was not accepted")
	}
	if event.LatestAssistantUpdate != "reply from the terminal" {
		t.Fatalf("assistant update = %q", event.LatestAssistantUpdate)
	}
}

func TestActivityEventFromHookOpenCode(t *testing.T) {
	// session-start lands the native session id used for --session restore; it
	// carries no activity state, so it flows purely on the identity path.
	start, ok := ActivityEventFromHook("opencode", "session-start", []byte(`{"session_id":"ses_abc"}`))
	if !ok || start.AgentSessionID != "ses_abc" || start.State != "" {
		t.Fatalf("session-start = (%+v, %v), want identity ses_abc with empty state", start, ok)
	}
	if !ValidActivityEvent(start) {
		t.Fatal("opencode session-start should be valid")
	}

	cases := []struct {
		event string
		state contract.ActivityState
	}{
		{"user-prompt-submit", contract.ActivityActive},
		{"active", contract.ActivityActive},
		{"permission-blocked", contract.ActivityWaitingInput},
		{"stop", contract.ActivityIdle},
	}
	for _, tc := range cases {
		event, ok := ActivityEventFromHook("opencode", tc.event, []byte(`{"session_id":"ses_abc"}`))
		if !ok {
			t.Fatalf("%s not accepted", tc.event)
		}
		if event.State != tc.state {
			t.Errorf("%s -> state %q, want %q", tc.event, event.State, tc.state)
		}
		if !ValidActivityEvent(event) {
			t.Errorf("%s should be a valid opencode activity event", tc.event)
		}
	}
}

func TestValidActivityEventAcceptsInteractiveSourceMarker(t *testing.T) {
	if !ValidActivityEvent(ActivityEvent{
		Harness:         "codex",
		Event:           "stop",
		State:           "idle",
		SourceInterface: "tui",
	}) {
		t.Fatal("interactive TUI source marker should be valid")
	}
}

func TestValidActivityEventRejectsUnknownSourceMarker(t *testing.T) {
	if ValidActivityEvent(ActivityEvent{
		Harness:         "codex",
		Event:           "stop",
		State:           "idle",
		SourceInterface: "chat",
	}) {
		t.Fatal("unknown source marker should be rejected")
	}
}

func TestCodexInterruptReportsIdle(t *testing.T) {
	event, ok := ActivityEventFromHook("codex", "interrupt", []byte(`{"turn_id":"turn-1"}`))
	if !ok || event.State != contract.ActivityIdle || !ValidActivityEvent(event) {
		t.Fatalf("Codex interrupt event = %+v, reported=%t; want valid idle activity", event, ok)
	}
}
