package qwenacp

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/qwen"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Run with AO_LIVE_QWEN_ACP=1. Covers resume and interrupt against the local
// Qwen Code account. CI never depends on this.
func TestLiveQwenACPResumeAndInterrupt(t *testing.T) {
	if os.Getenv("AO_LIVE_QWEN_ACP") != "1" {
		t.Skip("set AO_LIVE_QWEN_ACP=1 to run against the local Qwen Code account")
	}

	driver := New(qwen.New(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	workspace := t.TempDir()
	conversation, err := driver.Start(ctx, ports.ChatStartConfig{
		SessionID: "live-qwen-acp-floor", DataDir: t.TempDir(), WorkspacePath: workspace,
		Env: envMap(), Permissions: ports.PermissionModeBypassPermissions,
		SystemPrompt: "Answer in one short sentence.",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitReady(ctx, t, conversation)

	token := "AO_QWEN_RESUME_TOKEN"
	answer := sendAndWait(ctx, t, conversation, "live-resume-1",
		"Reply with exactly: "+token, false)
	if !strings.Contains(answer, token) {
		t.Fatalf("seed answer = %q", answer)
	}
	providerID := conversation.ProviderConversationID()
	if providerID == "" {
		t.Fatal("empty provider conversation id")
	}
	if err := conversation.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	resumed, err := driver.Resume(ctx, ports.ChatResumeConfig{
		SessionID: "live-qwen-acp-floor", ProviderConversationID: providerID,
		DataDir: t.TempDir(), WorkspacePath: workspace, Env: envMap(),
		Permissions:  ports.PermissionModeBypassPermissions,
		SystemPrompt: "Answer in one short sentence.",
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer resumed.Close()
	waitReady(ctx, t, resumed)
	answer = sendAndWait(ctx, t, resumed, "live-resume-2",
		"What exact token did I ask you to reply with in the previous turn? Repeat it and nothing else.", false)
	if !strings.Contains(answer, token) {
		t.Fatalf("resumed answer = %q", answer)
	}

	sendAndWait(ctx, t, resumed, "live-interrupt-1",
		"Run the shell command `sleep 30` and wait until it finishes, then say done.", true)
}

func waitReady(ctx context.Context, t *testing.T, conv ports.ChatConversation) {
	t.Helper()
	for {
		select {
		case event, ok := <-conv.Events():
			if !ok {
				t.Fatal("controller closed before ready")
			}
			if event.Kind == ports.ChatEventControllerState && event.ControllerState == ports.ChatControllerReady {
				return
			}
			if event.Kind == ports.ChatEventError {
				t.Fatalf("controller error: %v", event.Err)
			}
		case <-ctx.Done():
			t.Fatalf("wait ready: %v", ctx.Err())
		}
	}
}

func sendAndWait(ctx context.Context, t *testing.T, conv ports.ChatConversation, id, prompt string, interrupt bool) string {
	t.Helper()
	ref, err := conv.SendTurn(ctx, ports.ChatUserMessage{
		Text: prompt, ClientMessageID: id, Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("SendTurn: %v", err)
	}
	if err := conv.(ports.ChatDeferredTurnStarter).StartDeferredTurn(ref.ProviderTurnID); err != nil {
		t.Fatalf("StartDeferredTurn: %v", err)
	}

	var answer strings.Builder
	interrupted := false
	var interruptAfter <-chan time.Time
	if interrupt {
		interruptAfter = time.After(2 * time.Second)
	}
	for {
		select {
		case <-interruptAfter:
			if interrupt && !interrupted {
				interrupted = true
				if err := conv.Interrupt(ctx, ref.ProviderTurnID); err != nil && !errors.Is(err, ports.ErrChatNoActiveTurn) {
					t.Fatalf("Interrupt: %v", err)
				}
			}
		case event, ok := <-conv.Events():
			if !ok {
				t.Fatalf("controller closed before completion; answer=%q", answer.String())
			}
			if event.ProviderTurnID != "" && event.ProviderTurnID != ref.ProviderTurnID {
				continue
			}
			switch event.Kind {
			case ports.ChatEventMessageDelta:
				answer.WriteString(event.Delta)
			case ports.ChatEventActivityStarted:
				if interrupt && !interrupted {
					interrupted = true
					if err := conv.Interrupt(ctx, ref.ProviderTurnID); err != nil {
						t.Fatalf("Interrupt: %v", err)
					}
				}
			case ports.ChatEventTurnCompleted:
				if interrupt && event.TurnState != domain.TurnStateInterrupted {
					t.Fatalf("cancelled turn state = %q; answer=%q", event.TurnState, answer.String())
				}
				if !interrupt && event.TurnState != domain.TurnStateCompleted {
					t.Fatalf("turn state = %q; answer=%q", event.TurnState, answer.String())
				}
				return answer.String()
			case ports.ChatEventError:
				t.Fatalf("turn error: %v; answer=%q", event.Err, answer.String())
			}
		case <-ctx.Done():
			t.Fatalf("turn timed out: %v; answer=%q", ctx.Err(), answer.String())
		}
	}
}
