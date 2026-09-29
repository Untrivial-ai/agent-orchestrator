package mimocode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestGetAgentHooksInstallsOwnedHookAgentAndSkill(t *testing.T) {
	workspace := t.TempDir()
	cfg := ports.WorkspaceHookConfig{
		WorkspacePath: workspace, SessionID: "sess/1", SystemPrompt: "Follow AO instructions.",
		Config: ports.AgentConfig{Permissions: ports.PermissionModeAcceptEdits},
	}
	p := New()
	if err := p.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := p.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatalf("second install is not idempotent: %v", err)
	}

	for path, markers := range map[string][]string{
		mimoHookPath(workspace):            {mimoHookSentinel, `"mimo-code"`, `"permission.ask"`, `case "question.asked":`, `case "session.created":`},
		mimoAgentPath(workspace, "sess/1"): {mimoAgentSentinel, "mode: primary", "edit: allow", "Follow AO instructions."},
		filepath.Join(workspace, ".mimocode", "skills", "using-ao", "SKILL.md"): {"using-ao"},
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, marker := range markers {
			if !strings.Contains(string(data), marker) {
				t.Errorf("%s missing %q", path, marker)
			}
		}
	}
	installed, err := p.AreHooksInstalled(context.Background(), workspace)
	if err != nil || !installed {
		t.Fatalf("AreHooksInstalled = (%v, %v), want true", installed, err)
	}
}

func TestGetAgentHooksPreservesForeignOwnedPath(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, ".mimocode", "hooks", "ao-activity.ts")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("export default {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := New().GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: workspace}); err == nil {
		t.Fatal("GetAgentHooks overwrote a foreign hook")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "export default {}\n" {
		t.Fatalf("foreign hook changed: %q, %v", data, err)
	}
}

func TestUninstallHooksRemovesOnlyOwnedArtifacts(t *testing.T) {
	workspace := t.TempDir()
	p := New()
	cfg := ports.WorkspaceHookConfig{WorkspacePath: workspace, SessionID: "sess-1", SystemPrompt: "AO rules"}
	if err := p.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(workspace, ".mimocode", "hooks", "user.ts")
	if err := os.WriteFile(foreign, []byte("export default {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.UninstallHooks(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("foreign hook removed: %v", err)
	}
	if _, err := os.Stat(mimoHookPath(workspace)); !os.IsNotExist(err) {
		t.Fatalf("AO hook remains: %v", err)
	}
	if _, err := os.Stat(mimoAgentPath(workspace, "sess-1")); !os.IsNotExist(err) {
		t.Fatalf("AO agent remains: %v", err)
	}
}
