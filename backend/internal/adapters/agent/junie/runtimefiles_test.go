package junie

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeFilesArePrivateAndFailClosed(t *testing.T) {
	dir := t.TempDir()
	files, err := NewRuntimeFileBuilder().Prepare(context.Background(), RuntimeFileRequest{DataDir: dir, SessionID: "ao-1", SystemPrompt: "standing"})
	if err != nil {
		t.Fatal(err)
	}
	guidelines, err := os.ReadFile(files.GuidelinesPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(guidelines) != "standing\n" {
		t.Fatalf("guidelines = %q", guidelines)
	}
	for _, path := range []string{files.ConfigPath, files.GuidelinesPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o", path, info.Mode().Perm())
		}
	}
	var cfg struct {
		Hooks map[string]json.RawMessage `json:"hooks"`
	}
	data, err := os.ReadFile(files.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, exists := cfg.Hooks["PermissionRequest"]; exists {
		t.Fatal("unsafe EAP permission observer must be absent")
	}
}

func TestRuntimeFilesRejectTraversalAndPreferInline(t *testing.T) {
	b := NewRuntimeFileBuilder()
	dir := t.TempDir()
	source := filepath.Join(dir, "source.md")
	if err := os.WriteFile(source, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Prepare(context.Background(), RuntimeFileRequest{DataDir: dir, SessionID: "../escape"}); err == nil {
		t.Fatal("expected traversal rejection")
	}
	f, err := b.Prepare(context.Background(), RuntimeFileRequest{DataDir: dir, SessionID: "safe", SystemPrompt: "inline", SystemPromptFile: source})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(f.GuidelinesPath)
	if string(data) != "inline\n" {
		t.Fatalf("guidelines = %q", data)
	}
}
