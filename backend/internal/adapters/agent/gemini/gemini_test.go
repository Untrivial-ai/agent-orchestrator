package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hooksjson"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestNativeHookActivity(t *testing.T) {
	for _, tc := range []struct {
		event, payload string
		want           domain.ActivityState
		ok             bool
	}{
		{"session-start", `{}`, domain.ActivityActive, true},
		{"user-prompt-submit", `{}`, domain.ActivityActive, true},
		{"notification", `{"notification_type":"ToolPermission"}`, domain.ActivityWaitingInput, true},
		{"notification", `{"notification_type":"Other"}`, "", false},
		{"notification", `invalid`, "", false},
		{"after-tool", `{}`, domain.ActivityActive, true},
		{"stop", `{}`, domain.ActivityIdle, true},
	} {
		t.Run(tc.event+tc.payload, func(t *testing.T) {
			got, ok := DeriveActivityState(tc.event, []byte(tc.payload))
			if got != tc.want || ok != tc.ok {
				t.Fatalf("got %q,%v", got, ok)
			}
		})
	}
}

func TestLaunchAndRestore(t *testing.T) {
	p := &Plugin{resolvedBinary: "gemini"}
	ctx := context.Background()
	cmd, err := p.GetLaunchCommand(ctx, ports.LaunchConfig{Prompt: "-fix this", NativeSessionID: "native-123", Config: ports.AgentConfig{Model: " model "}, Permissions: ports.PermissionModeAcceptEdits})
	want := []string{"gemini", "--skip-trust", "--approval-mode", "auto_edit", "--model", "model", "--session-id", "native-123", "--prompt-interactive", "-fix this"}
	if err != nil || !reflect.DeepEqual(cmd, want) {
		t.Fatalf("launch = %q, %v; want %q", cmd, err, want)
	}
	cmd, ok, err := p.GetRestoreCommand(ctx, ports.RestoreConfig{Session: ports.SessionRef{Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "native-123"}}, Prompt: "continue"})
	want = []string{"gemini", "--skip-trust", "--resume", "native-123", "--prompt-interactive", "continue"}
	if err != nil || !ok || !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore = %q, %v, %v", cmd, ok, err)
	}
	if _, ok, err := p.GetRestoreCommand(ctx, ports.RestoreConfig{}); ok || err != nil {
		t.Fatalf("missing identity: %v, %v", ok, err)
	}
}

func TestPermissions(t *testing.T) {
	for _, tc := range []struct {
		mode ports.PermissionMode
		want string
	}{{ports.PermissionModeDefault, ""}, {ports.PermissionModeAcceptEdits, "auto_edit"}, {ports.PermissionModeAuto, "auto_edit"}, {ports.PermissionModeBypassPermissions, "yolo"}, {"unknown", ""}} {
		t.Run(string(tc.mode), func(t *testing.T) {
			cmd, err := (&Plugin{resolvedBinary: "gemini"}).GetLaunchCommand(context.Background(), ports.LaunchConfig{Permissions: tc.mode})
			want := []string{"gemini", "--skip-trust"}
			if tc.want != "" {
				want = append(want, "--approval-mode", tc.want)
			}
			if err != nil || !reflect.DeepEqual(cmd, want) {
				t.Fatalf("got %q, %v; want %q", cmd, err, want)
			}
		})
	}
}

func TestRejectUnsupportedToolRestrictions(t *testing.T) {
	p := &Plugin{resolvedBinary: "gemini"}
	if _, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{DisallowedTools: []string{"shell"}}); err == nil {
		t.Fatal("silently ignored restricted tools")
	}
}

func TestCancelledLaunch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&Plugin{resolvedBinary: "gemini"}).GetLaunchCommand(ctx, ports.LaunchConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestHooksPreserveSettingsAndReconcile(t *testing.T) {
	ws := t.TempDir()
	dir := filepath.Join(ws, ".gemini")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"theme":"user","hooks":{"BeforeAgent":[{"hooks":[{"type":"command","command":"user-hook"}]}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	p := New()
	for range 2 {
		if err := p.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: ws}); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Theme string
		Hooks map[string][]hooksjson.MatcherGroup
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Theme != "user" || len(got.Hooks["BeforeAgent"]) != 1 || len(got.Hooks["BeforeAgent"][0].Hooks) != 2 || got.Hooks["BeforeAgent"][0].Hooks[0].Command != "user-hook" {
		t.Fatalf("settings = %s", data)
	}
	for _, event := range []string{"SessionStart", "BeforeAgent", "AfterAgent", "Notification", "AfterTool"} {
		if len(got.Hooks[event]) == 0 {
			t.Errorf("missing %s", event)
		}
	}
	if err := p.UninstallHooks(context.Background(), ws); err != nil {
		t.Fatal(err)
	}
	if ok, err := p.AreHooksInstalled(context.Background(), ws); err != nil || ok {
		t.Fatalf("uninstall = %v, %v", ok, err)
	}
}
