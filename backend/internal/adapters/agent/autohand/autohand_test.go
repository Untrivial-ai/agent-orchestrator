package autohand

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/activitystate"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestManifestIDMatchesHarness(t *testing.T) {
	m := (&Plugin{}).Manifest()
	if m.ID != "autohand" {
		t.Fatalf("Manifest ID = %q, want %q", m.ID, "autohand")
	}
	if adapterID != "autohand" {
		t.Fatalf("adapterID = %q, want %q", adapterID, "autohand")
	}
	if len(m.Capabilities) != 1 || m.Capabilities[0] != "agent" {
		t.Fatalf("Capabilities = %#v, want [agent]", m.Capabilities)
	}
}

func TestAugmentRuntimeEnvDisablesAutomaticBrowserLogin(t *testing.T) {
	env := map[string]string{autohandNoBrowserEnv: "0", "PRESERVED": "value"}

	(&Plugin{}).AugmentRuntimeEnv(env, t.TempDir())

	if got := env[autohandNoBrowserEnv]; got != "1" {
		t.Fatalf("%s = %q, want 1", autohandNoBrowserEnv, got)
	}
	if got := env["PRESERVED"]; got != "value" {
		t.Fatalf("PRESERVED = %q, want value", got)
	}
}

func TestGetLaunchCommandBuildsArgv(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "autohand"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions:      ports.PermissionModeBypassPermissions,
		Prompt:           "-fix this",
		WorkspacePath:    "/work/space",
		SystemPromptFile: filepath.Join("tmp", "prompt with spaces.md"),
		SystemPrompt:     "inline wins",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"autohand",
		"--path", "/work/space",
		"--unrestricted",
		"--sys-prompt", "inline wins",
		"--", "-fix this",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandInlineSystemPrompt(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "autohand"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPrompt: "be terse",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"autohand", "--sys-prompt", "be terse"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandMapsApprovalModes(t *testing.T) {
	tests := []struct {
		name        string
		permission  ports.PermissionMode
		want        []string
		notExpected []string
	}{
		{
			name:        "default",
			permission:  ports.PermissionModeDefault,
			notExpected: []string{"--unrestricted", "--yes", "--restricted"},
		},
		{
			name:        "accept-edits",
			permission:  ports.PermissionModeAcceptEdits,
			want:        []string{"--yes"},
			notExpected: []string{"--unrestricted"},
		},
		{
			name:       "auto",
			permission: ports.PermissionModeAuto,
			want:       []string{"--unrestricted"},
		},
		{
			name:       "bypass-permissions",
			permission: ports.PermissionModeBypassPermissions,
			want:       []string{"--unrestricted"},
		},
		{
			name:        "unknown falls back to default",
			permission:  "frobnicate",
			notExpected: []string{"--unrestricted", "--yes", "--restricted"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &Plugin{resolvedBinary: "autohand"}
			cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
				Permissions: tt.permission,
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tt.want {
				if !contains(cmd, want) {
					t.Fatalf("command %#v missing %q", cmd, want)
				}
			}
			for _, missing := range tt.notExpected {
				if contains(cmd, missing) {
					t.Fatalf("command %#v contains %q", cmd, missing)
				}
			}
		})
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
			Description: "Model override passed to `autohand --model`.",
		},
	}
	if !reflect.DeepEqual(spec.Fields, want) {
		t.Fatalf("config fields\nwant: %#v\n got: %#v", want, spec.Fields)
	}
}

func TestGetLaunchCommandAppendsConfiguredModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "autohand"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Config: ports.AgentConfig{Model: "  anthropic/claude-sonnet-4-20250514  "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(cmd, "--model") {
		t.Fatalf("command %#v missing --model flag", cmd)
	}
	want := []string{"autohand", "--model", "anthropic/claude-sonnet-4-20250514"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandOmitsBlankConfiguredModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "autohand"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Config: ports.AgentConfig{Model: " \t "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if contains(cmd, "--model") {
		t.Fatalf("command %#v contains --model for blank model", cmd)
	}
}

func TestGetRestoreCommandAppendsConfiguredModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "autohand"}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Config: ports.AgentConfig{Model: "  anthropic/claude-sonnet-4-20250514  "},
		Session: ports.SessionRef{
			WorkspacePath: "/work/space",
			Metadata:      map[string]string{ports.MetadataKeyAgentSessionID: "sess-123"},
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{"autohand", "resume", "--path", "/work/space", "--model", "anthropic/claude-sonnet-4-20250514", "sess-123"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore cmd\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetRestoreCommandReadsAgentSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "autohand"}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Permissions:  ports.PermissionModeAuto,
		SystemPrompt: "restore instructions ignored by resume",
		Session: ports.SessionRef{
			WorkspacePath: "/work/space",
			Metadata:      map[string]string{ports.MetadataKeyAgentSessionID: "sess-123"},
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{"autohand", "resume", "--path", "/work/space", "sess-123"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore cmd\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetRestoreCommandFalseWithoutAgentSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "autohand"}

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
				Permissions: ports.PermissionModeAuto,
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
	plugin := &Plugin{resolvedBinary: "autohand"}

	info, ok, err := plugin.SessionInfo(context.Background(), ports.SessionRef{
		WorkspacePath: "/some/path",
		Metadata: map[string]string{
			ports.MetadataKeyAgentSessionID: "sess-123",
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
	if info.AgentSessionID != "sess-123" {
		t.Fatalf("AgentSessionID = %q, want native id", info.AgentSessionID)
	}
	if info.Title != "Fix login redirect" {
		t.Fatalf("Title = %q, want hook title", info.Title)
	}
	if info.Summary != "Updated the auth callback and tests." {
		t.Fatalf("Summary = %q, want hook summary", info.Summary)
	}
	if info.Metadata != nil {
		t.Fatalf("Metadata = %#v, want nil", info.Metadata)
	}
}

func TestSessionInfoFalseWhenNoHookMetadata(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "autohand"}

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

func TestContextCancellationIsRespected(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "autohand"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := plugin.GetConfigSpec(ctx); err == nil {
		t.Fatal("GetConfigSpec: want context error")
	}
	if _, err := plugin.GetPromptDeliveryStrategy(ctx, ports.LaunchConfig{}); err == nil {
		t.Fatal("GetPromptDeliveryStrategy: want context error")
	}
	if _, _, err := plugin.GetRestoreCommand(ctx, ports.RestoreConfig{}); err == nil {
		t.Fatal("GetRestoreCommand: want context error")
	}
	if _, _, err := plugin.SessionInfo(ctx, ports.SessionRef{}); err == nil {
		t.Fatal("SessionInfo: want context error")
	}
	if err := plugin.GetAgentHooks(ctx, ports.WorkspaceHookConfig{}); err == nil {
		t.Fatal("GetAgentHooks: want context error")
	}
	// resolvedBinary is set, so this exercises the cached-binary path, which
	// must still honor cancellation.
	if _, err := plugin.GetLaunchCommand(ctx, ports.LaunchConfig{}); err == nil {
		t.Fatal("GetLaunchCommand: want context error")
	}
}

// TestGetAgentHooksPreservesUnknownEntryFields locks the round-trip behavior:
// keys AO does not model on a user hook entry (here "async") must survive a
// GetAgentHooks rewrite instead of being silently dropped.
func TestGetAgentHooksPreservesUnknownEntryFields(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "autohand"}
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("AUTOHAND_CONFIG", configPath)

	existing := `{
  "hooks": {
    "enabled": false,
    "hooks": [
      {"event": "stop", "command": "~/.autohand/hooks/sound-alert.sh", "description": "user hook", "enabled": true, "async": true, "filter": {"glob": "*.go"}}
    ]
  }
}`
	if err := os.WriteFile(configPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: t.TempDir()}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var top struct {
		Hooks struct {
			Hooks []map[string]json.RawMessage `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}

	var userEntry map[string]json.RawMessage
	for _, entry := range top.Hooks.Hooks {
		if string(entry["command"]) == `"~/.autohand/hooks/sound-alert.sh"` {
			userEntry = entry
			break
		}
	}
	if userEntry == nil {
		t.Fatalf("user hook entry not found in %s", data)
	}
	if string(userEntry["async"]) != "true" {
		t.Fatalf("unknown field async dropped: %s", data)
	}
	filterRaw, ok := userEntry["filter"]
	if !ok {
		t.Fatalf("unknown field filter dropped: %s", data)
	}
	var filter map[string]string
	if err := json.Unmarshal(filterRaw, &filter); err != nil {
		t.Fatalf("filter not valid json: %v (%s)", err, filterRaw)
	}
	if filter["glob"] != "*.go" {
		t.Fatalf("unknown field filter not preserved: got %v in %s", filter, data)
	}
}

func TestDeriveActivityState(t *testing.T) {
	tests := []struct {
		name   string
		event  string
		want   domain.ActivityState
		wantOK bool
	}{
		{"session start -> active", "session-start", domain.ActivityActive, true},
		{"user prompt -> active", "user-prompt-submit", domain.ActivityActive, true},
		{"stop -> idle", "stop", domain.ActivityIdle, true},
		{"permission request -> waiting_input", "permission-request", domain.ActivityWaitingInput, true},
		{"unknown event -> no signal", "frobnicate", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := activitystate.StandardDeriveActivityState(tt.event, []byte(`{}`))
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("StandardDeriveActivityState(%q) = (%q, %v), want (%q, %v)",
					tt.event, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestGetAgentHooksInstallsAndPreservesConfig(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "autohand"}
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("AUTOHAND_CONFIG", configPath)

	// Seed a config with unrelated keys plus a user hook; both must survive.
	existing := `{
  "provider": "openai",
  "auth": {"token": "keep-me"},
  "hooks": {
    "enabled": false,
    "hooks": [
      {"event": "stop", "command": "~/.autohand/hooks/sound-alert.sh", "description": "user hook", "enabled": true, "async": true}
    ]
  }
}`
	if err := os.WriteFile(configPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := ports.WorkspaceHookConfig{
		DataDir:       t.TempDir(),
		SessionID:     "sess-1",
		WorkspacePath: t.TempDir(),
	}
	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	// A second install must not duplicate AO hook commands.
	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	// Unrelated top-level config keys are preserved.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	if string(top["provider"]) != `"openai"` {
		t.Fatalf("provider not preserved: %s", top["provider"])
	}
	if _, ok := top["auth"]; !ok {
		t.Fatalf("auth block dropped: %s", data)
	}

	_, hooksSection, entries := mustReadHooks(t, configPath)
	if string(hooksSection["enabled"]) != "true" {
		t.Fatalf("hooks.enabled = %s, want true", hooksSection["enabled"])
	}

	for _, spec := range autohandManagedHooks {
		command := autohandHookCommandPrefix + spec.Subcommand
		if got := countCommand(entries, command); got != 1 {
			t.Fatalf("command %q count = %d, want 1 in %#v", command, got, entries)
		}
	}
	if countCommand(entries, "~/.autohand/hooks/sound-alert.sh") != 1 {
		t.Fatalf("user hook not preserved: %#v", entries)
	}

	if installed, err := plugin.AreHooksInstalled(context.Background(), ""); err != nil || !installed {
		t.Fatalf("AreHooksInstalled after install = (%v, %v), want (true, nil)", installed, err)
	}
}

func TestUninstallHooksRemovesOnlyAOHooks(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "autohand"}
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("AUTOHAND_CONFIG", configPath)

	existing := `{
  "hooks": {
    "enabled": false,
    "hooks": [
      {"event": "stop", "command": "~/.autohand/hooks/sound-alert.sh", "description": "user hook", "enabled": true}
    ]
  }
}`
	if err := os.WriteFile(configPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	cfg := ports.WorkspaceHookConfig{DataDir: t.TempDir(), SessionID: "sess-1", WorkspacePath: t.TempDir()}
	if err := plugin.GetAgentHooks(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if installed, err := plugin.AreHooksInstalled(ctx, ""); err != nil || !installed {
		t.Fatalf("AreHooksInstalled after install = (%v, %v), want (true, nil)", installed, err)
	}

	if err := plugin.UninstallHooks(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if installed, err := plugin.AreHooksInstalled(ctx, ""); err != nil || installed {
		t.Fatalf("AreHooksInstalled after uninstall = (%v, %v), want (false, nil)", installed, err)
	}

	_, _, entries := mustReadHooks(t, configPath)
	for _, spec := range autohandManagedHooks {
		command := autohandHookCommandPrefix + spec.Subcommand
		if got := countCommand(entries, command); got != 0 {
			t.Fatalf("command %q count = %d after uninstall, want 0", command, got)
		}
	}
	if countCommand(entries, "~/.autohand/hooks/sound-alert.sh") != 1 {
		t.Fatalf("user hook not preserved after uninstall: %#v", entries)
	}
}

func TestUninstallHooksMissingFileIsNoOp(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "autohand"}
	configPath := filepath.Join(t.TempDir(), "missing", "config.json")
	t.Setenv("AUTOHAND_CONFIG", configPath)

	if err := plugin.UninstallHooks(context.Background(), ""); err != nil {
		t.Fatalf("UninstallHooks on missing file = %v, want nil", err)
	}
	if installed, err := plugin.AreHooksInstalled(context.Background(), ""); err != nil || installed {
		t.Fatalf("AreHooksInstalled on missing file = (%v, %v), want (false, nil)", installed, err)
	}
}

func TestGetAgentHooksCreatesConfigWhenAbsent(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "autohand"}
	configPath := filepath.Join(t.TempDir(), "nested", "config.json")
	t.Setenv("AUTOHAND_CONFIG", configPath)

	if err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	_, hooksSection, entries := mustReadHooks(t, configPath)
	if string(hooksSection["enabled"]) != "true" {
		t.Fatalf("hooks.enabled = %s, want true", hooksSection["enabled"])
	}
	if len(entries) != len(autohandManagedHooks) {
		t.Fatalf("entry count = %d, want %d", len(entries), len(autohandManagedHooks))
	}
}

func mustReadHooks(t *testing.T, configPath string) (map[string]json.RawMessage, map[string]json.RawMessage, []autohandHookEntry) {
	t.Helper()
	top, section, entries, err := readAutohandHooks(configPath)
	if err != nil {
		t.Fatalf("readAutohandHooks: %v", err)
	}
	return top, section, entries
}

func countCommand(entries []autohandHookEntry, command string) int {
	count := 0
	for _, entry := range entries {
		if entry.Command == command {
			count++
		}
	}
	return count
}

func contains(values []string, needle string) bool {
	for _, v := range values {
		if v == needle {
			return true
		}
	}
	return false
}
