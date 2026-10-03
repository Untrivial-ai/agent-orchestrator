package workerexec

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

func TestCodexConversationSettingsUsesLastNativeTurnForBothInterfaces(t *testing.T) {
	dataDir := t.TempDir()
	id := "550e8400-e29b-41d4-a716-446655440000"
	path := filepath.Join(dataDir, "codex", "sessions", "2026", "10", "02", "rollout-2026-10-02T01-00-00-"+id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	rollout := `{"type":"turn_context","payload":{"model":"tui-model","effort":"high"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"agent_message","message":"done"}}` + "\n" +
		`{"type":"turn_context","payload":{"model":"chat-model","collaboration_mode":{"settings":{"reasoning_effort":"xhigh"}}}}` + "\n"
	if err := os.WriteFile(path, []byte(rollout), 0o600); err != nil {
		t.Fatal(err)
	}
	model, effort, err := CodexConversationSettings(dataDir, id)
	if err != nil {
		t.Fatal(err)
	}
	if model != "chat-model" || effort != "xhigh" {
		t.Fatalf("settings = %q/%q, want chat-model/xhigh", model, effort)
	}
	// The same selection must be applied to the interactive resume command.
	b := HarnessBuilder{DataDir: dataDir}
	command, err := b.BuildInteractive(worker.LaunchContext{
		SessionID: "ao-session", Harness: "codex", Mode: "trusted", Model: model,
		ReasoningEffort: effort, AgentSessionID: id,
	}, worker.CredentialResponse{Provider: "codex", CredentialType: "auth_json", Secret: `{}`}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if command.Cleanup != nil {
		defer command.Cleanup()
	}
	if !slices.Contains(command.Args, "chat-model") || !slices.Contains(command.Args, "model_reasoning_effort=xhigh") {
		t.Fatalf("interactive resume omitted native settings: %v", command.Args)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"type":"turn_context","payload":{"model":"new-tui-model","effort":"low"}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	model, effort, err = CodexConversationSettings(dataDir, id)
	if err != nil {
		t.Fatal(err)
	}
	if model != "new-tui-model" || effort != "low" {
		t.Fatalf("TUI settings = %q/%q, want new-tui-model/low", model, effort)
	}
}

func TestCodexConversationSettingsDoesNotReadUnrelatedRollout(t *testing.T) {
	model, effort, err := CodexConversationSettings(t.TempDir(), "../other-session")
	if err != nil || model != "" || effort != "" {
		t.Fatalf("settings = %q/%q, %v", model, effort, err)
	}
}

func TestCodexConversationSettingsAfterKeepsPendingSelectionUntilNewNativeTurn(t *testing.T) {
	dataDir := t.TempDir()
	id := "550e8400-e29b-41d4-a716-446655440000"
	path := filepath.Join(dataDir, "codex", "sessions", "rollout-2026-10-02T01-00-00-"+id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	before := `{"timestamp":"2026-10-02T01:00:00Z","type":"turn_context","payload":{"model":"old-model","effort":"low"}}` + "\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	selectedAt := time.Date(2026, 10, 2, 1, 1, 0, 0, time.UTC)
	model, effort, err := CodexConversationSettingsAfter(dataDir, id, selectedAt)
	if err != nil || model != "" || effort != "" {
		t.Fatalf("older native settings = %q/%q, %v", model, effort, err)
	}
	after := `{"timestamp":"2026-10-02T01:02:00Z","type":"turn_context","payload":{"model":"new-tui-model","effort":"high"}}` + "\n"
	if err := os.WriteFile(path, []byte(before+after), 0o600); err != nil {
		t.Fatal(err)
	}
	model, effort, err = CodexConversationSettingsAfter(dataDir, id, selectedAt)
	if err != nil || model != "new-tui-model" || effort != "high" {
		t.Fatalf("newer native settings = %q/%q, %v", model, effort, err)
	}
}
