package domain

import (
	"encoding/json"
	"testing"
)

func TestProjectSettingsAgentConfigForSessionUsesRoleDefaults(t *testing.T) {
	settings := ParseProjectSettings(json.RawMessage(`{
		"worker":{"agent":"codex","agentConfig":{"model":"gpt-5","effort":"high","permissions":"auto"}},
		"orchestrator":{"agent":"claude-code","agentConfig":{"model":"claude-sonnet","permissions":"accept-edits"}}
	}`))

	if got := settings.AgentConfigForSession("worker"); got.Model != "gpt-5" || got.Effort != "high" || got.Permissions != "auto" {
		t.Fatalf("worker config = %+v", got)
	}
	if got := settings.AgentConfigForSession("orchestrator"); got.Model != "claude-sonnet" || got.Permissions != "accept-edits" {
		t.Fatalf("orchestrator config = %+v", got)
	}
}

func TestProjectSettingsIgnoresMalformedConfig(t *testing.T) {
	if got := ParseProjectSettings(json.RawMessage(`not-json`)).AgentConfigForSession("worker"); got != (AgentConfig{}) {
		t.Fatalf("malformed config = %+v, want empty defaults", got)
	}
}

func TestProjectSettingsReadsAutoReview(t *testing.T) {
	if !ParseProjectSettings(json.RawMessage(`{"autoReview":true}`)).AutoReview {
		t.Fatal("autoReview true was not read")
	}
	if ParseProjectSettings(json.RawMessage(`{"autoReview":false}`)).AutoReview {
		t.Fatal("autoReview false was not read")
	}
}
