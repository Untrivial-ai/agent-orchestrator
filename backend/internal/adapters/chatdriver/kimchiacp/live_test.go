package kimchiacp

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/kimchi"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Run explicitly with AO_LIVE_KIMCHI_ACP=1. It uses the user's existing Kimchi
// executable, settings, and account; CI never depends on any of them.
func TestLiveKimchiACP(t *testing.T) {
	if os.Getenv("AO_LIVE_KIMCHI_ACP") != "1" {
		t.Skip("set AO_LIVE_KIMCHI_ACP=1 to run against the local Kimchi account")
	}

	driver := New(kimchi.New(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Probe: version check + auth status.
	if _, err := driver.Probe(ctx); err != nil {
		t.Fatalf("Probe: %v", err)
	}

	// Start a fresh session (no resume — Kimchi does not advertise session/resume).
	conversation, err := driver.Start(ctx, ports.ChatStartConfig{
		SessionID:     "live-kimchi-acp",
		DataDir:       t.TempDir(),
		WorkspacePath: t.TempDir(),
		Env:           envMap(),
		SystemPrompt:  "Answer in one short sentence.",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer conversation.Close()

	// Send a turn and verify streaming + completion.
	ref, err := conversation.SendTurn(ctx, ports.ChatUserMessage{
		Text:            "Reply with exactly: AO Kimchi ACP works",
		ClientMessageID: "live-1",
		Origin:          domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("SendTurn: %v", err)
	}
	if starter, ok := conversation.(ports.ChatDeferredTurnStarter); ok {
		if err := starter.StartDeferredTurn(ref.ProviderTurnID); err != nil {
			t.Fatalf("StartDeferredTurn: %v", err)
		}
	}

	var answer strings.Builder
	for {
		select {
		case event, ok := <-conversation.Events():
			if !ok {
				t.Fatalf("controller closed before completion; answer=%q", answer.String())
			}
			switch event.Kind {
			case ports.ChatEventMessageDelta:
				answer.WriteString(event.Delta)
			case ports.ChatEventTurnCompleted:
				if event.TurnState != domain.TurnStateCompleted {
					t.Fatalf("turn state = %q; answer=%q", event.TurnState, answer.String())
				}
				if !strings.Contains(answer.String(), "AO Kimchi ACP works") {
					t.Fatalf("answer = %q", answer.String())
				}
				return
			}
		case <-ctx.Done():
			t.Fatalf("live turn timed out: %v; answer=%q", ctx.Err(), answer.String())
		}
	}
}

func envMap() map[string]string {
	out := make(map[string]string)
	for _, pair := range os.Environ() {
		name, value, ok := strings.Cut(pair, "=")
		if ok {
			out[name] = value
		}
	}
	return out
}
