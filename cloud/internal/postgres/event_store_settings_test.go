package postgres

import (
	"encoding/json"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

func TestChatMessagePayloadKeepsLegacyIdempotencyShapeWithoutSettings(t *testing.T) {
	without, err := json.Marshal(chatMessagePayload{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if string(without) != `{"text":"hello"}` {
		t.Fatalf("default payload = %s", without)
	}
	with, err := json.Marshal(chatMessagePayload{
		Text: "hello", ChatTurnSettings: domain.ChatTurnSettings{Model: "codex-test", ReasoningEffort: "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(with) != `{"text":"hello","model":"codex-test","reasoningEffort":"high"}` {
		t.Fatalf("selected payload = %s", with)
	}
}
