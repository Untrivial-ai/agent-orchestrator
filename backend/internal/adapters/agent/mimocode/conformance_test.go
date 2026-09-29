package mimocode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// TestReleasedMiMoCodeConformance is opt-in because it executes a real released
// provider binary. It always isolates MiMo's profile and workspace.
func TestReleasedMiMoCodeConformance(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv("AO_MIMOCODE_BINARY"))
	if binary == "" {
		t.Skip("set AO_MIMOCODE_BINARY to a released mimo executable")
	}
	profile := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command(binary, args...) //nolint:gosec // explicit opt-in test binary
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "MIMOCODE_HOME="+profile, "MIMOCODE_DISABLE_DEFAULT_PLUGINS=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("mimo %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}

	if version := strings.TrimSpace(run(t.TempDir(), "--version")); !strings.HasPrefix(version, "0.1.") {
		t.Fatalf("version = %q, want released 0.1.x", version)
	}
	help := run(t.TempDir(), "--help")
	for _, flag := range []string{"--prompt", "--model", "--agent", "--trust", "--session", "--dangerously-skip-permissions"} {
		if !strings.Contains(help, flag) {
			t.Fatalf("help does not advertise %s", flag)
		}
	}
	if output := run(t.TempDir(), "providers", "list", "--help"); !strings.Contains(output, "list providers and credentials") {
		t.Fatalf("providers list help is unexpected:\n%s", output)
	}

	workspace := t.TempDir()
	if err := New().GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		WorkspacePath: workspace,
		SessionID:     "sess-1",
		SystemPrompt:  "Follow AO instructions.",
		Config:        ports.AgentConfig{Permissions: ports.PermissionModeAcceptEdits},
	}); err != nil {
		t.Fatal(err)
	}
	agent := run(workspace, "debug", "agent", "ao-sess-1")
	for _, marker := range []string{"Follow AO instructions.", "edit", "allow"} {
		if !strings.Contains(agent, marker) {
			t.Fatalf("resolved agent omitted %q:\n%s", marker, agent)
		}
	}
	if _, err := os.Stat(filepath.Join(workspace, ".mimocode", "hooks", "ao-activity.ts")); err != nil {
		t.Fatalf("activity hook unavailable to released binary: %v", err)
	}
}
