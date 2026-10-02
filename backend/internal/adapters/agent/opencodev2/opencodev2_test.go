package opencodev2

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func fakeBinary(t *testing.T, version string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixture")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "opencode")
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf '%s\\n' '" + version + "'; exit; fi\nprintf '%s\\n' \"$@\"\nprintf '%s\\n' \"$OPENCODE_CONFIG\" \"$OPENCODE_CONFIG_CONTENT\"\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin:/bin")
	return binary
}

func TestLaunchAndRestoreV2ArgumentsAndOverlay(t *testing.T) {
	for _, restore := range []bool{false, true} {
		for _, tc := range []struct {
			mode        ports.PermissionMode
			flag        string
			permissions string
		}{
			{ports.PermissionModeDefault, "", `null`},
			{ports.PermissionModeAcceptEdits, "", `[{"action":"edit","resource":"*","effect":"allow"}]`},
			{ports.PermissionModeAuto, "--auto", `null`},
			{ports.PermissionModeBypassPermissions, "--dangerously-skip-permissions", `[{"action":"*","resource":"*","effect":"allow"}]`},
		} {
			t.Run(string(tc.mode)+map[bool]string{false: "/fresh", true: "/restore"}[restore], func(t *testing.T) {
				binary := fakeBinary(t, "2.0.0")
				custom := filepath.Join(t.TempDir(), "custom.jsonc")
				original := "{ // caller config\n\"model\": \"user/model\"\n}\n"
				if err := os.WriteFile(custom, []byte(original), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("OPENCODE_CONFIG", custom)
				t.Setenv("OPENCODE_CONFIG_CONTENT", `{"providers":{"local":{"name":"mine"}},"permissions":[{"action":"shell","resource":"git push *","effect":"deny"}],"agents":{"user-agent":{"system":"user instructions"},"ao-sess-1":{"description":"preserve me","system":"old"}}}`)
				cfg := ports.LaunchConfig{Config: ports.AgentConfig{Model: " provider/model "}, SessionID: "sess/1", SystemPrompt: "AO rules", Permissions: tc.mode, Prompt: "--help\n'quoted' $HOME"}
				var cmd []string
				var err error
				if restore {
					var ok bool
					cmd, ok, err = New().GetRestoreCommand(context.Background(), ports.RestoreConfig{Config: cfg.Config, SystemPrompt: cfg.SystemPrompt, Permissions: cfg.Permissions, Prompt: cfg.Prompt, Session: ports.SessionRef{ID: cfg.SessionID, Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "ses_native"}}})
					if !ok {
						t.Fatalf("restore not supported: %v", err)
					}
				} else {
					cmd, err = New().GetLaunchCommand(context.Background(), cfg)
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(cmd) < 4 || cmd[0] != "env" || !strings.HasPrefix(cmd[1], "OPENCODE_CONFIG_CONTENT=") || cmd[2] != binary {
					t.Fatalf("command = %#v", cmd)
				}
				want := []string{binary, "--standalone"}
				if tc.flag != "" {
					want = append(want, tc.flag)
				}
				if restore {
					want = append(want, "--session", "ses_native")
				}
				want = append(want, "--prompt="+cfg.Prompt)
				if !reflect.DeepEqual(cmd[2:], want) {
					t.Fatalf("argv = %#v, want %#v", cmd[2:], want)
				}
				var config struct {
					DefaultAgent string              `json:"default_agent"`
					Providers    map[string]any      `json:"providers"`
					Permissions  []map[string]string `json:"permissions"`
					Agents       map[string]struct {
						System, Mode, Model, Description string
						Permissions                      json.RawMessage
					} `json:"agents"`
				}
				content := strings.TrimPrefix(cmd[1], "OPENCODE_CONFIG_CONTENT=")
				if err := json.Unmarshal([]byte(content), &config); err != nil {
					t.Fatal(err)
				}
				agent := config.Agents["ao-sess-1"]
				if config.DefaultAgent != "ao-sess-1" || agent.System != "AO rules" || agent.Model != "provider/model" || agent.Mode != "primary" || agent.Description != "preserve me" {
					t.Fatalf("AO config = %+v", config)
				}
				var gotPerm, wantPerm any
				if len(agent.Permissions) > 0 {
					if err := json.Unmarshal(agent.Permissions, &gotPerm); err != nil {
						t.Fatal(err)
					}
				}
				if err := json.Unmarshal([]byte(tc.permissions), &wantPerm); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(gotPerm, wantPerm) {
					t.Fatalf("permissions=%s, want %s", agent.Permissions, tc.permissions)
				}
				if config.Agents["user-agent"].System != "user instructions" || config.Providers["local"] == nil || len(config.Permissions) != 1 || config.Permissions[0]["effect"] != "deny" {
					t.Fatalf("caller config lost: %s", content)
				}
				out, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput()
				if err != nil {
					t.Fatalf("execute argv: %v: %s", err, out)
				}
				if !strings.Contains(string(out), "--prompt="+cfg.Prompt+"\n"+custom+"\n"+content) {
					t.Fatalf("prompt/config changed at exec boundary: %s", out)
				}
				data, err := os.ReadFile(custom)
				if err != nil || string(data) != original {
					t.Fatalf("custom config changed: %q %v", data, err)
				}
			})
		}
	}
}

func TestV2PromptFileAndNoOverlay(t *testing.T) {
	binary := fakeBinary(t, "2.0.0")
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	cmd, err := New().GetLaunchCommand(context.Background(), ports.LaunchConfig{})
	if err != nil || !reflect.DeepEqual(cmd, []string{binary, "--standalone"}) {
		t.Fatalf("empty launch=%#v %v", cmd, err)
	}
	file := filepath.Join(t.TempDir(), "system.md")
	if err := os.WriteFile(file, []byte("file instructions\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd, err = New().GetLaunchCommand(context.Background(), ports.LaunchConfig{SystemPromptFile: file, SessionID: "sess"})
	if err != nil {
		t.Fatal(err)
	}
	var overlay map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(cmd[1], "OPENCODE_CONFIG_CONTENT=")), &overlay); err != nil {
		t.Fatal(err)
	}
	if overlay["agents"].(map[string]any)["ao-sess"].(map[string]any)["system"] != "file instructions\n" {
		t.Fatalf("overlay=%#v", overlay)
	}
}

func TestV2ClearsPreviousAOAgentModelAndPermissions(t *testing.T) {
	fakeBinary(t, "2.0.0")
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"agents":{"ao-sess":{"model":"old/model","permissions":[{"action":"*","resource":"*","effect":"allow"}],"description":"preserved"}}}`)
	cmd, err := New().GetLaunchCommand(context.Background(), ports.LaunchConfig{SessionID: "sess", SystemPrompt: "new rules"})
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Agents map[string]map[string]any `json:"agents"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(cmd[1], "OPENCODE_CONFIG_CONTENT=")), &config); err != nil {
		t.Fatal(err)
	}
	agent := config.Agents["ao-sess"]
	if _, ok := agent["model"]; ok {
		t.Fatalf("stale model retained: %#v", agent)
	}
	if _, ok := agent["permissions"]; ok {
		t.Fatalf("stale bypass retained: %#v", agent)
	}
	if agent["description"] != "preserved" {
		t.Fatalf("unrelated field lost: %#v", agent)
	}
}

func TestPrepareACPConfigContentPreservesUserConfigAndConstrainsTheBuiltInAgent(t *testing.T) {
	existing := `{"providers":{"local":{"name":"mine"}},"permissions":[{"action":"shell","resource":"git push *","effect":"deny"}],"agents":{"mine":{"mode":"primary","system":"user rules"},"build":{"description":"preserve me"}}}`
	for _, test := range []struct {
		mode ports.PermissionMode
	}{
		{ports.PermissionModeDefault},
		{ports.PermissionModeAcceptEdits},
		{ports.PermissionModeAuto},
		{ports.PermissionModeBypassPermissions},
	} {
		t.Run(string(test.mode), func(t *testing.T) {
			content, err := PrepareACPConfigContent(existing, "AO standing rules", test.mode)
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				DefaultAgent string              `json:"default_agent"`
				Providers    map[string]any      `json:"providers"`
				Permissions  []map[string]string `json:"permissions"`
				Agents       map[string]struct {
					Mode, System, Description string
					Permissions               []permissionRule
				} `json:"agents"`
			}
			if err := json.Unmarshal([]byte(content), &config); err != nil {
				t.Fatal(err)
			}
			if config.DefaultAgent != "build" || config.Providers["local"] == nil ||
				len(config.Permissions) != 1 || config.Agents["mine"].System != "user rules" {
				t.Fatalf("user/v2 config = %#v", config)
			}
			build := config.Agents["build"]
			if build.Mode != "primary" || build.System != "AO standing rules" || build.Description != "preserve me" {
				t.Fatalf("build agent = %#v", build)
			}
			var raw struct {
				Agents map[string]map[string]json.RawMessage `json:"agents"`
			}
			if err := json.Unmarshal([]byte(content), &raw); err != nil {
				t.Fatal(err)
			}
			if _, ok := raw.Agents["build"]["permissions"]; ok {
				t.Fatal("agents.build.permissions must stay absent so file-defined rules survive the array-replacing merge")
			}
		})
	}
}

func TestV2RejectsFailuresWithoutSideEffects(t *testing.T) {
	for _, tc := range []struct {
		name, version, content, native string
		canceled                       bool
		wantErr                        string
	}{
		{"wrong major", "1.18.33", "", "ses_1", false, "requires OpenCode 2"},
		{"malformed version", "bad", "", "ses_1", false, "version"},
		{"canceled", "2.0.0", "", "ses_1", true, "canceled"},
		{"bad content", "2.0.0", "{invalid", "ses_1", false, "OPENCODE_CONFIG_CONTENT"},
		{"bad agents", "2.0.0", `{"agents":[]}`, "ses_1", false, "agents"},
		{"bad agent", "2.0.0", `{"agents":{"ao-sess":false}}`, "ses_1", false, "ao-sess"},
		{"missing native", "1.18.33", "{invalid", "", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeBinary(t, tc.version)
			t.Setenv("OPENCODE_CONFIG_CONTENT", tc.content)
			dir := t.TempDir()
			file := filepath.Join(dir, "system.md")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			cmd, ok, err := New().GetRestoreCommand(ctx, ports.RestoreConfig{SystemPrompt: "rules", SystemPromptFile: file, Session: ports.SessionRef{ID: "sess", Metadata: map[string]string{ports.MetadataKeyAgentSessionID: tc.native}}})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error=%v want %q", err, tc.wantErr)
			}
			if cmd != nil || ok {
				t.Fatalf("failure returned command: %#v %v", cmd, ok)
			}
			if tc.canceled && !errors.Is(err, context.Canceled) {
				t.Fatalf("error=%v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failure wrote files: %v %v", entries, err)
			}
			if os.Getenv("OPENCODE_CONFIG_CONTENT") != tc.content {
				t.Fatal("caller environment mutated")
			}
			if tc.native != "" {
				cmd, err = New().GetLaunchCommand(ctx, ports.LaunchConfig{SessionID: "sess", SystemPrompt: "rules", SystemPromptFile: file})
				if err == nil || cmd != nil {
					t.Fatalf("launch failure returned %#v %v", cmd, err)
				}
			}
		})
	}
}

func TestV2ResolvesAgainBetweenAttempts(t *testing.T) {
	first := fakeBinary(t, "2.0.0")
	plugin := New()
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{})
	if err != nil || cmd[0] != first {
		t.Fatalf("first=%#v %v", cmd, err)
	}
	fakeBinary(t, "1.18.33")
	if _, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{}); err == nil {
		t.Fatal("reused previous binary after PATH changed")
	}
}

func TestV2AuthStatusUsesPrivateServerAndConnections(t *testing.T) {
	for _, tc := range []struct {
		output string
		want   ports.AgentAuthStatus
	}{
		{`[]`, ports.AgentAuthStatusUnknown},
		{`[{"id":"anthropic","name":"Anthropic","connections":[{"type":"credential","label":"personal"}]}]`, ports.AgentAuthStatusAuthorized},
		{`[{"id":"anthropic","name":"Anthropic","connections":[]}]`, ports.AgentAuthStatusUnknown},
		{`bad output`, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tc.output, func(t *testing.T) {
			binary := fakeBinary(t, "2.0.0")
			script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 2.0.0; exit; fi\n[ \"$*\" = 'auth list --standalone --format json' ] || exit 99\nprintf '%s\\n' '" + tc.output + "'\n"
			if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			status, err := New().AuthStatus(context.Background())
			if err != nil || status != tc.want {
				t.Fatalf("auth=%q %v, want %q", status, err, tc.want)
			}
		})
	}
}

func TestSemanticMessageAcceptanceIsNotAdvertised(t *testing.T) {
	var agent ports.Agent = New()
	signaler, ok := agent.(ports.SemanticMessageAcceptanceSignaler)
	if !ok || signaler.EmitsSemanticMessageAcceptance() {
		t.Fatal("OpenCode 2 must not advertise semantic acceptance without a post-admission signal")
	}
}
