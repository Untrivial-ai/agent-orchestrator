package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestNativeConversationIDRequiresCapturedCodexThreadForTUI(t *testing.T) {
	p := &Plugin{}
	if id, ok, err := p.NativeConversationID(context.Background(), ports.SessionRef{
		ID: "ao-session-1", Metadata: map[string]string{},
	}, domain.SessionModeTUI, ""); err != nil || ok || id != "" {
		t.Fatalf("uncaptured TUI native id = %q ok=%v err=%v", id, ok, err)
	}
	tuiID, ok, err := p.NativeConversationID(context.Background(), ports.SessionRef{
		ID:       "ao-session-1",
		Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "codex-thread-1"},
	}, domain.SessionModeTUI, "")
	if err != nil || !ok || tuiID != "codex-thread-1" {
		t.Fatalf("captured TUI native id = %q ok=%v err=%v", tuiID, ok, err)
	}
	chatID, ok, err := p.NativeConversationID(context.Background(), ports.SessionRef{},
		domain.SessionModeChat, tuiID)
	if err != nil || !ok || chatID != tuiID {
		t.Fatalf("Chat native id = %q ok=%v err=%v", chatID, ok, err)
	}
}

func TestNativeConversationExistsRequiresActivePersistedCodexRollout(t *testing.T) {
	p := &Plugin{}
	id := "019fc430-1234-7abc-8def-0123456789ab"
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", t.TempDir())
	env := map[string]string{"CODEX_HOME": codexHome}

	exists, err := p.NativeConversationExists(context.Background(), ports.SessionRef{}, id, env)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("reserved Codex thread without a rollout reported as persisted")
	}

	activeDir := filepath.Join(codexHome, "sessions", "2026", "08", "08")
	if err := os.MkdirAll(activeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(activeDir, "rollout-2026-08-08T10-00-00-"+id+".jsonl")
	if err := os.WriteFile(rollout, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	exists, err = p.NativeConversationExists(context.Background(), ports.SessionRef{}, id, env)
	if err != nil || exists {
		t.Fatalf("empty rollout: exists=%v err=%v", exists, err)
	}
	if err := os.WriteFile(rollout, []byte("{\"type\":\"session_meta\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exists, err = p.NativeConversationExists(context.Background(), ports.SessionRef{}, id, env)
	if err != nil || !exists {
		t.Fatalf("active rollout: exists=%v err=%v", exists, err)
	}

	if err := os.Remove(rollout); err != nil {
		t.Fatal(err)
	}
	archivedDir := filepath.Join(codexHome, "archived_sessions")
	if err := os.MkdirAll(archivedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	archived := filepath.Join(archivedDir, "rollout-2026-08-08T10-00-00-"+id+".jsonl")
	if err := os.WriteFile(archived, []byte("{\"type\":\"session_meta\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exists, err = p.NativeConversationExists(context.Background(), ports.SessionRef{}, id, env)
	if err != nil || exists {
		t.Fatalf("archived rollout: exists=%v err=%v", exists, err)
	}

	compressed := rollout + ".zst"
	if err := os.WriteFile(compressed, []byte("compressed"), 0o600); err != nil {
		t.Fatal(err)
	}
	exists, err = p.NativeConversationExists(context.Background(), ports.SessionRef{}, id, env)
	if err != nil || !exists {
		t.Fatalf("compressed active rollout: exists=%v err=%v", exists, err)
	}
}

// canonicalTempDir returns a t.TempDir() with symlinks resolved so the
// workspace trust flag collapses to a single predictable entry (macOS TempDir
// lives under a /var -> /private/var symlink).
func canonicalTempDir(t *testing.T) string {
	t.Helper()
	raw := t.TempDir()
	dir, err := filepath.EvalSymlinks(raw)
	if err != nil {
		if os.IsPermission(err) {
			return raw
		}
		t.Fatal(err)
	}
	return dir
}

// sessionHookFlags mirrors the `-c` hook config appendSessionHookFlags emits,
// asserted literally so accidental format drift fails loudly: Codex parses
// these values as TOML. The absolute AO executable is load-bearing because
// Codex invokes hooks through a login shell, which may replace PATH.
func sessionHookFlags(t *testing.T) []string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(executable) {
		executable, err = filepath.Abs(executable)
		if err != nil {
			t.Fatal(err)
		}
	}
	if runtime.GOOS == "windows" {
		executable = `"` + executable + `"`
	} else {
		executable = `'` + strings.ReplaceAll(executable, `'`, `'"'"'`) + `'`
	}
	prefix := executable + " hooks codex "
	return []string{
		"-c", `hooks.SessionStart=[{hooks=[{type="command",command=` + codexTOMLBasicString(prefix+"session-start") + `,timeout=5}]}]`,
		"-c", `hooks.UserPromptSubmit=[{hooks=[{type="command",command=` + codexTOMLBasicString(prefix+"user-prompt-submit") + `,timeout=5}]}]`,
		"-c", `hooks.PermissionRequest=[{hooks=[{type="command",command=` + codexTOMLBasicString(prefix+"permission-request") + `,timeout=5}]}]`,
		"-c", `hooks.PreToolUse=[{matcher="^request_user_input$",hooks=[{type="command",command=` + codexTOMLBasicString(prefix+"pre-tool-use") + `,timeout=5}]}]`,
		"-c", `hooks.PostToolUse=[{matcher="^request_user_input$",hooks=[{type="command",command=` + codexTOMLBasicString(prefix+"post-tool-use") + `,timeout=5}]}]`,
		"-c", `hooks.Stop=[{hooks=[{type="command",command=` + codexTOMLBasicString(prefix+"stop") + `,timeout=5}]}]`,
	}
}

func TestExitDetectionUsesAOProcessSupervisor(t *testing.T) {
	plugin := &Plugin{}
	if got := plugin.ExitDetectionMode(); got != ports.AgentExitDetectionSupervisor {
		t.Fatalf("exit detection mode = %q, want %q", got, ports.AgentExitDetectionSupervisor)
	}
}

func TestGetLaunchCommandBuildsCrossPlatformArgv(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "codex"}
	workspace := canonicalTempDir(t)

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions:      ports.PermissionModeBypassPermissions,
		Prompt:           "-fix this",
		SystemPromptFile: filepath.Join("tmp", "prompt with spaces.md"),
		SystemPrompt:     "inline wins",
		WorkspacePath:    workspace,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"codex",
		"-c", "check_for_update_on_startup=false",
		"-c", "notice.hide_rate_limit_model_nudge=true",
		"-c", "features.default_mode_request_user_input=true",
		"--dangerously-bypass-hook-trust",
		"--dangerously-bypass-approvals-and-sandbox",
	}
	want = append(want, sessionHookFlags(t)...)
	if runtime.GOOS == "windows" {
		want = append(want, "--no-alt-screen")
	}
	want = append(want,
		"-c", `projects={`+codexTOMLConfigString(workspace)+`={trust_level="trusted"}}`,
		"-c", "developer_instructions="+codexTOMLConfigString("inline wins"),
		"--", "-fix this",
	)
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandWithoutWorkspaceOmitsTrustFlag(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "codex"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range cmd {
		if strings.HasPrefix(arg, "projects=") {
			t.Fatalf("command %#v contains a projects trust flag without a workspace", cmd)
		}
	}
	if !containsSubsequence(cmd, sessionHookFlags(t)) {
		t.Fatalf("command %#v missing session hook flags", cmd)
	}
}

func TestGetLaunchCommandAppendsConfiguredModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "codex"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Config: ports.AgentConfig{Model: "  gpt-5.4-mini  "},
		Prompt: "fix this",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubsequence(cmd, []string{"--model", "gpt-5.4-mini"}) {
		t.Fatalf("command %#v missing trimmed --model flag", cmd)
	}
	if containsSubsequence(cmd, []string{"--model", "  gpt-5.4-mini  "}) {
		t.Fatalf("command %#v used untrimmed model", cmd)
	}
}

func TestGetLaunchCommandOmitsBlankConfiguredModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "codex"}

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

func TestResolveCodexBinaryFindsNVMInstallWhenPathIsSparse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("NVM install discovery is Unix-specific")
	}
	home := t.TempDir()
	binDir := filepath.Join(home, ".nvm", "versions", "node", "v20.19.4", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(binDir, "codex")
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	origFileExists := fileExists
	fileExists = func(path string) bool {
		return strings.HasPrefix(path, home+string(os.PathSeparator)) && origFileExists(path)
	}
	t.Cleanup(func() {
		fileExists = origFileExists
	})

	got, err := ResolveCodexBinary(context.Background())
	if err != nil {
		t.Fatalf("ResolveCodexBinary: %v", err)
	}
	if got != want {
		t.Fatalf("ResolveCodexBinary = %q, want %q", got, want)
	}
}

func TestResolveCodexBinaryPrefersNPMOverWindowsAppsExecutable(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows resolver only")
	}
	root := t.TempDir()
	appData := filepath.Join(root, "Roaming")
	npmDir := filepath.Join(appData, "npm")
	want := filepath.Join(npmDir, "node_modules", "@openai", "codex", "node_modules", "@openai", "codex-win32-x64", "vendor", "x86_64-pc-windows-msvc", "bin", "codex.exe")
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("native codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(npmDir, "codex.cmd")
	if err := os.WriteFile(shim, []byte("@echo off\r\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	windowsApps := filepath.Join(root, "WindowsApps", "OpenAI.Codex_1.0.0.0_x64__test", "app", "resources")
	if err := os.MkdirAll(windowsApps, 0o755); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(windowsApps, "codex.exe")
	if err := os.WriteFile(blocked, []byte("blocked codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APPDATA", appData)
	t.Setenv("PATH", windowsApps)

	got, err := ResolveCodexBinary(context.Background())
	if err != nil {
		t.Fatalf("ResolveCodexBinary: %v", err)
	}
	if got != want {
		t.Fatalf("ResolveCodexBinary = %q, want %q", got, want)
	}
}

func TestGetLaunchCommandMapsApprovalModes(t *testing.T) {
	tests := []struct {
		name        string
		permission  ports.PermissionMode
		want        []string
		notExpected string
	}{
		{
			name:       "default",
			permission: ports.PermissionModeDefault,
			want:       []string{"--dangerously-bypass-approvals-and-sandbox"},
		},
		{
			name:        "accept-edits",
			permission:  ports.PermissionModeAcceptEdits,
			want:        []string{"--ask-for-approval", "on-request"},
			notExpected: "--dangerously-bypass-approvals-and-sandbox",
		},
		{
			name:        "auto",
			permission:  ports.PermissionModeAuto,
			want:        []string{"--ask-for-approval", "on-request", "-c", `approvals_reviewer="auto_review"`},
			notExpected: "--dangerously-bypass-approvals-and-sandbox",
		},
		{
			name:       "bypass-permissions",
			permission: ports.PermissionModeBypassPermissions,
			want:       []string{"--dangerously-bypass-approvals-and-sandbox"},
		},
		{
			name:       "empty",
			permission: "",
			want:       []string{"--dangerously-bypass-approvals-and-sandbox"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &Plugin{resolvedBinary: "codex"}
			cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
				Permissions: tt.permission,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(tt.want) > 0 && !containsSubsequence(cmd, tt.want) {
				t.Fatalf("command %#v does not contain %#v", cmd, tt.want)
			}
			if tt.notExpected != "" && contains(cmd, tt.notExpected) {
				t.Fatalf("command %#v contains %q", cmd, tt.notExpected)
			}
		})
	}
}

func TestAppendWorkspaceTrustFlagCoversLiteralAndResolvedPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs extra privileges on Windows")
	}
	base := canonicalTempDir(t)
	target := filepath.Join(base, "real")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	var cmd []string
	appendWorkspaceTrustFlag(&cmd, link)
	want := []string{
		"-c",
		`projects={'` + link + `'={trust_level="trusted"},'` + target + `'={trust_level="trusted"}}`,
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("trust flag\nwant: %#v\n got: %#v", want, cmd)
	}

	cmd = nil
	appendWorkspaceTrustFlag(&cmd, target)
	want = []string{"-c", `projects={'` + target + `'={trust_level="trusted"}}`}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("canonical-path trust flag\nwant: %#v\n got: %#v", want, cmd)
	}

	cmd = nil
	appendWorkspaceTrustFlag(&cmd, "   ")
	if cmd != nil {
		t.Fatalf("blank workspace produced %#v, want no flag", cmd)
	}
}

func TestCodexTOMLBasicStringEscapes(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"plain", "\"plain\""},
		{"C:\\Users\\dev", "\"C:\\\\Users\\\\dev\""},
		{"with \"quotes\"", "\"with \\\"quotes\\\"\""},
		{"tab\there", "\"tab\\u0009here\""},
	}
	for _, tt := range tests {
		if got := codexTOMLBasicString(tt.in); got != tt.want {
			t.Fatalf("codexTOMLBasicString(%q) = %s, want %s", tt.in, got, tt.want)
		}
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
			Description: "Model override passed to `codex --model`.",
		},
	}
	if !reflect.DeepEqual(spec.Fields, want) {
		t.Fatalf("config fields\nwant: %#v\n got: %#v", want, spec.Fields)
	}
}

// legacyHooksJSON builds a hooks.json in the shape older AO versions wrote:
// AO-managed entries plus one user-defined Stop hook.
func legacyHooksJSON() string {
	return `{
  "hooks": {
    "Stop": [
      {"matcher": null, "hooks": [
        {"type": "command", "command": "custom stop hook", "timeout": 3},
        {"type": "command", "command": "ao hooks codex stop", "timeout": 30}
      ]}
    ],
    "UserPromptSubmit": [
      {"matcher": null, "hooks": [
        {"type": "command", "command": "ao hooks codex user-prompt-submit", "timeout": 30}
      ]}
    ]
  },
  "unmanagedKey": {"keep": true}
}`
}

func TestGetAgentHooksWritesNothingIntoFreshWorkspace(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "codex"}
	workspace := t.TempDir()

	cfg := ports.WorkspaceHookConfig{
		DataDir:       t.TempDir(),
		SessionID:     "sess-1",
		WorkspacePath: workspace,
	}
	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(workspace, codexHooksDirName)); !os.IsNotExist(err) {
		t.Fatalf(".codex dir state = %v, want not-exist: hooks ride the launch command", err)
	}
}

func TestGetAgentHooksRequiresWorkspacePath(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "codex"}
	err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: "  "})
	if err == nil {
		t.Fatal("expected error for blank WorkspacePath")
	}
}

func TestGetAgentHooksStripsLegacyAOEntries(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "codex"}
	workspace := t.TempDir()
	hooksPath := filepath.Join(workspace, codexHooksDirName, codexHooksFileName)
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, []byte(legacyHooksJSON()), 0o644); err != nil {
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

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	var config codexHookFile
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	for _, spec := range codexManagedHooks {
		if got := countCodexHookCommand(config.Hooks[spec.Event], spec.Command); got != 0 {
			t.Fatalf("%s command %q count = %d after cleanup, want 0", spec.Event, spec.Command, got)
		}
	}
	if countCodexHookCommand(config.Hooks["Stop"], "custom stop hook") != 1 {
		t.Fatalf("user Stop hook not preserved: %#v", config.Hooks["Stop"])
	}
	if _, ok := config.Hooks["UserPromptSubmit"]; ok {
		t.Fatalf("UserPromptSubmit left behind after its only entry was AO's: %#v", config.Hooks)
	}
	if !strings.Contains(string(data), "unmanagedKey") {
		t.Fatalf("top-level keys AO doesn't manage were dropped: %s", data)
	}
}

func TestGetAgentHooksLeavesFilesWithoutAOEntriesUntouched(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "codex"}
	workspace := t.TempDir()
	hooksPath := filepath.Join(workspace, codexHooksDirName, codexHooksFileName)
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := `{"hooks":{"Stop":[{"matcher":null,"hooks":[{"type":"command","command":"custom stop hook","timeout":3}]}]}}`
	if err := os.WriteFile(hooksPath, []byte(seed), 0o644); err != nil {
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

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != seed {
		t.Fatalf("user-only hooks.json was rewritten\n--- before ---\n%s\n--- after ---\n%s", seed, data)
	}
}

func TestUninstallHooksRemovesLegacyCodexHooks(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "codex"}
	workspace := t.TempDir()
	hooksPath := filepath.Join(workspace, codexHooksDirName, codexHooksFileName)

	ctx := context.Background()

	// Missing file is a no-op.
	if err := plugin.UninstallHooks(ctx, workspace); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, []byte(legacyHooksJSON()), 0o644); err != nil {
		t.Fatal(err)
	}

	if installed, err := plugin.AreHooksInstalled(ctx, workspace); err != nil || !installed {
		t.Fatalf("AreHooksInstalled with legacy entries = (%v, %v), want (true, nil)", installed, err)
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
	var config codexHookFile
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	for _, spec := range codexManagedHooks {
		if got := countCodexHookCommand(config.Hooks[spec.Event], spec.Command); got != 0 {
			t.Fatalf("%s command %q count = %d after uninstall, want 0", spec.Event, spec.Command, got)
		}
	}
	if countCodexHookCommand(config.Hooks["Stop"], "custom stop hook") != 1 {
		t.Fatalf("user Stop hook not preserved: %#v", config.Hooks["Stop"])
	}
}

func TestGetRestoreCommandReadsAgentSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "codex"}
	workspace := canonicalTempDir(t)

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Permissions:      ports.PermissionModeAuto,
		SystemPrompt:     "restore inline wins",
		SystemPromptFile: filepath.Join("tmp", "restore-system.md"),
		Session: ports.SessionRef{
			Metadata:      map[string]string{ports.MetadataKeyAgentSessionID: "thread-123"},
			WorkspacePath: workspace,
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{
		"codex",
		"resume",
		"-c", "check_for_update_on_startup=false",
		"-c", "notice.hide_rate_limit_model_nudge=true",
		"-c", "features.default_mode_request_user_input=true",
		"--dangerously-bypass-hook-trust",
		"--ask-for-approval", "on-request",
		"-c", `approvals_reviewer="auto_review"`,
	}
	want = append(want, sessionHookFlags(t)...)
	if runtime.GOOS == "windows" {
		want = append(want, "--no-alt-screen")
	}
	want = append(want,
		"-c", `projects={`+codexTOMLConfigString(workspace)+`={trust_level="trusted"}}`,
		"-c", "developer_instructions="+codexTOMLConfigString("restore inline wins"),
		"thread-123",
	)
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore cmd\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetRestoreCommandAppendsConfiguredModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "codex"}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Config: ports.AgentConfig{Model: "  gpt-5.4-mini  "},
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "thread-123"},
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if !containsSubsequence(cmd, []string{"--model", "gpt-5.4-mini"}) {
		t.Fatalf("restore command %#v missing trimmed --model flag", cmd)
	}
}

func TestGetRestoreCommandFalseWithoutAgentSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "codex"}

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
	plugin := &Plugin{resolvedBinary: "codex"}

	info, ok, err := plugin.SessionInfo(context.Background(), ports.SessionRef{
		WorkspacePath: "/some/path",
		Metadata: map[string]string{
			ports.MetadataKeyAgentSessionID: "thread-123",
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
	if info.AgentSessionID != "thread-123" {
		t.Fatalf("AgentSessionID = %q, want native id", info.AgentSessionID)
	}
	if info.Title != "Fix login redirect" {
		t.Fatalf("Title = %q, want hook title", info.Title)
	}
	if info.Summary != "Updated the auth callback and tests." {
		t.Fatalf("Summary = %q, want hook summary", info.Summary)
	}
	if info.Metadata != nil {
		t.Fatalf("Metadata = %#v, want nil for Codex", info.Metadata)
	}
}

func TestSessionInfoFalseWhenNoHookMetadata(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "codex"}

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

func countCodexHookCommand(entries []codexMatcherGroup, command string) int {
	count := 0
	for _, entry := range entries {
		for _, hook := range entry.Hooks {
			if hook.Command == command {
				count++
			}
		}
	}
	return count
}

func TestDoctorLaunchProbesMirrorLaunchFlags(t *testing.T) {
	probes := DoctorLaunchProbes()
	if len(probes) != 2 {
		t.Fatalf("probes = %d, want 2", len(probes))
	}
	if !reflect.DeepEqual(probes[0], []string{"--dangerously-bypass-hook-trust", "--version"}) {
		t.Fatalf("flag probe = %#v", probes[0])
	}
	override := probes[1]
	if len(override) < 2 || override[0] != "features" || override[1] != "list" {
		t.Fatalf("override probe must ride `features list`, got %#v", override)
	}
	joined := strings.Join(override, " ")
	for _, want := range []string{
		"features.default_mode_request_user_input=true",
		"hooks.SessionStart=", "hooks.UserPromptSubmit=", "hooks.PermissionRequest=",
		"hooks.PreToolUse=", "hooks.PostToolUse=", "hooks.Stop=",
		"notice.hide_rate_limit_model_nudge=true",
		`projects={`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("override probe missing %q in %s", want, joined)
		}
	}
}

func TestRequestUserInputHooksUseExactMatcher(t *testing.T) {
	wantCommands := map[string]string{
		"PreToolUse":  codexHookCommandPrefix + "pre-tool-use",
		"PostToolUse": codexHookCommandPrefix + "post-tool-use",
	}
	for event, wantCommand := range wantCommands {
		var matcher string
		var command string
		for _, spec := range codexManagedHooks {
			if spec.Event == event {
				matcher = spec.Matcher
				command = spec.Command
				break
			}
		}
		if matcher != `^request_user_input$` {
			t.Fatalf("%s matcher = %q, want exact request_user_input matcher", event, matcher)
		}
		for _, tool := range []string{"request_user_input", "shell", "mcp__request_user_input", "request_user_input_extra"} {
			matched, err := regexp.MatchString(matcher, tool)
			if err != nil {
				t.Fatal(err)
			}
			if matched != (tool == "request_user_input") {
				t.Fatalf("%s matcher against %q = %v", event, tool, matched)
			}
		}
		if command != wantCommand {
			t.Fatalf("%s command = %q, want lifecycle event %q", event, command, wantCommand)
		}
	}
}
