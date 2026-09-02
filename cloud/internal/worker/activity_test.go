package worker

import "testing"

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
