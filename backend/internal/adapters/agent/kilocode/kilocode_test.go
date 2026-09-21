package kilocode

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/activitystate"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestManifestIDIsKilocode(t *testing.T) {
	m := New().Manifest()
	if m.ID != "kilocode" {
		t.Fatalf("Manifest ID = %q, want kilocode", m.ID)
	}
	if m.Name != "Kilo Code" {
		t.Fatalf("Manifest Name = %q, want Kilo Code", m.Name)
	}
}

func TestResolveKilocodeBinaryFindsOfficialKiloCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kilo")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("HOME", t.TempDir())

	got, err := ResolveKilocodeBinary(context.Background())
	if err != nil {
		t.Fatalf("ResolveKilocodeBinary: %v", err)
	}
	if got != path {
		t.Fatalf("ResolveKilocodeBinary = %q, want %q", got, path)
	}
}

func TestGetLaunchCommandBuildsArgv(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kilocode"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions:      ports.PermissionModeBypassPermissions,
		Prompt:           "-fix this",
		SystemPromptFile: filepath.Join("tmp", "prompt with spaces.md"),
		SystemPrompt:     "follow AO rules",
		SessionID:        "sess-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Kilo has no system-prompt flag, so AO injects a generated agent through
	// KILO_CONFIG_CONTENT and selects it with --agent. bypass-permissions shares
	// that env payload because the TUI has no permission flag.
	want := []string{
		"env", `KILO_CONFIG_CONTENT={"permission":{"*":"allow"},"agent":{"ao-sess-1":{"prompt":"follow AO rules"}}}`,
		"kilocode",
		"--agent", "ao-sess-1",
		"--prompt", "-fix this",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandReadsSystemPromptFile(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kilocode"}
	dir := t.TempDir()
	file := filepath.Join(dir, "system.md")
	if err := os.WriteFile(file, []byte("file rules\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPromptFile: file,
		SessionID:        "sess/file",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"env", `KILO_CONFIG_CONTENT={"agent":{"ao-sess-file":{"prompt":"file rules\n"}}}`,
		"kilocode",
		"--agent", "ao-sess-file",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandMapsPermissionModes(t *testing.T) {
	tests := []struct {
		name       string
		permission ports.PermissionMode
		// wantEnv is the expected KILO_CONFIG_CONTENT value, or "" when the mode
		// emits no env prefix at all (defers entirely to Kilo's own config).
		wantEnv string
	}{
		{name: "default", permission: ports.PermissionModeDefault, wantEnv: ""},
		{name: "accept-edits", permission: ports.PermissionModeAcceptEdits, wantEnv: `{"permission":{"edit":"allow"}}`},
		{name: "auto", permission: ports.PermissionModeAuto, wantEnv: `{"permission":{"bash":"allow","edit":"allow"}}`},
		{name: "bypass-permissions", permission: ports.PermissionModeBypassPermissions, wantEnv: `{"permission":{"*":"allow"}}`},
		{name: "empty", permission: "", wantEnv: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &Plugin{resolvedBinary: "kilocode"}
			cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{Permissions: tt.permission})
			if err != nil {
				t.Fatal(err)
			}
			// A permission FLAG must never leak onto the interactive TUI launch;
			// those exist only on `kilo run` (--auto).
			if contains(cmd, "--auto") {
				t.Fatalf("command %#v contains run-only --auto", cmd)
			}
			if tt.wantEnv == "" {
				if len(cmd) == 0 || cmd[0] == "env" {
					t.Fatalf("command %#v should have no env prefix", cmd)
				}
				return
			}
			// Non-default modes prepend `env KILO_CONFIG_CONTENT=<json>`.
			want := "KILO_CONFIG_CONTENT=" + tt.wantEnv
			if len(cmd) < 3 || cmd[0] != "env" || cmd[1] != want || cmd[2] != "kilocode" {
				t.Fatalf("command %#v must be prefixed with `env %s`", cmd, want)
			}
		})
	}
}

func TestGetLaunchCommandAppendsConfiguredModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kilocode"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions:  ports.PermissionModeBypassPermissions,
		SystemPrompt: "follow AO rules",
		SessionID:    "sess-1",
		// Surrounding whitespace must be trimmed before it reaches the config.
		Config: ports.AgentConfig{Model: "  anthropic/claude-haiku-4-20250514  "},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The role model rides on the AO-generated agent (agent.<name>.model),
	// alongside the injected system prompt, selected with --agent.
	want := []string{
		"env", `KILO_CONFIG_CONTENT={"permission":{"*":"allow"},"agent":{"ao-sess-1":{"prompt":"follow AO rules","model":"anthropic/claude-haiku-4-20250514"}}}`,
		"kilocode",
		"--agent", "ao-sess-1",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
	if strings.Contains(strings.Join(cmd, " "), "  anthropic/claude-haiku-4-20250514  ") {
		t.Fatalf("command %#v used untrimmed model", cmd)
	}
}

// TestGetLaunchCommandAppendsModelWithoutSystemPrompt is the issue's repro: a
// role config with a model but no system prompt. The model must still create and
// select an AO agent, otherwise --agent is never passed and the model is dropped.
func TestGetLaunchCommandAppendsModelWithoutSystemPrompt(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kilocode"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SessionID: "sess-1",
		Config:    ports.AgentConfig{Model: "anthropic/claude-haiku-4-20250514"},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"env", `KILO_CONFIG_CONTENT={"agent":{"ao-sess-1":{"model":"anthropic/claude-haiku-4-20250514"}}}`,
		"kilocode",
		"--agent", "ao-sess-1",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandOmitsBlankConfiguredModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kilocode"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SessionID: "sess-1",
		Config:    ports.AgentConfig{Model: " \t "},
	})
	if err != nil {
		t.Fatal(err)
	}

	// A blank model must not fabricate an agent, an --agent flag, or an env prefix.
	want := []string{"kilocode"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetPromptDeliveryStrategyIsInCommand(t *testing.T) {
	plugin := &Plugin{}

	got, err := plugin.GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.PromptDeliveryInCommand {
		t.Fatalf("unexpected strategy: %q", got)
	}
}

func TestGetConfigSpecReportsModelField(t *testing.T) {
	plugin := &Plugin{}

	spec, err := plugin.GetConfigSpec(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.ConfigField{
		{
			Key:         "model",
			Type:        ports.ConfigFieldString,
			Description: "Model override written to the AO-generated Kilo agent (agent.<name>.model); format provider/model-id (e.g. anthropic/claude-haiku-4-20250514).",
		},
	}
	if !reflect.DeepEqual(spec.Fields, want) {
		t.Fatalf("config fields\nwant: %#v\n got: %#v", want, spec.Fields)
	}
}

func TestGetAgentHooksInstallsPlugin(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kilocode"}
	workspace := t.TempDir()

	// A user's own plugin in the same dir must survive AO's install untouched.
	pluginDir := filepath.Dir(kilocodePluginPath(workspace))
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userPlugin := filepath.Join(pluginDir, "user.js")
	userBody := []byte("export const userPlugin = async () => ({})\n")
	if err := os.WriteFile(userPlugin, userBody, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	cfg := ports.WorkspaceHookConfig{DataDir: t.TempDir(), SessionID: "sess-1", WorkspacePath: workspace}
	if err := plugin.GetAgentHooks(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	// A second install must be idempotent (overwrite with identical content).
	if err := plugin.GetAgentHooks(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	if installed, err := plugin.AreHooksInstalled(ctx, workspace); err != nil || !installed {
		t.Fatalf("AreHooksInstalled after install = (%v, %v), want (true, nil)", installed, err)
	}

	data, err := os.ReadFile(kilocodePluginPath(workspace))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, kilocodePluginSentinel) {
		t.Fatalf("installed plugin missing AO sentinel:\n%s", body)
	}
	// Every normalized activity event must be wired via `ao hooks kilocode <event>`.
	for _, event := range kilocodeManagedEvents {
		want := kilocodeHookCommandPrefix + event
		if !strings.Contains(body, want) {
			t.Fatalf("installed plugin missing hook command %q:\n%s", want, body)
		}
	}
	// The Kilo-native lifecycle surfaces the plugin subscribes to. Stop maps to
	// session.status(idle) — NOT the deprecated session.idle — the user prompt is
	// detected from message.updated/message.part.updated, and permission requests
	// from the permission.ask hook.
	for _, marker := range []string{"session.created", "message.updated", "message.part.updated", "session.status", "permission.ask"} {
		if !strings.Contains(body, marker) {
			t.Fatalf("installed plugin missing Kilo event %q:\n%s", marker, body)
		}
	}
	// Guard against regressing back to subscribing to the deprecated/unreliable
	// session.idle event (the quoted event string is how a `case` would name it;
	// the explanatory comment mentions it unquoted, which is fine).
	if strings.Contains(body, `"session.idle"`) {
		t.Fatalf("plugin subscribes to deprecated session.idle; use session.status(idle):\n%s", body)
	}
	for _, marker := range []string{"function readSessionID", "function readCreatedSessionID", "sessionID", "sessionId", "session_id"} {
		if !strings.Contains(body, marker) {
			t.Fatalf("installed plugin missing session id normalization marker %q:\n%s", marker, body)
		}
	}
	if strings.Contains(body, "value?.session_id ?? value?.id") {
		t.Fatalf("readSessionID must not fall back to generic object id:\n%s", body)
	}
	// A hung `ao hooks` call must not block Kilo forever, so each spawn is
	// time-boxed (parity with the claude/codex 30s hook timeout).
	if !strings.Contains(body, "timeout:") {
		t.Fatalf("plugin spawn has no timeout; a hung hook would block Kilo:\n%s", body)
	}

	// The user's plugin is untouched.
	got, err := os.ReadFile(userPlugin)
	if err != nil {
		t.Fatalf("user plugin removed by install: %v", err)
	}
	if !reflect.DeepEqual(got, userBody) {
		t.Fatalf("user plugin modified by install: %q", got)
	}
}

func TestGetAgentHooksRefusesToClobberForeignFile(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kilocode"}
	workspace := t.TempDir()
	ctx := context.Background()

	// A non-AO file occupying AO's exact path must NOT be silently overwritten.
	pluginPath := kilocodePluginPath(workspace)
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := []byte("export const notOurs = async () => ({})\n")
	if err := os.WriteFile(pluginPath, foreign, 0o644); err != nil {
		t.Fatal(err)
	}

	err := plugin.GetAgentHooks(ctx, ports.WorkspaceHookConfig{WorkspacePath: workspace})
	if err == nil {
		t.Fatal("GetAgentHooks overwrote a non-AO file; want a loud error")
	}
	got, readErr := os.ReadFile(pluginPath)
	if readErr != nil {
		t.Fatalf("foreign file removed by refused install: %v", readErr)
	}
	if !reflect.DeepEqual(got, foreign) {
		t.Fatalf("foreign file modified by refused install: %q", got)
	}
}

func TestUninstallHooksRemovesPlugin(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kilocode"}
	workspace := t.TempDir()
	ctx := context.Background()
	cfg := ports.WorkspaceHookConfig{DataDir: t.TempDir(), SessionID: "sess-1", WorkspacePath: workspace}

	// Pre-seed a user's own plugin; it must survive uninstall.
	pluginDir := filepath.Dir(kilocodePluginPath(workspace))
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userPlugin := filepath.Join(pluginDir, "user.js")
	if err := os.WriteFile(userPlugin, []byte("export const userPlugin = async () => ({})\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := plugin.GetAgentHooks(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if installed, err := plugin.AreHooksInstalled(ctx, workspace); err != nil || !installed {
		t.Fatalf("AreHooksInstalled after install = (%v, %v), want (true, nil)", installed, err)
	}

	if err := plugin.UninstallHooks(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	if installed, err := plugin.AreHooksInstalled(ctx, workspace); err != nil || installed {
		t.Fatalf("AreHooksInstalled after uninstall = (%v, %v), want (false, nil)", installed, err)
	}
	if _, err := os.Stat(kilocodePluginPath(workspace)); !os.IsNotExist(err) {
		t.Fatalf("AO plugin still present after uninstall: err=%v", err)
	}
	if _, err := os.Stat(userPlugin); err != nil {
		t.Fatalf("user plugin removed by uninstall: %v", err)
	}
}

func TestUninstallHooksLeavesForeignFile(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kilocode"}
	workspace := t.TempDir()
	ctx := context.Background()

	// A non-AO file occupying AO's filename must NOT be deleted by uninstall.
	pluginPath := kilocodePluginPath(workspace)
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := []byte("export const notOurs = async () => ({})\n")
	if err := os.WriteFile(pluginPath, foreign, 0o644); err != nil {
		t.Fatal(err)
	}

	if installed, err := plugin.AreHooksInstalled(ctx, workspace); err != nil || installed {
		t.Fatalf("AreHooksInstalled on foreign file = (%v, %v), want (false, nil)", installed, err)
	}
	if err := plugin.UninstallHooks(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("foreign file removed by uninstall: %v", err)
	}
	if !reflect.DeepEqual(got, foreign) {
		t.Fatalf("foreign file modified by uninstall: %q", got)
	}
}

func TestGetRestoreCommandReadsAgentSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kilocode"}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Permissions: ports.PermissionModeBypassPermissions,
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "ses_abc123"},
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{
		"env", `KILO_CONFIG_CONTENT={"permission":{"*":"allow"}}`,
		"kilocode",
		"--session", "ses_abc123",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore cmd\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetRestoreCommandReappliesSystemPromptAgent(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kilocode"}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		SystemPrompt: "restore AO rules",
		Session: ports.SessionRef{
			ID:       "sess-1",
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "ses_abc123"},
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{
		"env", `KILO_CONFIG_CONTENT={"agent":{"ao-sess-1":{"prompt":"restore AO rules"}}}`,
		"kilocode",
		"--agent", "ao-sess-1",
		"--session", "ses_abc123",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore cmd\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetRestoreCommandAppendsConfiguredModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kilocode"}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			ID:       "sess-1",
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "ses_abc123"},
		},
		Config: ports.AgentConfig{Model: "anthropic/claude-haiku-4-20250514"},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	// Resume re-injects the role model on the AO agent, mirroring launch.
	want := []string{
		"env", `KILO_CONFIG_CONTENT={"agent":{"ao-sess-1":{"model":"anthropic/claude-haiku-4-20250514"}}}`,
		"kilocode",
		"--agent", "ao-sess-1",
		"--session", "ses_abc123",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore cmd\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetRestoreCommandFalseWithoutAgentSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kilocode"}

	cases := []struct {
		name string
		ref  ports.SessionRef
	}{
		{"empty session ref", ports.SessionRef{}},
		{"empty metadata", ports.SessionRef{Metadata: map[string]string{}}},
		{"blank agent session metadata", ports.SessionRef{Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "   "}}},
		{"workspace path only", ports.SessionRef{WorkspacePath: "/some/path"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
				Permissions: ports.PermissionModeDefault,
				Session:     tc.ref,
			})
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if ok {
				t.Fatalf("ok = true, want false")
			}
			if cmd != nil {
				t.Fatalf("cmd = %#v, want nil", cmd)
			}
		})
	}
}

func TestSessionInfoReadsHookMetadata(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kilocode"}

	info, ok, err := plugin.SessionInfo(context.Background(), ports.SessionRef{
		WorkspacePath: "/some/path",
		Metadata: map[string]string{
			ports.MetadataKeyAgentSessionID: "ses_abc123",
			ports.MetadataKeyTitle:          "Fix login redirect",
			ports.MetadataKeySummary:        "Updated the auth callback and tests.",
			"ignored":                       "not returned",
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if info.AgentSessionID != "ses_abc123" {
		t.Fatalf("AgentSessionID = %q, want native id", info.AgentSessionID)
	}
	if info.Title != "Fix login redirect" {
		t.Fatalf("Title = %q, want hook title", info.Title)
	}
	if info.Summary != "Updated the auth callback and tests." {
		t.Fatalf("Summary = %q, want hook summary", info.Summary)
	}
	if info.Metadata != nil {
		t.Fatalf("Metadata = %#v, want nil for kilocode", info.Metadata)
	}
}

func TestSessionInfoFalseWhenNoHookMetadata(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "kilocode"}

	info, ok, err := plugin.SessionInfo(context.Background(), ports.SessionRef{
		WorkspacePath: "/some/path",
		Metadata:      map[string]string{},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ok {
		t.Fatalf("ok = true, want false")
	}
	if !reflect.DeepEqual(info, ports.SessionInfo{}) {
		t.Fatalf("info = %#v, want zero value", info)
	}
}

func TestDeriveActivityState(t *testing.T) {
	cases := []struct {
		event     string
		wantState domain.ActivityState
		wantOK    bool
	}{
		{"session-start", domain.ActivityActive, true},
		{"user-prompt-submit", domain.ActivityActive, true},
		{"stop", domain.ActivityIdle, true},
		{"permission-request", domain.ActivityWaitingInput, true},
		{"unknown", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.event, func(t *testing.T) {
			state, ok := activitystate.StandardDeriveActivityState(tc.event, nil)
			if state != tc.wantState || ok != tc.wantOK {
				t.Fatalf("StandardDeriveActivityState(%q) = (%q, %v), want (%q, %v)", tc.event, state, ok, tc.wantState, tc.wantOK)
			}
		})
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// These methods check ctx.Err() before doing any work, so a cancelled
	// context surfaces as an error. (GetLaunchCommand resolves the binary first,
	// whose own ctx check is short-circuited by the cached resolvedBinary, so it
	// is intentionally not asserted here — matching the codex/opencode exemplars.)
	plugin := &Plugin{resolvedBinary: "kilocode"}
	if _, err := plugin.GetPromptDeliveryStrategy(ctx, ports.LaunchConfig{}); err == nil {
		t.Fatal("GetPromptDeliveryStrategy: want ctx error, got nil")
	}
	if _, err := plugin.GetConfigSpec(ctx); err == nil {
		t.Fatal("GetConfigSpec: want ctx error, got nil")
	}
	if _, _, err := plugin.GetRestoreCommand(ctx, ports.RestoreConfig{}); err == nil {
		t.Fatal("GetRestoreCommand: want ctx error, got nil")
	}
	if _, _, err := plugin.SessionInfo(ctx, ports.SessionRef{}); err == nil {
		t.Fatal("SessionInfo: want ctx error, got nil")
	}
	if err := plugin.GetAgentHooks(ctx, ports.WorkspaceHookConfig{WorkspacePath: "/tmp"}); err == nil {
		t.Fatal("GetAgentHooks: want ctx error, got nil")
	}
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
