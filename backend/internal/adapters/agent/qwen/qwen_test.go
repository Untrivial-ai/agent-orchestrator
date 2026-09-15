package qwen

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hooksjson"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestGetLaunchCommandBuildsArgv(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qwen"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions:  ports.PermissionModeBypassPermissions,
		Prompt:       "-fix this",
		SystemPrompt: "be terse",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"qwen",
		"--approval-mode", "yolo",
		"--append-system-prompt", "be terse",
		"-p", "-fix this",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandReadsSystemPromptFile(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qwen"}
	dir := t.TempDir()
	file := filepath.Join(dir, "system.md")
	if err := os.WriteFile(file, []byte("file instructions\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPromptFile: file,
		Prompt:           "do it",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"qwen",
		"--append-system-prompt", "file instructions\n",
		"-p", "do it",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandSystemPromptFileReadError(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qwen"}

	_, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPromptFile: filepath.Join(t.TempDir(), "missing.md"),
	})
	if err == nil {
		t.Fatal("expected error for missing system prompt file")
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
			name:        "default",
			permission:  ports.PermissionModeDefault,
			notExpected: "--approval-mode",
		},
		{
			name:       "accept-edits",
			permission: ports.PermissionModeAcceptEdits,
			want:       []string{"--approval-mode", "auto-edit"},
		},
		{
			name:       "auto",
			permission: ports.PermissionModeAuto,
			want:       []string{"--approval-mode", "auto"},
		},
		{
			name:       "bypass-permissions",
			permission: ports.PermissionModeBypassPermissions,
			want:       []string{"--approval-mode", "yolo"},
		},
		{
			name:        "empty falls back to default",
			permission:  "",
			notExpected: "--approval-mode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &Plugin{resolvedBinary: "qwen"}
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

func TestGetLaunchCommandWorkerStartsInteractive(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qwen"}
	workspace := t.TempDir()
	dataDir := t.TempDir()

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Kind:          domain.KindWorker,
		DataDir:       dataDir,
		WorkspacePath: workspace,
		Permissions:   ports.PermissionModeDefault,
		Prompt:        "-fix this",
		SessionID:     "repo/issue#42",
		SystemPrompt:  "be terse",
	})
	if err != nil {
		t.Fatal(err)
	}

	if runtime.GOOS == "windows" {
		want := []string{
			"qwen",
			"--append-system-prompt", "be terse",
			"-i", "-fix this",
		}
		if !reflect.DeepEqual(cmd, want) {
			t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
		}
		return
	}

	want := []string{
		"sh",
		"-lc",
	}
	if len(cmd) != 3 || !reflect.DeepEqual(cmd[:2], want) {
		t.Fatalf("unexpected command prefix\nwant: %#v\n got: %#v", want, cmd)
	}
	script := cmd[2]
	sessionKey := safeQwenSessionKey("repo/issue#42")
	for _, part := range []string{
		"umask 077; ",
		"mkdir -p ",
		filepath.Join(dataDir, "agent-runtime", "qwen", sessionKey, sessionKey+".input.jsonl"),
		sessionKey + ".input.jsonl",
		sessionKey + ".output.jsonl",
		`"type":"submit"`,
		`"text":"-fix this"`,
		`"session_start"`,
		"exec 'qwen' '--append-system-prompt' 'be terse' '--json-file'",
		"'--input-file'",
	} {
		if !strings.Contains(script, part) {
			t.Fatalf("worker script missing %q in: %s", part, script)
		}
	}
	if strings.Contains(script, "'-p'") || strings.Contains(script, "'-i'") {
		t.Fatalf("worker script must not use -p/-i prompt flags: %s", script)
	}
	if strings.Contains(script, workspace) || strings.Contains(script, ".qwen-remote-input") {
		t.Fatalf("worker script must not store remote-input files in workspace: %s", script)
	}
}

func TestSafeQwenSessionKeyDisambiguatesSanitizedCollisions(t *testing.T) {
	first := safeQwenSessionKey("a.b-1")
	second := safeQwenSessionKey("a_b-1")
	if first == second {
		t.Fatalf("safeQwenSessionKey collision: %q", first)
	}
	if first != "a_b-1-612e622d31" {
		t.Fatalf("first key = %q, want readable prefix plus raw hex suffix", first)
	}
	if second != "a_b-1-615f622d31" {
		t.Fatalf("second key = %q, want readable prefix plus raw hex suffix", second)
	}
}

func TestGetLaunchCommandWorkerRequiresDataDirForRemoteInput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows worker launch uses the native -i fallback")
	}
	plugin := &Plugin{resolvedBinary: "qwen"}

	_, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Kind:      domain.KindWorker,
		Prompt:    "fix it",
		SessionID: "repo-1",
	})
	if err == nil {
		t.Fatal("err = nil, want missing data dir error")
	}
	if !strings.Contains(err.Error(), "data dir is required") {
		t.Fatalf("err = %v, want data dir context", err)
	}
}

func TestGetLaunchCommandWorkerAppendsTrimmedConfiguredModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qwen"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Kind:        domain.KindWorker,
		DataDir:     t.TempDir(),
		SessionID:   "sess-123",
		Config:      domain.AgentConfig{Model: "  qwen-plus  "},
		Permissions: ports.PermissionModeBypassPermissions,
		Prompt:      "fix it",
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if !containsSubsequence(cmd, []string{"--model", "qwen-plus"}) {
			t.Fatalf("command %#v missing trimmed --model qwen-plus", cmd)
		}
		return
	}
	if len(cmd) != 3 || !strings.Contains(cmd[2], "'--model' 'qwen-plus'") {
		t.Fatalf("worker command %#v missing trimmed --model qwen-plus", cmd)
	}
}

func TestGetLaunchCommandOmitsBlankConfiguredModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qwen"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Config:      domain.AgentConfig{Model: "  "},
		Permissions: ports.PermissionModeBypassPermissions,
		Prompt:      "fix it",
	})
	if err != nil {
		t.Fatal(err)
	}
	if contains(cmd, "--model") {
		t.Fatalf("command %#v contains unexpected --model flag", cmd)
	}
}

func TestGetRestoreCommandAppendsConfiguredModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qwen"}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Config:      domain.AgentConfig{Model: "qwen-max"},
		Permissions: ports.PermissionModeAuto,
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "sess-123"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if !containsSubsequence(cmd, []string{"--model", "qwen-max"}) {
		t.Fatalf("command %#v missing --model qwen-max", cmd)
	}
}

func TestGetPromptDeliveryStrategy(t *testing.T) {
	plugin := &Plugin{}

	got, err := plugin.GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{Kind: domain.KindWorker, Prompt: "fix it"})
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.PromptDeliveryInCommand {
		t.Fatalf("worker strategy = %q, want %q", got, ports.PromptDeliveryInCommand)
	}

	got, err = plugin.GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.PromptDeliveryInCommand {
		t.Fatalf("default strategy = %q, want %q", got, ports.PromptDeliveryInCommand)
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
			Description: "Model override passed to `qwen --model`.",
		},
	}
	if !reflect.DeepEqual(spec.Fields, want) {
		t.Fatalf("config fields\nwant: %#v\n got: %#v", want, spec.Fields)
	}
}

func TestContextCancellationIsHonored(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qwen"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := plugin.GetConfigSpec(ctx); err == nil {
		t.Fatal("GetConfigSpec: want error from cancelled context")
	}
	if _, err := plugin.GetPromptDeliveryStrategy(ctx, ports.LaunchConfig{}); err == nil {
		t.Fatal("GetPromptDeliveryStrategy: want error from cancelled context")
	}
	if err := plugin.GetAgentHooks(ctx, ports.WorkspaceHookConfig{WorkspacePath: t.TempDir()}); err == nil {
		t.Fatal("GetAgentHooks: want error from cancelled context")
	}
	if err := plugin.UninstallHooks(ctx, t.TempDir()); err == nil {
		t.Fatal("UninstallHooks: want error from cancelled context")
	}
	if _, err := plugin.AreHooksInstalled(ctx, t.TempDir()); err == nil {
		t.Fatal("AreHooksInstalled: want error from cancelled context")
	}
	if _, _, err := plugin.GetRestoreCommand(ctx, ports.RestoreConfig{}); err == nil {
		t.Fatal("GetRestoreCommand: want error from cancelled context")
	}
	if _, _, err := plugin.SessionInfo(ctx, ports.SessionRef{}); err == nil {
		t.Fatal("SessionInfo: want error from cancelled context")
	}
}

type qwenHookFile struct {
	Hooks map[string][]hooksjson.MatcherGroup `json:"hooks"`
}

func TestGetAgentHooksInstallsQwenHooks(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qwen"}
	workspace := t.TempDir()
	settingsDir := filepath.Join(workspace, ".qwen")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(settingsDir, "settings.json")
	// Pre-seed an unrelated top-level setting and a user-owned Stop hook; both
	// must be preserved.
	existing := `{"theme":"dark","hooks":{"Stop":[{"hooks":[{"type":"command","command":"custom stop hook","timeout":3}]}]}}`
	if err := os.WriteFile(settingsPath, []byte(existing), 0o644); err != nil {
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

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}

	// Unrelated top-level setting survives.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	if string(top["theme"]) != `"dark"` {
		t.Fatalf("unrelated top-level setting not preserved: %s", top["theme"])
	}

	var config qwenHookFile
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Hooks == nil {
		t.Fatalf("hooks config missing hooks object: %#v", config)
	}
	for _, spec := range qwenManagedHooks {
		entries := config.Hooks[spec.Event]
		if count := countQwenHookCommand(entries, spec.Command); count != 1 {
			t.Fatalf("%s command count = %d, want 1 in %#v", spec.Event, count, entries)
		}
	}
	// User-owned Stop hook survives.
	if countQwenHookCommand(config.Hooks["Stop"], "custom stop hook") != 1 {
		t.Fatalf("existing Stop hook was not preserved: %#v", config.Hooks["Stop"])
	}
	// SessionStart lands under the startup/resume matcher.
	assertSessionStartMatcher(t, config.Hooks["SessionStart"])
}

func assertSessionStartMatcher(t *testing.T, groups []hooksjson.MatcherGroup) {
	t.Helper()
	for _, group := range groups {
		for _, hook := range group.Hooks {
			if hook.Command == qwenHookCommandPrefix+"session-start" {
				if group.Matcher == nil || *group.Matcher != "startup|resume" {
					t.Fatalf("session-start hook not under startup/resume matcher: %#v", group)
				}
				return
			}
		}
	}
	t.Fatalf("session-start hook not found: %#v", groups)
}

func TestGetAgentHooksMigratesSessionStartMatcher(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qwen"}
	workspace := t.TempDir()
	settingsDir := filepath.Join(workspace, ".qwen")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(settingsDir, "settings.json")
	existing := `{"hooks":{"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"ao hooks qwen session-start","timeout":30000},{"type":"command","command":"custom startup hook","timeout":3}]}]}}`
	if err := os.WriteFile(settingsPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: workspace}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var config qwenHookFile
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	groups := config.Hooks["SessionStart"]
	assertSessionStartMatcher(t, groups)
	if count := countQwenHookCommand(groups, qwenHookCommandPrefix+"session-start"); count != 1 {
		t.Fatalf("session-start command count = %d, want 1 in %#v", count, groups)
	}
	if count := countQwenHookCommand(groups, "custom startup hook"); count != 1 {
		t.Fatalf("custom startup hook count = %d, want 1 in %#v", count, groups)
	}
}

func TestUninstallHooksRemovesQwenHooks(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qwen"}
	workspace := t.TempDir()
	settingsPath := filepath.Join(workspace, ".qwen", "settings.json")

	ctx := context.Background()
	cfg := ports.WorkspaceHookConfig{DataDir: t.TempDir(), SessionID: "sess-1", WorkspacePath: workspace}

	// Pre-seed a user's own Stop hook; it must survive uninstall.
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"custom stop hook","timeout":3}]}]}}`
	if err := os.WriteFile(settingsPath, []byte(existing), 0o644); err != nil {
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

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var config qwenHookFile
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	for _, spec := range qwenManagedHooks {
		if got := countQwenHookCommand(config.Hooks[spec.Event], spec.Command); got != 0 {
			t.Fatalf("%s command %q count = %d after uninstall, want 0", spec.Event, spec.Command, got)
		}
	}
	if countQwenHookCommand(config.Hooks["Stop"], "custom stop hook") != 1 {
		t.Fatalf("user Stop hook not preserved: %#v", config.Hooks["Stop"])
	}
}

func TestGetRestoreCommandReadsAgentSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qwen"}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Permissions:  ports.PermissionModeAuto,
		SystemPrompt: "restore instructions",
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "sess-123"},
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{
		"qwen",
		"--approval-mode", "auto",
		"--append-system-prompt", "restore instructions",
		"--resume", "sess-123",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore cmd\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetRestoreCommandReadsSystemPromptFile(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qwen"}
	dir := t.TempDir()
	file := filepath.Join(dir, "restore-system.md")
	if err := os.WriteFile(file, []byte("restore file instructions"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		SystemPromptFile: file,
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "sess-123"},
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{
		"qwen",
		"--append-system-prompt", "restore file instructions",
		"--resume", "sess-123",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore cmd\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetRestoreCommandDefaultModeOmitsApprovalFlags(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qwen"}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Permissions: ports.PermissionModeDefault,
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "sess-123"},
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{
		"qwen",
		"--resume", "sess-123",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore cmd\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetRestoreCommandFalseWithoutAgentSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qwen"}

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
	plugin := &Plugin{resolvedBinary: "qwen"}

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
		t.Fatalf("Metadata = %#v, want nil for Qwen", info.Metadata)
	}
}

func TestSessionInfoFalseWhenNoHookMetadata(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qwen"}

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

func countQwenHookCommand(entries []hooksjson.MatcherGroup, command string) int {
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

func TestNativeConversationIDRequiresCapturedQwenSessionForTUI(t *testing.T) {
	p := &Plugin{}
	if id, ok, err := p.NativeConversationID(context.Background(), ports.SessionRef{
		ID: "ao-session-1", Metadata: map[string]string{},
	}, domain.SessionModeTUI, ""); err != nil || ok || id != "" {
		t.Fatalf("uncaptured TUI native id = %q ok=%v err=%v", id, ok, err)
	}
	tuiID, ok, err := p.NativeConversationID(context.Background(), ports.SessionRef{
		ID:       "ao-session-1",
		Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "f194dbbc-f28a-4449-b885-09dcec9b5b7f"},
	}, domain.SessionModeTUI, "")
	if err != nil || !ok || tuiID != "f194dbbc-f28a-4449-b885-09dcec9b5b7f" {
		t.Fatalf("captured TUI native id = %q ok=%v err=%v", tuiID, ok, err)
	}
	chatID, ok, err := p.NativeConversationID(context.Background(), ports.SessionRef{},
		domain.SessionModeChat, tuiID)
	if err != nil || !ok || chatID != tuiID {
		t.Fatalf("Chat native id = %q ok=%v err=%v", chatID, ok, err)
	}
}

func TestNativeConversationExistsRequiresPersistedQwenTranscript(t *testing.T) {
	p := &Plugin{}
	id := "f194dbbc-f28a-4449-b885-09dcec9b5b7f"
	qwenHome := t.TempDir()
	env := map[string]string{"QWEN_HOME": qwenHome}

	exists, err := p.NativeConversationExists(context.Background(), ports.SessionRef{}, id, env)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("reserved Qwen session without a transcript reported as persisted")
	}

	projectDir := filepath.Join(qwenHome, "projects", "-test-proj", "chats")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(projectDir, id+".jsonl")
	if err := os.WriteFile(transcript, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	exists, err = p.NativeConversationExists(context.Background(), ports.SessionRef{}, id, env)
	if err != nil || exists {
		t.Fatalf("empty transcript: exists=%v err=%v", exists, err)
	}
	if err := os.WriteFile(transcript, []byte("{\"sessionId\":1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exists, err = p.NativeConversationExists(context.Background(), ports.SessionRef{}, id, env)
	if err != nil || !exists {
		t.Fatalf("persisted transcript: exists=%v err=%v", exists, err)
	}

	exists, err = p.NativeConversationExists(context.Background(), ports.SessionRef{}, "not-a-uuid", env)
	if err != nil || exists {
		t.Fatalf("non-UUID id: exists=%v err=%v", exists, err)
	}
}
