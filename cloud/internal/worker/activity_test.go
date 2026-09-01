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
