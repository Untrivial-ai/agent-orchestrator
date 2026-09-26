package agentbase

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestProviderHomeDirPrecedence(t *testing.T) {
	t.Setenv("AGENT_HOME", " /daemon ")
	if got, err := ProviderHomeDir(map[string]string{"AGENT_HOME": " /session "}, "AGENT_HOME", ".agent"); err != nil || got != "/session" {
		t.Fatalf("session home = %q, %v", got, err)
	}
	if got, err := ProviderHomeDir(nil, "AGENT_HOME", ".agent"); err != nil || got != "/daemon" {
		t.Fatalf("daemon home = %q, %v", got, err)
	}
}

func TestTranscriptInProjectsRequiresNonEmptyRegularFile(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project", "chats")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(project, "prefix-session.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := TranscriptInProjects(context.Background(), root, filepath.Join("chats", "*-session.jsonl")); err != nil || ok {
		t.Fatalf("empty transcript = %v, %v", ok, err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := TranscriptInProjects(context.Background(), root, filepath.Join("chats", "*-session.jsonl")); err != nil || !ok {
		t.Fatalf("persisted transcript = %v, %v", ok, err)
	}
}

func TestTranscriptInProjectsFindsExactRelativePath(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project", "chats")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "session.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := TranscriptInProjects(context.Background(), root, filepath.Join("chats", "session.jsonl")); err != nil || !ok {
		t.Fatalf("exact transcript = %v, %v", ok, err)
	}
}

func TestTranscriptInProjectsHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := TranscriptInProjects(ctx, t.TempDir(), "session.jsonl"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestHookOrProviderConversationID(t *testing.T) {
	session := ports.SessionRef{Metadata: map[string]string{ports.MetadataKeyAgentSessionID: " hook-id "}}
	if id, ok, err := HookOrProviderConversationID(context.Background(), session, domain.SessionModeTUI, "provider-id"); err != nil || !ok || id != "hook-id" {
		t.Fatalf("TUI id = %q, %v, %v", id, ok, err)
	}
	if id, ok, err := HookOrProviderConversationID(context.Background(), session, domain.SessionModeChat, " provider-id "); err != nil || !ok || id != "provider-id" {
		t.Fatalf("Chat id = %q, %v, %v", id, ok, err)
	}
}

func TestModelConfigSpecHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ModelConfigSpec(ctx, "model"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ModelConfigSpec error = %v, want context canceled", err)
	}
}

func TestModelConfigSpecAndFlag(t *testing.T) {
	spec, err := ModelConfigSpec(context.Background(), "Model override.")
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Fields) != 1 || spec.Fields[0].Key != "model" || spec.Fields[0].Description != "Model override." {
		t.Fatalf("spec = %#v", spec)
	}

	cmd := []string{"agent"}
	AppendModelFlag(&cmd, ports.AgentConfig{Model: "  provider/model  "}, "--model")
	if want := []string{"agent", "--model", "provider/model"}; !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %q, want %q", cmd, want)
	}
	AppendModelFlag(&cmd, ports.AgentConfig{Model: "  "}, "--model")
	if len(cmd) != 3 {
		t.Fatalf("blank model changed cmd: %q", cmd)
	}
}
