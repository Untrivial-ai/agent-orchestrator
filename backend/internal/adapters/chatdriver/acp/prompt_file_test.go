package acp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteSessionPromptWritesSessionPrivateFile(t *testing.T) {
	dataDir := t.TempDir()
	cfg := LaunchConfig{DataDir: dataDir, SessionID: "worker-1"}
	path, err := WriteSessionPrompt(context.Background(), cfg, "auggie-acp", "rules.md", "be brief")
	if err != nil {
		t.Fatalf("WriteSessionPrompt: %v", err)
	}
	want := filepath.Join(dataDir, "prompts", "worker-1", "auggie-acp", "rules.md")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != "be brief\n" {
		t.Fatalf("body = %q", body)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600: standing instructions are session-private", info.Mode().Perm())
	}
}

// Trailing newlines are normalized so a prompt that already ends in one does
// not accumulate blank lines across launches.
func TestWriteSessionPromptNormalizesTrailingNewlines(t *testing.T) {
	cfg := LaunchConfig{DataDir: t.TempDir(), SessionID: "worker-1"}
	path, err := WriteSessionPrompt(context.Background(), cfg, "pi-acp", "AGENTS.md", "be brief\n\n")
	if err != nil {
		t.Fatalf("WriteSessionPrompt: %v", err)
	}
	body, _ := os.ReadFile(path)
	if string(body) != "be brief\n" {
		t.Fatalf("body = %q", body)
	}
}

func TestWriteSessionPromptRequiresDataDir(t *testing.T) {
	_, err := WriteSessionPrompt(context.Background(), LaunchConfig{SessionID: "worker-1"},
		"auggie-acp", "rules.md", "be brief")
	if err == nil {
		t.Fatal("missing data directory must fail rather than write outside AO state")
	}
}

func TestWriteSessionPromptHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := WriteSessionPrompt(ctx, LaunchConfig{DataDir: t.TempDir(), SessionID: "worker-1"},
		"auggie-acp", "rules.md", "be brief")
	if err == nil {
		t.Fatal("cancelled context must abort before writing")
	}
}
