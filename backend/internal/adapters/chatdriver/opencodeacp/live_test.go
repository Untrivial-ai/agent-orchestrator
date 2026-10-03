package opencodeacp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/opencode"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/processalive"
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

// Run explicitly with AO_LIVE_OPENCODE_ACP=1. It uses the user's existing
// OpenCode executable, configuration, providers, and credentials; CI never
// depends on any of them. AO_LIVE_OPENCODE_ACP_MODEL can select an available
// provider/model when the configured default cannot serve requests.
func TestLiveOpenCodeACP(t *testing.T) {
	if os.Getenv("AO_LIVE_OPENCODE_ACP") != "1" {
		t.Skip("set AO_LIVE_OPENCODE_ACP=1 to run against the local OpenCode account")
	}

	driver := New(opencode.New(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if _, err := driver.Probe(ctx); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	workspace := t.TempDir()
	dataDir := t.TempDir()
	const sessionID = "live-opencode-acp"
	const marker = "AO-OPENCODE-HIBERNATE-7319"
	const systemPrompt = "Answer in one short sentence."
	model := os.Getenv("AO_LIVE_OPENCODE_ACP_MODEL")
	t.Cleanup(func() { _ = persistenthost.Shutdown(context.Background(), dataDir, sessionID) })
	conversation, err := driver.Start(ctx, ports.ChatStartConfig{
		SessionID: sessionID, DataDir: dataDir, WorkspacePath: workspace,
		Model: model, Env: envMap(), SystemPrompt: systemPrompt,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer conversation.(ports.ChatProviderTerminator).Terminate()
	if !conversation.Capabilities()[ports.ChatCapabilityResume] {
		t.Fatal("OpenCode ACP did not advertise native resume")
	}
	providerID := conversation.ProviderConversationID()
	if providerID == "" {
		t.Fatal("OpenCode ACP did not return a native conversation ID")
	}
	descriptorPath := filepath.Join(dataDir, "chat-hosts", sessionID, "host.json")
	firstHost := readLiveHost(t, descriptorPath)
	if !processalive.Alive(firstHost.PID) {
		t.Fatalf("OpenCode ACP host process %d was not running before hibernation", firstHost.PID)
	}

	answer, contextUsed, contextWindow := sendLiveOpenCodeTurn(ctx, t, conversation, "live-1",
		"Remember this marker for my next message: "+marker+". Reply with exactly: AO OpenCode ACP works")
	if !strings.Contains(answer, "AO OpenCode ACP works") {
		t.Fatalf("first answer = %q", answer)
	}
	if contextUsed <= 0 || contextWindow <= 0 {
		t.Fatalf("OpenCode did not report nonzero context usage: used=%d size=%d", contextUsed, contextWindow)
	}
	t.Logf("OpenCode ACP context: %d / %d tokens", contextUsed, contextWindow)

	if err := conversation.(ports.ChatProviderHibernator).Hibernate(); err != nil {
		t.Fatalf("Hibernate: %v", err)
	}
	if _, err := os.Stat(descriptorPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenCode ACP host survived hibernation: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for processalive.Alive(firstHost.PID) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if processalive.Alive(firstHost.PID) {
		t.Fatalf("OpenCode ACP host process %d survived hibernation", firstHost.PID)
	}

	conversation, err = driver.Resume(ctx, ports.ChatResumeConfig{
		SessionID: sessionID, ProviderConversationID: providerID, DataDir: dataDir,
		WorkspacePath: workspace, Model: model, Env: envMap(), SystemPrompt: systemPrompt,
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer conversation.(ports.ChatProviderTerminator).Terminate()
	if conversation.ProviderConversationID() != providerID || conversation.(ports.ChatLiveReconnector).ReconnectedLive() {
		t.Fatal("OpenCode ACP did not native-resume the same conversation in a replacement process")
	}
	secondHost := readLiveHost(t, descriptorPath)
	if secondHost.PID == firstHost.PID || !processalive.Alive(secondHost.PID) {
		t.Fatalf("replacement host process = %d; first = %d", secondHost.PID, firstHost.PID)
	}
	answer, _, _ = sendLiveOpenCodeTurn(ctx, t, conversation, "live-2",
		"What exact marker did I ask you to remember before hibernation? Reply with only that marker.")
	if !strings.Contains(answer, marker) {
		t.Fatalf("resumed conversation lost earlier context: answer = %q, want %q", answer, marker)
	}
}

func readLiveHost(t *testing.T, path string) persistenthost.Descriptor {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read OpenCode ACP host descriptor: %v", err)
	}
	var host persistenthost.Descriptor
	if err := json.Unmarshal(raw, &host); err != nil || host.PID <= 0 {
		t.Fatalf("decode OpenCode ACP host PID: %d, %v", host.PID, err)
	}
	return host
}

func sendLiveOpenCodeTurn(ctx context.Context, t *testing.T, conversation ports.ChatConversation, clientID, prompt string) (string, int64, int64) {
	t.Helper()
	ref, err := conversation.SendTurn(ctx, ports.ChatUserMessage{
		Text: prompt, ClientMessageID: clientID,
		Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("SendTurn: %v", err)
	}
	if err := conversation.(ports.ChatDeferredTurnStarter).StartDeferredTurn(ref.ProviderTurnID); err != nil {
		t.Fatalf("StartDeferredTurn: %v", err)
	}

	var answer strings.Builder
	var contextUsed, contextWindow int64
	var providerErr error
	for {
		select {
		case event, ok := <-conversation.Events():
			if !ok {
				t.Fatalf("controller closed before completion; answer=%q", answer.String())
			}
			if event.Err != nil {
				providerErr = event.Err
			}
			switch event.Kind {
			case ports.ChatEventMessageDelta:
				answer.WriteString(event.Delta)
			case ports.ChatEventUsage:
				if event.Usage != nil && event.Usage.ContextKnown {
					contextUsed, contextWindow = event.Usage.ContextUsed, event.Usage.ContextWindow
				}
			case ports.ChatEventTurnCompleted:
				if event.TurnState != domain.TurnStateCompleted {
					t.Fatalf("turn state = %q; answer=%q; error=%v", event.TurnState, answer.String(), providerErr)
				}
				if acknowledger, ok := conversation.(ports.ChatProviderEventAcknowledger); ok {
					if err := acknowledger.AcknowledgeProviderEvent(context.Background(), event.ProviderEventID); err != nil {
						t.Fatalf("acknowledge turn: %v", err)
					}
				}
				return answer.String(), contextUsed, contextWindow
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
