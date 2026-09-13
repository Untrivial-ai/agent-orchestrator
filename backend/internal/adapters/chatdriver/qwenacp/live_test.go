package qwenacp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/qwen"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Run explicitly with AO_LIVE_QWEN_ACP=1. It uses the user's existing Qwen Code
// executable, settings, models, and credentials; CI never depends on them.
func TestLiveQwenACP(t *testing.T) {
	if os.Getenv("AO_LIVE_QWEN_ACP") != "1" {
		t.Skip("set AO_LIVE_QWEN_ACP=1 to run against the local Qwen Code account")
	}

	driver := New(qwen.New(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if _, err := driver.Probe(ctx); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	conversation, err := driver.Start(ctx, ports.ChatStartConfig{
		SessionID: "live-qwen-acp", DataDir: t.TempDir(), WorkspacePath: t.TempDir(),
		Env: envMap(), Permissions: ports.PermissionModeBypassPermissions,
		SystemPrompt: "Answer in one short sentence.",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer conversation.Close()

	ref, err := conversation.SendTurn(ctx, ports.ChatUserMessage{
		Text: "Reply with exactly: AO Qwen ACP works", ClientMessageID: "live-1",
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
				if !strings.Contains(answer.String(), "AO Qwen ACP works") {
					t.Fatalf("answer = %q", answer.String())
				}
				return
			}
		case <-ctx.Done():
			t.Fatalf("live turn timed out: %v; answer=%q", ctx.Err(), answer.String())
		}
	}
}

func TestLiveQwenACPHandshake(t *testing.T) {
	if os.Getenv("AO_PROBE_QWEN_ACP") != "1" {
		t.Skip("set AO_PROBE_QWEN_ACP=1 to probe the local Qwen Code install")
	}

	driver := New(qwen.New(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if _, err := driver.Probe(ctx); err != nil {
		t.Fatalf("Probe: %v", err)
	}

	// Qwen ACP requires a selected auth type, not just OPENAI_API_KEY. A real
	// user gets that from `/auth`; the handshake uses a private QWEN_HOME so
	// we can open a session without writing the operator's ~/.qwen.
	qwenHome := t.TempDir()
	settings := []byte(`{"security":{"auth":{"selectedType":"openai"}},"env":{"OPENAI_API_KEY":"sk-test-ao-probe"}}`)
	if err := os.WriteFile(filepath.Join(qwenHome, "settings.json"), settings, 0o600); err != nil {
		t.Fatalf("write Qwen settings: %v", err)
	}
	env := envMap()
	env["QWEN_HOME"] = qwenHome
	env["OPENAI_API_KEY"] = "sk-test-ao-probe"

	conversation, err := driver.Start(ctx, ports.ChatStartConfig{
		SessionID: "live-qwen-acp-handshake", DataDir: t.TempDir(), WorkspacePath: t.TempDir(),
		Env: env, Permissions: ports.PermissionModeBypassPermissions,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer conversation.Close()

	for {
		select {
		case event, ok := <-conversation.Events():
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
			t.Fatalf("handshake timed out: %v", ctx.Err())
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
