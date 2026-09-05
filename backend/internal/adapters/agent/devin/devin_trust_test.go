package devin

import (
	"os"
	"path/filepath"
	"testing"
)

// A zero-byte ~/.claude.json (caught mid-truncate) must NOT be overwritten.
// Sibling of claudecode #3746 / PR #3756: ensureDevinWorkspaceTrusted acts on
// the same ~/.claude.json and had the same unguarded zero-byte read, which
// would replace the user's real config (oauthAccount, projects history) with
// one containing only the trust entry.
func TestEnsureDevinWorkspaceTrustedRefusesZeroByteConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, ".claude.json")
	if err := os.WriteFile(cfg, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureDevinWorkspaceTrusted(cfg, "/some/worktree"); err == nil {
		t.Fatal("expected error for zero-byte config, got nil (config would be overwritten)")
	}
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("zero-byte config must not be overwritten; got %d bytes: %s", len(data), data)
	}
}

// Same guard for devin's own trusted_workspaces.json.
func TestEnsureDevinNativeWorkspaceTrustedRefusesZeroByteConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "trusted_workspaces.json")
	if err := os.WriteFile(cfg, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureDevinNativeWorkspaceTrusted(cfg, "/some/worktree"); err == nil {
		t.Fatal("expected error for zero-byte config, got nil (config would be overwritten)")
	}
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("zero-byte config must not be overwritten; got %d bytes: %s", len(data), data)
	}
}
