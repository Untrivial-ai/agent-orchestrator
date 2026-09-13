package qwenacp

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/qwen"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Run with AO_LIVE_QWEN_ACP=1. Pins the reason qwenacp declares approvals
// unavailable: under default (ask-me) permissions Qwen Code's ACP mode runs a
// shell command and completes the turn without ever raising a permission
// request. If a future Qwen build starts asking, this canary fails and the
// approvals capability should be reconsidered. CI never depends on this.
func TestLiveQwenACPAutoExecutesWithoutApproval(t *testing.T) {
	if os.Getenv("AO_LIVE_QWEN_ACP") != "1" {
		t.Skip("set AO_LIVE_QWEN_ACP=1 to run against the local Qwen Code account")
	}

	driver := New(qwen.New(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	conversation, err := driver.Start(ctx, ports.ChatStartConfig{
		SessionID: "live-qwen-acp-noask", DataDir: t.TempDir(), WorkspacePath: t.TempDir(),
		Env: envMap(), Permissions: ports.PermissionModeDefault,
		SystemPrompt: "You are in an automated test. Use your shell tool to do exactly what is asked.",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer conversation.Close()
	waitReady(ctx, t, conversation)

	ref, err := conversation.SendTurn(ctx, ports.ChatUserMessage{
		Text:            "Run the shell command `echo ao-qwen-noask` using your shell tool. Do nothing else.",
		ClientMessageID: "noask-1", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("SendTurn: %v", err)
	}
	if err := conversation.(ports.ChatDeferredTurnStarter).StartDeferredTurn(ref.ProviderTurnID); err != nil {
		t.Fatalf("StartDeferredTurn: %v", err)
	}

	ranTool := false
	for {
		select {
		case event, ok := <-conversation.Events():
			if !ok {
				t.Fatal("controller closed before completion")
			}
			if event.ProviderTurnID != "" && event.ProviderTurnID != ref.ProviderTurnID {
				continue
			}
			switch event.Kind {
			case ports.ChatEventApprovalRequested:
				t.Fatalf("Qwen ACP asked for approval; approvals capability may now be honored: %+v", event.Decisions)
			case ports.ChatEventActivityStarted:
				if event.ActivityKind == domain.ActivityKindCommand {
					ranTool = true
				}
			case ports.ChatEventTurnCompleted:
				if !ranTool {
					t.Fatalf("turn completed without running the shell tool; state=%q", event.TurnState)
				}
				return
			case ports.ChatEventError:
				t.Fatalf("turn error: %v", event.Err)
			}
		case <-ctx.Done():
			t.Fatalf("timed out: %v", ctx.Err())
		}
	}
}
