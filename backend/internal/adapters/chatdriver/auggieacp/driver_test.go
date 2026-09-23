package auggieacp

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
)

func TestConfigureStartsACPMode(t *testing.T) {
	args, env, err := configure(context.Background(), acpdriver.LaunchConfig{})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	want := []string{"--acp"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	if env != nil {
		t.Fatalf("env = %#v, want nil", env)
	}
}

func TestConfigurePassesModel(t *testing.T) {
	args, _, err := configure(context.Background(), acpdriver.LaunchConfig{Model: "claude-sonnet-4"})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	want := []string{"--acp", "--model", "claude-sonnet-4"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestConfigureWritesStandingRulesFile(t *testing.T) {
	dataDir := t.TempDir()
	args, env, err := configure(context.Background(), acpdriver.LaunchConfig{
		SessionID: "worker-1", DataDir: dataDir, SystemPrompt: "Follow AO worker rules.",
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	wantPath := filepath.Join(dataDir, "prompts", "worker-1", "auggie-acp", "ao-standing-rules.md")
	want := []string{"--acp", "--rules", wantPath}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	if env != nil {
		t.Fatalf("env = %#v, want nil", env)
	}
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read standing rules: %v", err)
	}
	if got := string(data); got != "Follow AO worker rules.\n" {
		t.Fatalf("standing rules = %q", got)
	}
}

func TestConfigureOmitsBlankSystemPrompt(t *testing.T) {
	args, _, err := configure(context.Background(), acpdriver.LaunchConfig{SystemPrompt: "   "})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	want := []string{"--acp"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestConfigureRequiresDataDirForStandingRules(t *testing.T) {
	_, _, err := configure(context.Background(), acpdriver.LaunchConfig{SystemPrompt: "Follow AO rules."})
	if err == nil {
		t.Fatal("configure succeeded without AO data directory")
	}
	if !strings.Contains(err.Error(), "data directory") {
		t.Fatalf("error = %v, want data directory guidance", err)
	}
}
