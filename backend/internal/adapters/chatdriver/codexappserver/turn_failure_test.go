package codexappserver

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestNativeTurnFailureExplanations(t *testing.T) {
	const missingError = "Codex reported that the turn failed without providing an error message."
	for _, tt := range []struct {
		name     string
		fields   string
		state    domain.TurnState
		message  string
		fallback bool
	}{
		{"missing error", `"status":"failed"`, domain.TurnStateFailed, missingError, true},
		{"null error", `"status":"failed","error":null`, domain.TurnStateFailed, missingError, true},
		{"empty error", `"status":"failed","error":{"message":""}`, domain.TurnStateFailed, missingError, true},
		{"whitespace error", `"status":"failed","error":{"message":" \t\n"}`, domain.TurnStateFailed, missingError, true},
		{"provider error", `"status":"failed","error":{"message":" quota exhausted "}`, domain.TurnStateFailed, " quota exhausted ", false},
		{"unknown status", `"status":"new-status"`, domain.TurnStateFailed, `Codex ended the turn with an unrecognized status "new-status" and no error message.`, true},
		{"absent status", `"items":[]`, domain.TurnStateFailed, "Codex ended the turn without reporting a status or an error message.", true},
		{"blank status", `"status":""`, domain.TurnStateFailed, "Codex ended the turn without reporting a status or an error message.", true},
		{"unknown status with error", `"status":"new-status","error":{"message":"provider detail"}`, domain.TurnStateFailed, "provider detail", false},
		{"completed", `"status":"completed"`, domain.TurnStateCompleted, "", false},
		{"interrupted", `"status":"interrupted"`, domain.TurnStateInterrupted, "", false},
		{"cancelled", `"status":"cancelled"`, domain.TurnStateInterrupted, "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			turn := `{"id":"turn-1",` + tt.fields + `}`
			live := normalizeOne(t, "turn/completed", `{"threadId":"thread-1","turn":`+turn+`}`)
			conv, srv := openConversation(t)
			srv.reply("thread/read", `{"thread":{"id":"thread-1","turns":[`+turn+`]}}`)
			history, err := conv.ReadHistory(context.Background())
			if err != nil {
				t.Fatalf("ReadHistory: %v", err)
			}
			if len(history) != 2 || history[1].ProviderEventID == "" {
				t.Fatalf("history = %+v, want start and identified completion only", history)
			}
			for _, event := range []ports.ChatEvent{live, history[1]} {
				if event.Kind != ports.ChatEventTurnCompleted || event.TurnState != tt.state || event.ProviderTurnID != "turn-1" {
					t.Fatalf("completion = %+v, want state %s for turn-1", event, tt.state)
				}
				message := ""
				if event.Err != nil {
					message = event.Err.Error()
				}
				if message != tt.message {
					t.Errorf("error = %q, want %q", message, tt.message)
				}
				var fallback interface{ ChatFailureFallback() bool }
				if got := errors.As(event.Err, &fallback) && fallback.ChatFailureFallback(); got != tt.fallback {
					t.Errorf("fallback = %v, want %v", got, tt.fallback)
				}
			}
		})
	}
}
