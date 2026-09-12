package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestResolveCursorBinaryFindsWinGetLinkOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows install location")
	}
	localAppData := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("APPDATA", "")
	t.Setenv("LOCALAPPDATA", localAppData)
	t.Setenv("USERPROFILE", t.TempDir())
	want := filepath.Join(localAppData, "Microsoft", "WinGet", "Links", "cursor-agent.exe")
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveCursorBinary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("ResolveCursorBinary() = %q, want %q", got, want)
	}
}

func TestGetLaunchCommandBuildsArgv(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cursor-agent"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions:      ports.PermissionModeBypassPermissions,
		Prompt:           "-fix this",
		SystemPromptFile: filepath.Join("tmp", "prompt with spaces.md"),
		SystemPrompt:     "ignored",
	})
	if err != nil {
		t.Fatal(err)
	}

	// System prompt is never injected via a flag for cursor; the prompt is
	// positional and last, guarded by a `--` end-of-options sentinel so a
	// leading "-" is not parsed as a flag.
	want := []string{
		"cursor-agent",
		"--yolo",
		"--", "-fix this",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandOmitsPromptWhenEmpty(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cursor-agent"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions: ports.PermissionModeDefault,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"cursor-agent"}
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
			notExpected: []string{"--force", "--yolo"},
		},
		{
			name:        "accept-edits",
			permission:  ports.PermissionModeAcceptEdits,
			notExpected: []string{"--force", "--yolo"},
		},
		{
			name:        "auto",
			permission:  ports.PermissionModeAuto,
			want:        []string{"--force"},
			notExpected: []string{"--yolo"},
		},
		{
			name:        "bypass-permissions",
			permission:  ports.PermissionModeBypassPermissions,
			want:        []string{"--yolo"},
			notExpected: []string{"--force"},
		},
		{
			name:        "unknown falls back to default",
			permission:  "",
			notExpected: []string{"--force", "--yolo"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &Plugin{resolvedBinary: "cursor-agent"}
			cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
				Permissions: tt.permission,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(tt.want) > 0 && !containsSubsequence(cmd, tt.want) {
				t.Fatalf("command %#v does not contain %#v", cmd, tt.want)
			}
			for _, ne := range tt.notExpected {
				if contains(cmd, ne) {
					t.Fatalf("command %#v unexpectedly contains %q", cmd, ne)
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

func TestGetConfigSpecReportsModel(t *testing.T) {
	plugin := &Plugin{}

	spec, err := plugin.GetConfigSpec(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Fields) != 1 || spec.Fields[0].Key != "model" {
		t.Fatalf("unexpected config fields: %#v", spec.Fields)
	}
}

func TestGetLaunchCommandForwardsModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cursor-agent"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{Config: ports.AgentConfig{Model: "  claude-4.6-sonnet  "}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"cursor-agent", "--model", "claude-4.6-sonnet"}; !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandReadsAgentSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cursor-agent"}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Permissions: ports.PermissionModeAuto,
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "chat-123"},
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{
		"cursor-agent",
		"--force",
		"--resume", "chat-123",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore cmd\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetRestoreCommandFalseWithoutAgentSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cursor-agent"}

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
	plugin := &Plugin{resolvedBinary: "cursor-agent"}

	info, ok, err := plugin.SessionInfo(context.Background(), ports.SessionRef{
		WorkspacePath: "/some/path",
		Metadata: map[string]string{
			ports.MetadataKeyAgentSessionID: "chat-123",
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
	if info.AgentSessionID != "chat-123" {
		t.Fatalf("AgentSessionID = %q, want native id", info.AgentSessionID)
	}
	if info.Title != "Fix login redirect" {
		t.Fatalf("Title = %q, want hook title", info.Title)
	}
	if info.Summary != "Updated the auth callback and tests." {
		t.Fatalf("Summary = %q, want hook summary", info.Summary)
	}
	if info.Metadata != nil {
		t.Fatalf("Metadata = %#v, want nil for Cursor", info.Metadata)
	}
}

func TestSessionInfoFalseWhenNoHookMetadata(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cursor-agent"}

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

func TestContextCancellationPerMethod(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cursor-agent"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := plugin.GetConfigSpec(ctx); err == nil {
		t.Fatal("GetConfigSpec: want context error")
	}
	// GetLaunchCommand surfaces ctx cancellation only via binary resolution; with
	// a cached binary it short-circuits, so it is not asserted here (mirrors codex).
	if _, err := plugin.GetPromptDeliveryStrategy(ctx, ports.LaunchConfig{}); err == nil {
		t.Fatal("GetPromptDeliveryStrategy: want context error")
	}
	if _, _, err := plugin.GetRestoreCommand(ctx, ports.RestoreConfig{
		Session: ports.SessionRef{Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "chat-123"}},
	}); err == nil {
		t.Fatal("GetRestoreCommand: want context error")
	}
	if _, _, err := plugin.SessionInfo(ctx, ports.SessionRef{}); err == nil {
		t.Fatal("SessionInfo: want context error")
	}
	if err := plugin.GetAgentHooks(ctx, ports.WorkspaceHookConfig{WorkspacePath: t.TempDir()}); err == nil {
		t.Fatal("GetAgentHooks: want context error")
	}
	if err := plugin.UninstallHooks(ctx, t.TempDir()); err == nil {
		t.Fatal("UninstallHooks: want context error")
	}
	if _, err := plugin.AreHooksInstalled(ctx, t.TempDir()); err == nil {
		t.Fatal("AreHooksInstalled: want context error")
	}
}

func TestGetAgentHooksInstallsCursorHooks(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cursor-agent"}
	workspace := t.TempDir()
	hooksDir := filepath.Join(workspace, ".cursor")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(hooksDir, "hooks.json")
	// Pre-existing user hook on an event AO also manages, plus a non-AO field.
	existing := `{"version":1,"customField":"keep me","hooks":{"stop":[{"command":"custom stop hook"}]}}`
	if err := os.WriteFile(hooksPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := ports.WorkspaceHookConfig{
		DataDir:       t.TempDir(),
		SessionID:     "sess-1",
		WorkspacePath: workspace,
	}
	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	// A second install must not duplicate AO hook commands.
	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	var config cursorHookFile
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Hooks == nil {
		t.Fatalf("hooks config missing hooks object: %#v", config)
	}
	if config.Version != 1 {
		t.Fatalf("version = %d, want 1", config.Version)
	}
	for _, spec := range cursorManagedHooks {
		entries := config.Hooks[spec.Event]
		if count := countCursorHookCommand(entries, spec.Command); count != 1 {
			t.Fatalf("%s command %q count = %d, want 1 in %#v", spec.Event, spec.Command, count, entries)
		}
	}
	for _, event := range []string{"beforeShellExecution", "beforeMCPExecution"} {
		entries := config.Hooks[event]
		for _, entry := range entries {
			if isCursorManagedHook(entry.Command) && !entry.FailClosed {
				t.Fatalf("%s managed permission hook = %#v, want failClosed", event, entry)
			}
		}
	}
	stopEntries := config.Hooks["stop"]
	if countCursorHookCommand(stopEntries, "custom stop hook") != 1 {
		t.Fatalf("existing stop hook was not preserved: %#v", stopEntries)
	}
	// Unmanaged top-level fields must be preserved.
	if !strings.Contains(string(data), "keep me") {
		t.Fatalf("unmanaged field 'customField' was dropped: %s", data)
	}
	trustPath := cursorWorkspaceTrustPath(cursorDataDir(cfg.DataDir), workspace)
	trustData, err := os.ReadFile(trustPath)
	if err != nil {
		t.Fatalf("read trust marker: %v", err)
	}
	var trust cursorWorkspaceTrust
	if err := json.Unmarshal(trustData, &trust); err != nil {
		t.Fatalf("parse trust marker: %v", err)
	}
	if trust.WorkspacePath != workspace {
		t.Fatalf("trust workspacePath = %q, want %q", trust.WorkspacePath, workspace)
	}
	if trust.TrustMethod != "ao-session" {
		t.Fatalf("trustMethod = %q, want ao-session", trust.TrustMethod)
	}
	if trust.TrustedAt == "" {
		t.Fatal("trustedAt is empty")
	}
	if !trust.AOManaged {
		t.Fatal("aoManaged = false, want true")
	}
}

func TestGetAgentHooksMigratesLegacyPermissionCallbacksWithoutRewritingUserHooks(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cursor-agent"}
	workspace := t.TempDir()
	hooksDir := filepath.Join(workspace, ".cursor")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(hooksDir, "hooks.json")
	existing := `{"version":1,"hooks":{"beforeShellExecution":[` +
		`{"command":"ao hooks cursor permission-request"},` +
		`{"command":"custom permission hook","failClosed":true,"matcher":"git push","timeout":17},` +
		`{"type":"prompt","prompt":"Allow this command?","timeout":23}` +
		`]}}`
	if err := os.WriteFile(hooksPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		DataDir: t.TempDir(), SessionID: "sess-1", WorkspacePath: workspace,
	}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	var config cursorHookFile
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	entries := config.Hooks["beforeShellExecution"]
	if got := countCursorHookCommand(entries, cursorHookCommandPrefix+"permission-request"); got != 0 {
		t.Fatalf("legacy permission callback count = %d, want 0 in %#v", got, entries)
	}
	if got := countCursorHookCommand(entries, cursorHookCommandPrefix+"before-shell-execution"); got != 1 {
		t.Fatalf("current permission callback count = %d, want 1 in %#v", got, entries)
	}
	for _, entry := range entries {
		if entry.Command == "custom permission hook" && !entry.FailClosed {
			t.Fatalf("custom hook lost failClosed during migration: %#v", entry)
		}
	}

	var rawConfig struct {
		Hooks map[string][]map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(data, &rawConfig); err != nil {
		t.Fatal(err)
	}
	wantCustom := map[string]any{
		"command": "custom permission hook", "failClosed": true,
		"matcher": "git push", "timeout": float64(17),
	}
	wantPrompt := map[string]any{
		"type": "prompt", "prompt": "Allow this command?", "timeout": float64(23),
	}
	var gotCustom, gotPrompt map[string]any
	for _, entry := range rawConfig.Hooks["beforeShellExecution"] {
		if entry["command"] == "custom permission hook" {
			gotCustom = entry
		}
		if entry["type"] == "prompt" {
			gotPrompt = entry
		}
	}
	if !reflect.DeepEqual(gotCustom, wantCustom) {
		t.Fatalf("custom command hook = %#v, want %#v", gotCustom, wantCustom)
	}
	if !reflect.DeepEqual(gotPrompt, wantPrompt) {
		t.Fatalf("custom prompt hook = %#v, want %#v", gotPrompt, wantPrompt)
	}
}

func TestGetAgentHooksTrustSeedIsBestEffort(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cursor-agent"}
	if err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: t.TempDir()}); err != nil {
		t.Fatalf("GetAgentHooks returned trust seed error; want best-effort nil: %v", err)
	}
}

func TestAugmentRuntimeEnvUsesAODataDir(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cursor-agent"}
	env := map[string]string{cursorDataDirEnv: "/outside-ao"}
	dataDir := t.TempDir()

	plugin.AugmentRuntimeEnv(env, dataDir)

	if got, want := env[cursorDataDirEnv], cursorDataDir(dataDir); got != want {
		t.Fatalf("%s = %q, want %q", cursorDataDirEnv, got, want)
	}
}

func TestAugmentRuntimeEnvAdvertisesTerminalThemeHint(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "terminal-theme"), []byte("dark\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	(&Plugin{resolvedBinary: "cursor-agent"}).AugmentRuntimeEnv(env, dataDir)
	if env["TERM_THEME"] != "dark" {
		t.Fatalf("TERM_THEME = %q, want dark", env["TERM_THEME"])
	}
	if env["COLORFGBG"] != "15;0" {
		t.Fatalf("COLORFGBG = %q, want 15;0", env["COLORFGBG"])
	}
}

func TestGetAgentHooksUsesCursorDataDirOverride(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cursor-agent"}
	workspace := t.TempDir()
	cursorDataDir := cursorDataDir(t.TempDir())
	aoDataDir := t.TempDir()

	cfg := ports.WorkspaceHookConfig{
		DataDir:       aoDataDir,
		Env:           map[string]string{cursorDataDirEnv: cursorDataDir},
		SessionID:     "sess-1",
		WorkspacePath: workspace,
	}
	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	trustPath := cursorWorkspaceTrustPath(cursorDataDir, workspace)
	if _, err := os.Stat(trustPath); err != nil {
		t.Fatalf("trust marker under CURSOR_DATA_DIR = %v, want exists", err)
	}
	statePath := cursorWorkspaceTrustStatePath(cfg)
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("trust cleanup state = %v, want exists", err)
	}
}

func TestCleanupWorkspaceUsesRecordedTrustPathWhenEnvChanges(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cursor-agent"}
	workspace := t.TempDir()
	originalCursorDataDir := cursorDataDir(t.TempDir())
	newCursorDataDir := cursorDataDir(t.TempDir())
	aoDataDir := t.TempDir()

	installCfg := ports.WorkspaceHookConfig{
		DataDir:       aoDataDir,
		Env:           map[string]string{cursorDataDirEnv: originalCursorDataDir},
		SessionID:     "sess-1",
		WorkspacePath: workspace,
	}
	if err := plugin.GetAgentHooks(context.Background(), installCfg); err != nil {
		t.Fatal(err)
	}
	trustPath := cursorWorkspaceTrustPath(originalCursorDataDir, workspace)
	if _, err := os.Stat(trustPath); err != nil {
		t.Fatalf("original trust marker = %v, want exists", err)
	}

	cleanupCfg := installCfg
	cleanupCfg.Env = map[string]string{cursorDataDirEnv: newCursorDataDir}
	if err := plugin.CleanupWorkspace(context.Background(), cleanupCfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(trustPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original trust marker stat = %v, want removed", err)
	}
	if _, err := os.Stat(cursorWorkspaceTrustStatePath(installCfg)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("trust cleanup state stat = %v, want removed", err)
	}
}

func TestCleanupWorkspaceRemovesOnlyAOManagedTrustMarker(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cursor-agent"}
	workspace := t.TempDir()
	cfg := ports.WorkspaceHookConfig{DataDir: t.TempDir(), SessionID: "sess-1", WorkspacePath: workspace}
	trustPath := cursorWorkspaceTrustPath(cursorDataDir(cfg.DataDir), workspace)

	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := plugin.CleanupWorkspace(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(trustPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("AO trust marker stat = %v, want missing", err)
	}

	if err := os.MkdirAll(filepath.Dir(trustPath), 0o750); err != nil {
		t.Fatal(err)
	}
	userTrust := cursorWorkspaceTrust{
		TrustedAt:     "2026-01-02T03:04:05.000Z",
		WorkspacePath: workspace,
		TrustMethod:   "manual",
	}
	data, err := json.Marshal(userTrust)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trustPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := plugin.CleanupWorkspace(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(trustPath); err != nil {
		t.Fatalf("user trust marker stat = %v, want preserved", err)
	}
}

func TestUninstallHooksRemovesOnlyAOHooks(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cursor-agent"}
	workspace := t.TempDir()
	hooksPath := filepath.Join(workspace, ".cursor", "hooks.json")

	ctx := context.Background()
	cfg := ports.WorkspaceHookConfig{DataDir: t.TempDir(), SessionID: "sess-1", WorkspacePath: workspace}

	// Pre-seed user-owned hooks, including a command that shares AO's prefix;
	// every field must survive install and uninstall unchanged.
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"version":1,"hooks":{"stop":[` +
		`{"command":"custom stop hook"},` +
		`{"command":"ao hooks cursor custom-handler","matcher":"Shell","timeout":17},` +
		`{"type":"prompt","prompt":"Keep this prompt?","timeout":23}` +
		`]}}`
	if err := os.WriteFile(hooksPath, []byte(existing), 0o644); err != nil {
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

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	var config cursorHookFile
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	for _, spec := range cursorManagedHooks {
		if got := countCursorHookCommand(config.Hooks[spec.Event], spec.Command); got != 0 {
			t.Fatalf("%s command %q count = %d after uninstall, want 0", spec.Event, spec.Command, got)
		}
	}
	if countCursorHookCommand(config.Hooks["stop"], "custom stop hook") != 1 {
		t.Fatalf("user stop hook not preserved: %#v", config.Hooks["stop"])
	}

	var rawConfig struct {
		Hooks map[string][]map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(data, &rawConfig); err != nil {
		t.Fatal(err)
	}
	wantUserHooks := []map[string]any{
		{"command": "custom stop hook"},
		{"command": "ao hooks cursor custom-handler", "matcher": "Shell", "timeout": float64(17)},
		{"type": "prompt", "prompt": "Keep this prompt?", "timeout": float64(23)},
	}
	if got := rawConfig.Hooks["stop"]; !reflect.DeepEqual(got, wantUserHooks) {
		t.Fatalf("user hooks after uninstall = %#v, want %#v", got, wantUserHooks)
	}
}

func TestAreHooksInstalledFalseWhenNoFile(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cursor-agent"}
	workspace := t.TempDir()

	installed, err := plugin.AreHooksInstalled(context.Background(), workspace)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if installed {
		t.Fatal("installed = true, want false for missing file")
	}
}

func TestGetAgentHooksRequiresWorkspacePath(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cursor-agent"}
	if err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{}); err == nil {
		t.Fatal("want error for empty WorkspacePath")
	}
}

func TestCursorWorkspaceProjectName(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{
			path: "/Users/example/.ao/data/worktrees/project/session-1",
			want: "Users-example-ao-data-worktrees-project-session-1",
		},
		{
			path: "/Users/example/Library/Application Support/Cursor/workspace.json",
			want: "Users-example-Library-Application-Support-Cursor-workspace-json",
		},
		{
			path: "/tmp/with_underscores/and...dots",
			want: "tmp-with-underscores-and-dots",
		},
	}
	for _, tt := range tests {
		if got := cursorWorkspaceProjectName(tt.path); got != tt.want {
			t.Fatalf("project name for %q = %q, want %q", tt.path, got, tt.want)
		}
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

func containsSubsequence(values []string, needle []string) bool {
	if len(needle) == 0 {
		return true
	}

	for start := range values {
		if start+len(needle) > len(values) {
			return false
		}
		ok := true
		for offset, want := range needle {
			if values[start+offset] != want {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}

	return false
}

func countCursorHookCommand(entries []cursorHookEntry, command string) int {
	count := 0
	for _, hook := range entries {
		if hook.Command == command {
			count++
		}
	}
	return count
}
