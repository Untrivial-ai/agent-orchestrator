//go:build windows

package agentlaunch_test

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/claudecode"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hooksjson"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// TestMain makes the test binary an executable callback fixture.
func TestMain(m *testing.M) {
	if os.Getenv("AO_TEST_CLAUDE_HOOK") != "1" {
		os.Exit(m.Run())
	}
	for i, arg := range os.Args {
		if arg == "hooks" {
			fmt.Println(strings.Join(os.Args[i:], " "))
			_, _ = io.Copy(os.Stdout, os.Stdin)
			os.Exit(0)
		}
	}
	os.Exit(92)
}

func TestWindowsInstalledClaudeHook(t *testing.T) {
	workspace := t.TempDir()
	p := &claudecode.Plugin{}
	if err := p.GetAgentHooks(t.Context(), ports.WorkspaceHookConfig{WorkspacePath: workspace}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(workspace, ".claude", "settings.local.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks map[string][]hooksjson.MatcherGroup `json:"hooks"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	hook := settings.Hooks["SessionStart"][0].Hooks[0]
	if string(hook.Extra["shell"]) != `"powershell"` {
		t.Fatalf("installed shell: %s", hook.Extra["shell"])
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Join(t.TempDir(), "AO's canonical cli.exe")
	if err := os.WriteFile(canonical, binary, 0700); err != nil {
		t.Fatal(err)
	}
	foreign := t.TempDir()
	if err := os.WriteFile(filepath.Join(foreign, "ao.cmd"), []byte("@echo FOREIGN\r\n@exit /b 91\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Fatal(err)
	}
	for _, missing := range []bool{false, true} {
		cmd := exec.CommandContext(t.Context(), powershell, "-NoProfile", "-NonInteractive", "-Command", hook.Command)
		for _, env := range os.Environ() {
			key, _, _ := strings.Cut(env, "=")
			if !strings.EqualFold(key, "AO_CLI") && !strings.EqualFold(key, "PATH") {
				cmd.Env = append(cmd.Env, env)
			}
		}
		cmd.Env = append(cmd.Env, "PATH="+foreign, "AO_TEST_CLAUDE_HOOK=1")
		if !missing {
			cmd.Env = append(cmd.Env, "AO_CLI="+filepath.ToSlash(canonical))
		}
		payload := `{"session_id":"native","source":"startup"}`
		cmd.Stdin = strings.NewReader(payload)
		out, err := cmd.CombinedOutput()
		if missing {
			if err == nil || !strings.Contains(string(out), "AO_CLI is not set") || strings.Contains(string(out), "FOREIGN") {
				t.Fatalf("missing canonical: %v: %s", err, out)
			}
		} else if err != nil || strings.ReplaceAll(string(out), "\r\n", "\n") != "hooks claude-code session-start\n"+payload {
			t.Fatalf("callback: %v: %s", err, out)
		}
	}
}
