package cursoracp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/cursor"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Run explicitly with AO_LIVE_CURSOR_ACP=1. It uses the user's existing Cursor
// executable, account, and AO-managed Cursor profile; CI never depends on them.
// The test exercises the native tool/approval path, cancellation, load-based
// resume, dynamic options/commands, and Cursor's ordinary AGENTS.md rules.
func TestLiveCursorACP(t *testing.T) {
	if os.Getenv("AO_LIVE_CURSOR_ACP") != "1" {
		t.Skip("set AO_LIVE_CURSOR_ACP=1 to run against the local Cursor account")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	driver := New(cursor.New(), nil)
	if _, err := driver.Probe(ctx); err != nil {
		t.Fatalf("Probe: %v", err)
	}

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte(
		"# Project rule\nWhen reporting success, include the exact token CURSOR_RULES_APPLIED.\n"), 0o600); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	dataDir := os.Getenv("AO_DATA_DIR")
	if strings.TrimSpace(dataDir) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("home directory: %v", err)
		}
		dataDir = filepath.Join(home, ".ao")
	}

	conv, err := driver.Start(ctx, ports.ChatStartConfig{
		SessionID: "live-cursor-acp", DataDir: dataDir, WorkspacePath: workspace,
		Env: liveEnvMap(),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	providerID := conv.ProviderConversationID()
	if providerID == "" || !conv.Capabilities()[ports.ChatCapabilityResume] {
		t.Fatalf("provider id/resume = %q, %#v", providerID, conv.Capabilities())
	}
	assertCursorAdvertisements(ctx, t, conv)

	ref := sendLiveTurn(ctx, t, conv,
		"Use the shell to run `printf cursor-acp-ok > proof.txt`, then report success.")
	waitForLiveTurn(ctx, t, conv, ref.ProviderTurnID, true, false)
	proof, err := os.ReadFile(filepath.Join(workspace, "proof.txt"))
	if err != nil || string(proof) != "cursor-acp-ok" {
		t.Fatalf("tool-created proof = %q, %v", proof, err)
	}

	cancelRef := sendLiveTurn(ctx, t, conv,
		"Use the shell to run `sleep 30`, wait for it, then say finished.")
	waitForLiveTurn(ctx, t, conv, cancelRef.ProviderTurnID, true, true)
	if err := conv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	resumed, err := driver.Resume(ctx, ports.ChatResumeConfig{
		SessionID: "live-cursor-acp", ProviderConversationID: providerID,
		DataDir: dataDir, WorkspacePath: workspace, Env: liveEnvMap(),
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer resumed.Close()
	resumeRef := sendLiveTurn(ctx, t, resumed,
		"Confirm the project rule token and whether proof.txt exists. Do not modify files.")
	answer := waitForLiveTurn(ctx, t, resumed, resumeRef.ProviderTurnID, false, false)
	if !strings.Contains(answer, "CURSOR_RULES_APPLIED") || !strings.Contains(answer, "proof.txt") {
		t.Fatalf("resumed answer did not apply project rules/history: %q", answer)
	}
}

func assertCursorAdvertisements(ctx context.Context, t *testing.T, conv ports.ChatConversation) {
	t.Helper()
	options, err := conv.(ports.ChatConfigOptionController).ListConfigOptions(ctx)
	if err != nil {
		t.Fatalf("ListConfigOptions: %v", err)
	}
	seenModel, seenMode := false, false
	for _, option := range options {
		seenModel = seenModel || option.ID == "model"
		seenMode = seenMode || option.ID == "mode"
	}
	if !seenModel || !seenMode {
		t.Fatalf("Cursor options = %#v, want advertised model and mode", options)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		skills, err := conv.(ports.ChatSkillLister).ListSkills(ctx)
		if err != nil {
			t.Fatalf("ListSkills: %v", err)
		}
		if len(skills) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Cursor advertised no slash commands")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func sendLiveTurn(ctx context.Context, t *testing.T, conv ports.ChatConversation, text string) ports.ChatTurnRef {
	t.Helper()
	ref, err := conv.SendTurn(ctx, ports.ChatUserMessage{
		Text: text, ClientMessageID: "live-" + time.Now().Format("150405.000000000"),
		Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("SendTurn: %v", err)
	}
	if err := conv.(ports.ChatDeferredTurnStarter).StartDeferredTurn(ref.ProviderTurnID); err != nil {
		t.Fatalf("StartDeferredTurn: %v", err)
	}
	return ref
}

func waitForLiveTurn(
	ctx context.Context,
	t *testing.T,
	conv ports.ChatConversation,
	turnID string,
	approve bool,
	interrupt bool,
) string {
	t.Helper()
	var answer strings.Builder
	interrupted := false
	for {
		select {
		case event, ok := <-conv.Events():
			if !ok {
				t.Fatalf("controller closed before turn completion; answer=%q", answer.String())
			}
			if event.ProviderTurnID != "" && event.ProviderTurnID != turnID {
				continue
			}
			switch event.Kind {
			case ports.ChatEventMessageDelta:
				answer.WriteString(event.Delta)
			case ports.ChatEventApprovalRequested:
				if !approve || len(event.Decisions) == 0 {
					t.Fatalf("unexpected/unanswerable approval: %#v", event)
				}
				decision := event.Decisions[0]
				for _, offered := range event.Decisions {
					var raw struct {
						Kind string `json:"kind"`
					}
					_ = json.Unmarshal(offered.Raw, &raw)
					if raw.Kind == "allow_once" {
						decision = offered
						break
					}
				}
				if err := conv.ResolveRequest(ctx, event.RequestID, ports.ChatDecision{ID: decision.ID}); err != nil {
					t.Fatalf("ResolveRequest: %v", err)
				}
				if interrupt && !interrupted {
					interrupted = true
					if err := conv.Interrupt(ctx, turnID); err != nil {
						t.Fatalf("Interrupt: %v", err)
					}
				}
			case ports.ChatEventTurnCompleted:
				if interrupt && event.TurnState != domain.TurnStateInterrupted {
					t.Fatalf("cancelled turn state = %q", event.TurnState)
				}
				if !interrupt && event.TurnState != domain.TurnStateCompleted {
					t.Fatalf("turn state = %q; answer=%q", event.TurnState, answer.String())
				}
				return answer.String()
			}
		case <-ctx.Done():
			t.Fatalf("live turn timed out: %v; answer=%q", ctx.Err(), answer.String())
		}
	}
}

func liveEnvMap() map[string]string {
	out := make(map[string]string)
	for _, pair := range os.Environ() {
		name, value, ok := strings.Cut(pair, "=")
		if ok {
			out[name] = value
		}
	}
	return out
}
