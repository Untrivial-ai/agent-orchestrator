package ompacp

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/omp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestMain(m *testing.M) {
	if len(os.Args) >= 8 && os.Args[1] == "chat-host" {
		if os.Args[5] != string(persistenthost.ProtocolACP) || os.Args[7] != "--" {
			os.Exit(2)
		}
		err := persistenthost.Run(context.Background(), persistenthost.Config{
			SessionID: os.Args[2], DataDir: os.Args[3], Workdir: os.Args[4],
			Env: os.Environ(), Argv: os.Args[8:], Protocol: persistenthost.ProtocolACP,
			OwnershipFingerprint: os.Args[6],
		})
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Run explicitly with AO_LIVE_OMP_ACP=1. It uses the user's existing OMP
// executable, settings, models, and credentials; CI never depends on them.
func TestLiveOMPACP(t *testing.T) {
	if os.Getenv("AO_LIVE_OMP_ACP") != "1" {
		t.Skip("set AO_LIVE_OMP_ACP=1 to run against the local OMP account")
	}

	driver := New(omp.New(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if _, err := driver.Probe(ctx); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	dataDir := t.TempDir()
	workspace := t.TempDir()
	conversation, err := driver.Start(ctx, ports.ChatStartConfig{
		SessionID: "live-omp-acp", DataDir: dataDir, WorkspacePath: workspace,
		Env: envMap(), Permissions: ports.PermissionModeBypassPermissions,
		Model: os.Getenv("AO_LIVE_OMP_ACP_MODEL"), SystemPrompt: "Answer in one short sentence.",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer conversation.(ports.ChatProviderTerminator).Terminate()

	ref, err := conversation.SendTurn(ctx, ports.ChatUserMessage{
		Text: "Remember the exact token AO_OMP_RESUME_731 and reply only READY.", ClientMessageID: "live-1",
		Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("SendTurn: %v", err)
	}
	if err := conversation.(ports.ChatDeferredTurnStarter).StartDeferredTurn(ref.ProviderTurnID); err != nil {
		t.Fatalf("StartDeferredTurn: %v", err)
	}

	var answer strings.Builder

firstTurn:
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
				if !strings.Contains(answer.String(), "READY") {
					t.Fatalf("answer = %q", answer.String())
				}
				break firstTurn
			}
		case <-ctx.Done():
			t.Fatalf("live turn timed out: %v; answer=%q", ctx.Err(), answer.String())
		}
	}
	providerID := conversation.ProviderConversationID()
	if err := conversation.(ports.ChatProviderTerminator).Terminate(); err != nil {
		t.Fatalf("Terminate fresh host: %v", err)
	}
	conversation, err = driver.Resume(ctx, ports.ChatResumeConfig{
		SessionID: "live-omp-acp", ProviderConversationID: providerID,
		DataDir: dataDir, WorkspacePath: workspace, Env: envMap(),
		Permissions: ports.PermissionModeBypassPermissions, AllowResumeWithoutHistory: true,
	})
	if err != nil {
		t.Fatalf("Resume after fresh provider process: %v", err)
	}
	defer conversation.(ports.ChatProviderTerminator).Terminate()
	ref, err = conversation.SendTurn(ctx, ports.ChatUserMessage{
		Text:            "What exact token did I ask you to remember? Reply with the token only.",
		ClientMessageID: "live-2", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("SendTurn after Resume: %v", err)
	}
	if err := conversation.(ports.ChatDeferredTurnStarter).StartDeferredTurn(ref.ProviderTurnID); err != nil {
		t.Fatalf("StartDeferredTurn after Resume: %v", err)
	}
	var resumedAnswer strings.Builder
	for {
		select {
		case event, ok := <-conversation.Events():
			if !ok {
				t.Fatalf("resumed controller closed before completion; answer=%q", resumedAnswer.String())
			}
			if event.Kind == ports.ChatEventMessageDelta {
				resumedAnswer.WriteString(event.Delta)
			}
			if event.Kind == ports.ChatEventTurnCompleted {
				if event.TurnState != domain.TurnStateCompleted || !strings.Contains(resumedAnswer.String(), "AO_OMP_RESUME_731") {
					t.Fatalf("resumed turn state=%q answer=%q", event.TurnState, resumedAnswer.String())
				}
				return
			}
		case <-ctx.Done():
			t.Fatalf("resumed live turn timed out: %v; answer=%q", ctx.Err(), resumedAnswer.String())
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
