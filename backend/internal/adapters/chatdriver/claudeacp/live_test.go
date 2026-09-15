package claudeacp

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/claudecode"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Run explicitly with AO_LIVE_CLAUDE_ACP=1. It spends two very small real turns
// against the user's existing Claude Code login and proves the complete boundary,
// including standing-context replacement on resume: packaged Node ->
// claude-agent-acp -> user-installed Claude -> normalized AO events. CI never
// depends on credentials or the network.
func TestLiveClaudeACP(t *testing.T) {
	if os.Getenv("AO_LIVE_CLAUDE_ACP") != "1" {
		t.Skip("set AO_LIVE_CLAUDE_ACP=1 to run against the local Claude Code account")
	}

	driver := New(claudecode.New(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := driver.Probe(ctx); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	workspace := t.TempDir()
	dataDir := t.TempDir()
	conversation, err := driver.Start(ctx, ports.ChatStartConfig{
		SessionID: domain.SessionID("live-claude-acp"), DataDir: dataDir, WorkspacePath: workspace,
		SystemPrompt: "For this live integration test, your role name is AO ACP standing context works. " +
			"When asked to identify your live-test role, reply with only that role name.",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	answer := sendLiveTurn(ctx, t, conversation, "live-1", "Identify your live-test role.")
	if strings.TrimSpace(answer) != "AO ACP standing context works" {
		t.Fatalf("new-session answer = %q", answer)
	}
	providerID := conversation.ProviderConversationID()
	if err := conversation.(ports.ChatProviderTerminator).Terminate(); err != nil {
		t.Fatalf("Terminate fresh host: %v", err)
	}

	conversation, err = driver.Resume(ctx, ports.ChatResumeConfig{
		SessionID: domain.SessionID("live-claude-acp"), ProviderConversationID: providerID,
		DataDir: dataDir, WorkspacePath: workspace,
		SystemPrompt: "For this resumed live integration test, your role name is AO ACP resumed context works. " +
			"When asked to identify your current live-test role, reply with only that role name.",
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer conversation.(ports.ChatProviderTerminator).Terminate()
	answer = sendLiveTurn(ctx, t, conversation, "live-2", "Identify your current live-test role.")
	if strings.TrimSpace(answer) != "AO ACP resumed context works" {
		t.Fatalf("resumed-session answer = %q", answer)
	}
}

func sendLiveTurn(
	ctx context.Context,
	t *testing.T,
	conversation ports.ChatConversation,
	clientMessageID, prompt string,
) string {
	t.Helper()
	ref, err := conversation.SendTurn(ctx, ports.ChatUserMessage{
		Text: prompt, ClientMessageID: clientMessageID, Origin: domain.MessageOriginHuman,
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
			if event.Kind == ports.ChatEventMessageDelta {
				answer.WriteString(event.Delta)
			}
			if event.Kind == ports.ChatEventTurnCompleted {
				if event.TurnState != domain.TurnStateCompleted {
					t.Fatalf("turn state = %q; answer=%q", event.TurnState, answer.String())
				}
				if acknowledger, ok := conversation.(ports.ChatProviderEventAcknowledger); ok {
					if err := acknowledger.AcknowledgeProviderEvent(context.Background(), event.ProviderEventID); err != nil {
						t.Fatalf("acknowledge turn: %v", err)
					}
				}
				return answer.String()
			}
		case <-ctx.Done():
			t.Fatalf("live turn timed out: %v; answer=%q", ctx.Err(), answer.String())
		}
	}
}
