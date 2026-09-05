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
// resume, dynamic options/commands, AO's managed standing rule, Cursor's
// ordinary AGENTS.md rules, and a blocking Cursor extension request.
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
		Env: liveEnvMap(), Permissions: ports.PermissionModeDefault,
		SystemPrompt: "On every response include the exact token AO_STANDING_START.",
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
	startAnswer := waitForLiveTurn(ctx, t, conv, ref.ProviderTurnID, true, false)
	if !strings.Contains(startAnswer, "AO_STANDING_START") || !strings.Contains(startAnswer, "CURSOR_RULES_APPLIED") {
		t.Fatalf("start answer omitted standing/project rule tokens: %q", startAnswer)
	}
	proof, err := os.ReadFile(filepath.Join(workspace, "proof.txt"))
	if err != nil || string(proof) != "cursor-acp-ok" {
		t.Fatalf("tool-created proof = %q, %v", proof, err)
	}

	cancelRef := sendLiveTurn(ctx, t, conv,
		"Use the shell to run `sleep 30`, wait for it, then say finished.")
	waitForLiveTurn(ctx, t, conv, cancelRef.ProviderTurnID, true, true)
	if err := conv.(ports.ChatProviderTerminator).Terminate(); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	resumed, err := driver.Resume(ctx, ports.ChatResumeConfig{
		SessionID: "live-cursor-acp", ProviderConversationID: providerID,
		DataDir: dataDir, WorkspacePath: workspace, Env: liveEnvMap(),
		Permissions:  ports.PermissionModeDefault,
		SystemPrompt: "On every response include the exact token AO_STANDING_RESUME.",
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer resumed.(ports.ChatProviderTerminator).Terminate()
	resumeRef := sendLiveTurn(ctx, t, resumed,
		"Confirm the project rule token and whether proof.txt exists. Do not modify files.")
	answer := waitForLiveTurn(ctx, t, resumed, resumeRef.ProviderTurnID, false, false)
	if !strings.Contains(answer, "AO_STANDING_RESUME") || !strings.Contains(answer, "CURSOR_RULES_APPLIED") || !strings.Contains(answer, "proof.txt") {
		t.Fatalf("resumed answer did not apply project rules/history: %q", answer)
	}

	questionRef := sendLiveTurn(ctx, t, resumed,
		"Use your ask-question tool now. Ask exactly one multiple-choice question with choices alpha and beta, then report my choice.")
	waitForCursorInput(ctx, t, resumed, questionRef.ProviderTurnID)
	questionAnswer := waitForLiveTurn(ctx, t, resumed, questionRef.ProviderTurnID, false, false)
	if !strings.Contains(strings.ToLower(questionAnswer), "alpha") {
		t.Fatalf("answer after cursor/ask_question = %q, want selected alpha", questionAnswer)
	}
}

// Every AO permission mode must at least open and complete a native Cursor ACP
// turn. The contract tests assert exact flags/client policy; this live matrix
// catches provider-side flag drift. Run explicitly because it consumes account
// turns and may surface real permission prompts.
func TestLiveCursorACPPermissionModes(t *testing.T) {
	if os.Getenv("AO_LIVE_CURSOR_ACP") != "1" {
		t.Skip("set AO_LIVE_CURSOR_ACP=1 to run against the local Cursor account")
	}
	dataDir := liveDataDir(t)
	for _, test := range []struct {
		name string
		mode ports.PermissionMode
	}{
		{name: "default", mode: ports.PermissionModeDefault},
		{name: "accept-edits", mode: ports.PermissionModeAcceptEdits},
		{name: "auto", mode: ports.PermissionModeAuto},
		{name: "bypass-permissions", mode: ports.PermissionModeBypassPermissions},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			workspace := t.TempDir()
			driver := New(cursor.New(), nil)
			conv, err := driver.Start(ctx, ports.ChatStartConfig{
				SessionID: domain.SessionID("live-cursor-mode-" + test.name),
				DataDir:   dataDir, WorkspacePath: workspace, Env: liveEnvMap(), Permissions: test.mode,
			})
			if err != nil {
				t.Fatalf("Start(%s): %v", test.mode, err)
			}
			defer conv.(ports.ChatProviderTerminator).Terminate()
			ref := sendLiveTurnWithSettings(ctx, t, conv,
				"Use the file editing tool (not the shell) to create mode.txt containing ok, then say done.",
				ports.ChatTurnSettings{Approval: test.mode})
			waitForLiveTurn(ctx, t, conv, ref.ProviderTurnID, true, false)
			if content, err := os.ReadFile(filepath.Join(workspace, "mode.txt")); err != nil || strings.TrimSpace(string(content)) != "ok" {
				t.Fatalf("mode %s proof = %q, %v", test.mode, content, err)
			}
		})
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
	return sendLiveTurnWithSettings(ctx, t, conv, text, ports.ChatTurnSettings{})
}

func sendLiveTurnWithSettings(
	ctx context.Context,
	t *testing.T,
	conv ports.ChatConversation,
	text string,
	settings ports.ChatTurnSettings,
) ports.ChatTurnRef {
	t.Helper()
	ref, err := conv.SendTurn(ctx, ports.ChatUserMessage{
		Text: text, ClientMessageID: "live-" + time.Now().Format("150405.000000000"),
		Origin: domain.MessageOriginHuman, Settings: settings,
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
				if err := conv.(ports.ChatProviderEventAcknowledger).AcknowledgeProviderEvent(context.Background(), event.ProviderEventID); err != nil {
					t.Fatalf("acknowledge: %v", err)
				}
				return answer.String()
			}
		case <-ctx.Done():
			t.Fatalf("live turn timed out: %v; answer=%q", ctx.Err(), answer.String())
		}
	}
}

func waitForCursorInput(
	ctx context.Context,
	t *testing.T,
	conv ports.ChatConversation,
	turnID string,
) {
	t.Helper()
	for {
		select {
		case event, ok := <-conv.Events():
			if !ok {
				t.Fatal("controller closed before cursor/ask_question")
			}
			if event.ProviderTurnID != "" && event.ProviderTurnID != turnID {
				continue
			}
			if event.Kind == ports.ChatEventTurnCompleted {
				t.Fatal("turn completed without cursor/ask_question")
			}
			if event.Kind != ports.ChatEventInputRequested || event.Input == nil {
				continue
			}
			content := firstFormChoice(event.Input.Schema)
			if len(content) != 1 {
				t.Fatalf("cursor input schema = %#v", event.Input.Schema)
			}
			if err := conv.(ports.ChatInputResponder).ResolveInput(ctx, event.RequestID, ports.ChatInputResponse{
				Action: ports.ChatInputActionAccept, Content: content,
			}); err != nil {
				t.Fatalf("ResolveInput: %v", err)
			}
			return
		case <-ctx.Done():
			t.Fatalf("waiting for cursor/ask_question: %v", ctx.Err())
		}
	}
}

func firstFormChoice(schema map[string]any) map[string]any {
	properties, _ := schema["properties"].(map[string]any)
	for name, raw := range properties {
		property, _ := raw.(map[string]any)
		choices, _ := property["oneOf"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		if value, ok := choice["const"].(string); ok {
			return map[string]any{name: value}
		}
	}
	return nil
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

func liveDataDir(t *testing.T) string {
	t.Helper()
	if dataDir := strings.TrimSpace(os.Getenv("AO_DATA_DIR")); dataDir != "" {
		return dataDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home directory: %v", err)
	}
	return filepath.Join(home, ".ao")
}
