package workerlauncher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManifestValidationRejectsEscapesAndUntrustedInputs(t *testing.T) {
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, "workspaces")
	profileRoot := filepath.Join(root, "profiles")
	worktree := filepath.Join(workspaceRoot, "session-1")
	profile := filepath.Join(profileRoot, "session-1")
	for _, dir := range []string{worktree, profile} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	exe := filepath.Join(root, "ao.exe")
	if err := os.WriteFile(exe, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	base := Manifest{
		Version: ManifestVersion, SessionID: "session-1", ProjectID: "project-1",
		CanonicalWorktree: worktree, WorkerProfileRoot: profile,
		ExecutableID: ExecutableAOAgent, Argv: []string{"agent-process", "supervise"}, Cwd: worktree,
		AllowedEnvironmentKeys: []string{"AO_SESSION_ID"}, TerminalRows: 24, TerminalCols: 80,
		CreatedAt: now, ExpiresAt: now.Add(time.Minute), Nonce: strings.Repeat("a", 32),
	}
	cfg := ValidationConfig{
		WorkspaceRoot: workspaceRoot, SessionProfileRoot: profileRoot,
		Executables:        map[string]string{ExecutableAOAgent: exe},
		AllowedEnvironment: map[string]struct{}{"AO_SESSION_ID": {}}, Now: now,
	}
	if err := base.Validate(cfg); err != nil {
		t.Fatalf("valid manifest: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"worktree escape", func(m *Manifest) { m.CanonicalWorktree = root; m.Cwd = root }},
		{"profile escape", func(m *Manifest) { m.WorkerProfileRoot = root }},
		{"arbitrary executable", func(m *Manifest) { m.ExecutableID = `C:\\Windows\\System32\\cmd.exe` }},
		{"raw environment", func(m *Manifest) { m.AllowedEnvironmentKeys = []string{"PATH", "GITHUB_TOKEN"} }},
		{"expired", func(m *Manifest) { m.ExpiresAt = now.Add(-time.Second) }},
		{"bad nonce", func(m *Manifest) { m.Nonce = "short" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := base
			m.Argv = append([]string(nil), base.Argv...)
			m.AllowedEnvironmentKeys = append([]string(nil), base.AllowedEnvironmentKeys...)
			tt.mutate(&m)
			if err := m.Validate(cfg); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestCanonicalContainedDirectoryUsesPathBoundary(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "session")
	sibling := root + "-suffix"
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalContainedDirectory(inside, root); err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalContainedDirectory(sibling, root); err == nil {
		t.Fatal("string-prefix sibling accepted")
	}
}
