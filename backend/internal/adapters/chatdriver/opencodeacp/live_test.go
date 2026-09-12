package opencodeacp

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/opencode"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Run explicitly with AO_LIVE_OPENCODE_ACP=1. It uses the user's existing
// OpenCode executable, configuration, providers, and credentials; CI never
// depends on any of them.
func TestLiveOpenCodeACP(t *testing.T) {
	if os.Getenv("AO_LIVE_OPENCODE_ACP") != "1" {
		t.Skip("set AO_LIVE_OPENCODE_ACP=1 to run against the local OpenCode account")
	}

	driver := New(opencode.New(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if _, err := driver.Probe(ctx); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	workspace := t.TempDir()
	dataDir := t.TempDir()
	conversation, err := driver.Start(ctx, ports.ChatStartConfig{
		SessionID: "live-opencode-acp", DataDir: dataDir, WorkspacePath: workspace,
		Env: envMap(), SystemPrompt: "Answer in one short sentence.",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer conversation.(ports.ChatProviderTerminator).Terminate()

	ref, err := conversation.SendTurn(ctx, ports.ChatUserMessage{
		Text: "Reply with exactly: AO OpenCode ACP works", ClientMessageID: "live-1",
		Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("SendTurn: %v", err)
	}
	if err := conversation.(ports.ChatDeferredTurnStarter).StartDeferredTurn(ref.ProviderTurnID); err != nil {
		t.Fatalf("StartDeferredTurn: %v", err)
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
				if !strings.Contains(answer.String(), "AO OpenCode ACP works") {
					t.Fatalf("answer = %q", answer.String())
				}
				if acknowledger, ok := conversation.(ports.ChatProviderEventAcknowledger); ok {
					if err := acknowledger.AcknowledgeProviderEvent(context.Background(), event.ProviderEventID); err != nil {
						t.Fatalf("acknowledge turn: %v", err)
					}
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
