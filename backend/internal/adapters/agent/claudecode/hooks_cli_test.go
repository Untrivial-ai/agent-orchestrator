package claudecode

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hooksjson"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestManagedSessionStartUsesCanonicalCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX hook fixture; Windows canonical execution tested in agentlaunch")
	}
	workspace, foreign := t.TempDir(), t.TempDir()
	canonical := filepath.Join(t.TempDir(), "AO's canonical cli")
	for path, body := range map[string]string{
		canonical:                    "#!/bin/sh\nprintf '%s\\n' \"$*\"\ncat\n",
		filepath.Join(foreign, "ao"): "#!/bin/sh\necho FOREIGN >&2\nexit 91\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	p := &Plugin{resolvedBinary: "claude"}
	if err := p.GetAgentHooks(t.Context(), ports.WorkspaceHookConfig{WorkspacePath: workspace}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(claudeSettingsPath(workspace))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks map[string][]hooksjson.MatcherGroup `json:"hooks"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	command := settings.Hooks["SessionStart"][0].Hooks[0].Command
	for _, mode := range []string{"stale", "absent", "unset canonical"} {
		t.Run(mode, func(t *testing.T) {
			path := "/usr/bin:/bin"
			if mode == "stale" {
				path = foreign + ":" + path
			}
			cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", command)
			cmd.Env = []string{"PATH=" + path}
			if mode != "unset canonical" {
				cmd.Env = append(cmd.Env, "AO_CLI="+canonical)
			}
			payload := `{"session_id":"native-session","source":"startup"}`
			cmd.Stdin = strings.NewReader(payload)
			out, err := cmd.CombinedOutput()
			if mode == "unset canonical" {
				if err == nil || !strings.Contains(string(out), "AO_CLI is not set") {
					t.Fatalf("missing canonical reference: %v: %s", err, out)
				}
			} else if err != nil || string(out) != "hooks claude-code session-start\n"+payload {
				t.Fatalf("hook callback: %v: %s", err, out)
			}
		})
	}
}

func TestLegacyHooksRemainDetectableAndRemovable(t *testing.T) {
	workspace := t.TempDir()
	path := claudeSettingsPath(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"ao hooks claude-code stop"},{"type":"command","command":"user hook"}]}]}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{}
	if installed, err := p.AreHooksInstalled(t.Context(), workspace); err != nil || !installed {
		t.Fatalf("legacy detection: %v, %v", installed, err)
	}
	if err := p.UninstallHooks(t.Context(), workspace); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "ao hooks") || !strings.Contains(string(data), "user hook") {
		t.Fatalf("legacy uninstall: %s", data)
	}
}
