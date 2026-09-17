package qoder

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestHooksPreserveUserEntriesAndAreIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".qoder", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	before := `{"theme":"dark","hooks":{"Stop":[{"hooks":[{"type":"command","command":"user-stop"}]}]}}`
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	p := New()
	for range 2 {
		if err := p.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: dir}); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "user-stop") {
		t.Fatal("user hook removed")
	}
	if strings.Count(got, "ao hooks qoder stop") != 1 {
		t.Fatalf("managed stop count = %d", strings.Count(got, "ao hooks qoder stop"))
	}
	installed, err := p.AreHooksInstalled(context.Background(), dir)
	if err != nil || !installed {
		t.Fatalf("installed = %v, %v", installed, err)
	}
	if err := p.UninstallHooks(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "user-stop") || strings.Contains(string(data), "ao hooks qoder ") {
		t.Fatalf("uninstall result = %s", data)
	}
}
