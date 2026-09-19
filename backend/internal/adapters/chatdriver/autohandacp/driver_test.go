package autohandacp

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// The adapter beside the resolved `autohand` wins over any `autohand-acp` on
// PATH, so a second Autohand installation cannot be spliced into the session.
func TestResolveAdapterBinaryPrefersTheAdapterBesideAutohand(t *testing.T) {
	dir := t.TempDir()
	autohand := filepath.Join(dir, binaryName("autohand"))
	adapter := filepath.Join(dir, binaryName("autohand-acp"))
	writeExecutable(t, autohand)
	writeExecutable(t, adapter)
	pathDir := t.TempDir()
	writeExecutable(t, filepath.Join(pathDir, binaryName("autohand-acp")))
	t.Setenv("PATH", pathDir)

	got, err := resolveAdapterBinary(context.Background(), fixedPlugin(autohand))
	if err != nil {
		t.Fatalf("resolveAdapterBinary: %v", err)
	}
	if got != adapter {
		t.Fatalf("resolved = %q, want the sibling %q", got, adapter)
	}
}

func TestResolveAdapterBinaryFallsBackToPATH(t *testing.T) {
	dir := t.TempDir()
	autohand := filepath.Join(dir, binaryName("autohand"))
	writeExecutable(t, autohand)
	pathDir := t.TempDir()
	adapter := filepath.Join(pathDir, binaryName("autohand-acp"))
	writeExecutable(t, adapter)
	t.Setenv("PATH", pathDir)

	got, err := resolveAdapterBinary(context.Background(), fixedPlugin(autohand))
	if err != nil {
		t.Fatalf("resolveAdapterBinary: %v", err)
	}
	if got != adapter {
		t.Fatalf("resolved = %q, want %q", got, adapter)
	}
}

type fixedPlugin string

func (p fixedPlugin) ResolveBinary(context.Context) (string, error) { return string(p), nil }
func (fixedPlugin) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	return ports.AgentAuthStatusUnknown, nil
}

func binaryName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
