package claudeacp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/claudecode"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestMain(m *testing.M) {
	if len(os.Args) >= 9 && os.Args[1] == "chat-host" {
		if os.Args[5] != string(persistenthost.ProtocolACP) || os.Args[7] != "--" {
			os.Exit(2)
		}
		err := persistenthost.Run(context.Background(), persistenthost.Config{
			SessionID: os.Args[2], DataDir: os.Args[3], Workdir: os.Args[4],
			Protocol: persistenthost.ProtocolACP, OwnershipFingerprint: os.Args[6],
			Env: os.Environ(), Argv: os.Args[8:],
		})
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Run explicitly with AO_LIVE_CLAUDE_ACP=1. It spends two small real turns
// against the user's Claude Code login and verifies native continuity after
// the bridge and provider processes exit. CI needs no credentials or network.
func TestLiveClaudeACP(t *testing.T) {
	if os.Getenv("AO_LIVE_CLAUDE_ACP") != "1" {
		t.Skip("set AO_LIVE_CLAUDE_ACP=1 to run against the local Claude Code account")
	}

	driver := New(claudecode.New(), nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := driver.Probe(ctx); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	workspace := t.TempDir()
	dataDir := t.TempDir()
	t.Cleanup(func() { _ = persistenthost.Shutdown(context.Background(), dataDir, "live-claude-acp") })
	const marker = "AO-CLAUDE-HIBERNATE-7319"
	const standing = "This is an AO live hibernation check. Keep answers short."
	conversation, err := driver.Start(ctx, ports.ChatStartConfig{
		SessionID: domain.SessionID("live-claude-acp"), DataDir: dataDir, WorkspacePath: workspace,
		SystemPrompt: standing,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	answer := sendLiveTurn(ctx, t, conversation, "live-1", "Remember this marker for my next message: "+marker+". Reply only stored.")
	if strings.TrimSpace(answer) != "stored" {
		t.Fatalf("new-session answer = %q", answer)
	}
	providerID := conversation.ProviderConversationID()
	if err := conversation.(ports.ChatProviderHibernator).Hibernate(); err != nil {
		t.Fatalf("Hibernate fresh host: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "chat-hosts", "live-claude-acp", "host.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Claude ACP host survived hibernation: %v", err)
	}

	conversation, err = driver.Resume(ctx, ports.ChatResumeConfig{
		SessionID: domain.SessionID("live-claude-acp"), ProviderConversationID: providerID,
		DataDir: dataDir, WorkspacePath: workspace,
		SystemPrompt: standing,
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer conversation.(ports.ChatProviderTerminator).Terminate()
	if conversation.ProviderConversationID() != providerID || conversation.(ports.ChatLiveReconnector).ReconnectedLive() {
		t.Fatal("Claude ACP did not resume the same session in a replacement bridge process")
	}
	answer = sendLiveTurn(ctx, t, conversation, "live-2", "What exact marker did I ask you to remember before hibernation? Reply with only that marker.")
	if !strings.Contains(answer, marker) {
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
