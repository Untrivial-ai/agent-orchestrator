package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hooksjson"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestNativeConversationIDUsesTheSameClaudeUUIDAcrossInterfaces(t *testing.T) {
	p := &Plugin{}
	tuiID, ok, err := p.NativeConversationID(context.Background(), ports.SessionRef{
		ID: "ao-session-1", Metadata: map[string]string{},
	}, domain.SessionModeTUI, "")
	if err != nil || !ok || tuiID != claudeSessionUUID("ao-session-1") {
		t.Fatalf("TUI native id = %q ok=%v err=%v", tuiID, ok, err)
	}
	chatID, ok, err := p.NativeConversationID(context.Background(), ports.SessionRef{
		ID: "ao-session-1", Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "stale"},
	}, domain.SessionModeChat, tuiID)
	if err != nil || !ok || chatID != tuiID {
		t.Fatalf("Chat native id = %q ok=%v err=%v", chatID, ok, err)
	}
}

func TestWindowsNativeClaudeCandidatesForNPMShim(t *testing.T) {
	shim := filepath.Join("prefix", "claude.cmd")
	want := []string{
		filepath.Join("prefix", "claude.exe"),
		filepath.Join("prefix", "node_modules", "@anthropic-ai", "claude-code", "bin", "claude.exe"),
	}
	if got := windowsNativeClaudeCandidatesForShim(shim); !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %#v, want %#v", got, want)
	}
}

func TestResolveNativeWindowsClaudeFindsNPMExecutable(t *testing.T) {
	prefix := t.TempDir()
	shim := filepath.Join(prefix, "claude.cmd")
	if err := os.WriteFile(shim, []byte("@echo off\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(prefix, "node_modules", "@anthropic-ai", "claude-code", "bin", "claude.exe")
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("native claude"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolveNativeWindowsClaude(shim, "windows"); got != want {
		t.Fatalf("resolved binary = %q, want %q", got, want)
	}
	if got := resolveNativeWindowsClaude(shim, "linux"); got != shim {
		t.Fatalf("non-Windows resolution = %q, want unchanged shim %q", got, shim)
	}
}

func TestNativeConversationExistsRequiresPersistedClaudeTranscript(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	p := &Plugin{}
	id := claudeSessionUUID("ao-session-1")

	exists, err := p.NativeConversationExists(context.Background(), ports.SessionRef{}, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("reserved id without a transcript reported as persisted")
	}

	projectDir := filepath.Join(configDir, "projects", "-tmp-worktree")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, id+".jsonl"), []byte("{\"type\":\"user\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exists, err = p.NativeConversationExists(context.Background(), ports.SessionRef{}, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("persisted transcript was not found")
	}

	// Project/session env is the environment Claude itself receives and must win
	// over the daemon's ambient configuration directory.
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	exists, err = p.NativeConversationExists(context.Background(), ports.SessionRef{}, id,
		map[string]string{"CLAUDE_CONFIG_DIR": configDir})
	if err != nil || !exists {
		t.Fatalf("session env transcript lookup: exists=%v err=%v", exists, err)
	}
}

func TestGetLaunchCommandBypassWithPrompt(t *testing.T) {
	p := &Plugin{resolvedBinary: "claude"}

	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions: ports.PermissionModeBypassPermissions,
		Prompt:      "-add a health check",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"claude",
		"--permission-mode", "bypassPermissions",
		"--", "-add a health check",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandMapsPermissionModes(t *testing.T) {
	tests := []struct {
		name        string
		permission  ports.PermissionMode
		want        []string
		notExpected string
	}{
		{"default omits flag (defers to settings.json)", ports.PermissionModeDefault, nil, "--permission-mode"},
		{"accept-edits", ports.PermissionModeAcceptEdits, []string{"--permission-mode", "acceptEdits"}, ""},
		{"auto", ports.PermissionModeAuto, []string{"--permission-mode", "auto"}, ""},
		{"bypass-permissions", ports.PermissionModeBypassPermissions, []string{"--permission-mode", "bypassPermissions"}, ""},
		{"empty omits permission flags", "", nil, "--permission-mode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Plugin{resolvedBinary: "claude"}
			cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
				Permissions: tt.permission,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(tt.want) > 0 && !containsSubsequence(cmd, tt.want) {
				t.Fatalf("command %#v does not contain %#v", cmd, tt.want)
			}
			if tt.notExpected != "" && contains(cmd, tt.notExpected) {
				t.Fatalf("command %#v unexpectedly contains %q", cmd, tt.notExpected)
			}
		})
	}
}

func TestGetLaunchCommandAppendsSystemPromptFromFile(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "system.md")
	if err := os.WriteFile(promptFile, []byte("You are an orchestrator.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &Plugin{resolvedBinary: "claude"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPromptFile: promptFile,
		Prompt:           "do the thing",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"claude",
		"--append-system-prompt-file", promptFile,
		"--", "do the thing",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandSystemPromptFileTakesPrecedenceOverInline(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "system.md")
	if err := os.WriteFile(promptFile, []byte("file instructions\n"), 0600); err != nil {
		t.Fatal(err)
	}

	p := &Plugin{resolvedBinary: "claude"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPrompt:     "inline instructions",
		SystemPromptFile: promptFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubsequence(cmd, []string{"--append-system-prompt-file", promptFile}) {
		t.Fatalf("command %#v does not use the system prompt file", cmd)
	}
	if contains(cmd, "inline instructions") {
		t.Fatalf("command %#v unexpectedly embeds the inline system prompt", cmd)
	}
}

func TestGetLaunchCommandInlineSystemPromptFallback(t *testing.T) {
	p := &Plugin{resolvedBinary: "claude"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPrompt: "inline instructions",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubsequence(cmd, []string{"--append-system-prompt", "inline instructions"}) {
		t.Fatalf("command %#v does not append the inline system prompt fallback", cmd)
	}
}

func TestGetLaunchCommandRejectsMissingSystemPromptFile(t *testing.T) {
	p := &Plugin{resolvedBinary: "claude"}
	_, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPromptFile: filepath.Join(t.TempDir(), "does-not-exist.md"),
	})
	if err == nil {
		t.Fatal("expected error for missing system prompt file")
	}
}

func TestGetLaunchCommandInjectsSessionID(t *testing.T) {
	p := &Plugin{resolvedBinary: "claude"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SessionID: "e0tt49",
		Prompt:    "do the thing",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantUUID := claudeSessionUUID("e0tt49")
	if !containsSubsequence(cmd, []string{"--session-id", wantUUID}) {
		t.Fatalf("command %#v missing --session-id %q", cmd, wantUUID)
	}

	// No SessionID → no --session-id flag.
	cmd, err = p.GetLaunchCommand(context.Background(), ports.LaunchConfig{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if contains(cmd, "--session-id") {
		t.Fatalf("command %#v unexpectedly contains --session-id", cmd)
	}
}

func TestGetLaunchCommandUsesRequestedNativeSessionID(t *testing.T) {
	p := &Plugin{resolvedBinary: "claude"}
	requested := "019f9f7c-53c0-7f10-8d56-a8a979dd7001"
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SessionID:       "stable-ao-session",
		NativeSessionID: requested,
		Prompt:          "continue",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubsequence(cmd, []string{"--session-id", requested}) {
		t.Fatalf("command %#v missing requested native session id", cmd)
	}
	if contains(cmd, claudeSessionUUID("stable-ao-session")) {
		t.Fatalf("command %#v used AO-derived id despite explicit native id", cmd)
	}
}

func TestGetLaunchCommandRejectsInvalidRequestedNativeSessionID(t *testing.T) {
	p := &Plugin{resolvedBinary: "claude"}
	if _, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		NativeSessionID: "not-a-uuid",
	}); err == nil {
		t.Fatal("expected invalid native session id error")
	}
}

func TestNewNativeSessionIDReturnsDistinctClaudeUUIDs(t *testing.T) {
	p := &Plugin{}
	first := p.NewNativeSessionID()
	second := p.NewNativeSessionID()
	if first == second {
		t.Fatalf("fresh native ids collided: %q", first)
	}
	if _, err := uuid.Parse(first); err != nil {
		t.Fatalf("first native id %q is not a UUID: %v", first, err)
	}
	if _, err := uuid.Parse(second); err != nil {
		t.Fatalf("second native id %q is not a UUID: %v", second, err)
	}
}

func TestNativeSessionConfigDirUsesRuntimeOverride(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "claude-profile")
	got, err := (&Plugin{}).NativeSessionConfigDir(context.Background(), map[string]string{
		claudeConfigDirEnv: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("config dir = %q, want %q", got, dir)
	}
}

func TestNativeSessionConfigDirExplicitEmptyIgnoresDaemonOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv(claudeConfigDirEnv, filepath.Join(t.TempDir(), "daemon-claude-profile"))

	got, err := (&Plugin{}).NativeSessionConfigDir(context.Background(), map[string]string{
		claudeConfigDirEnv: "",
		"HOME":             home,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".claude"); got != want {
		t.Fatalf("config dir = %q, want provider default %q", got, want)
	}
}

func TestLocateAndProbeNativeSessionTranscript(t *testing.T) {
	configDir := t.TempDir()
	sessionID := "019f9f7c-53c0-7f10-8d56-a8a979dd7001"
	transcript := filepath.Join(configDir, "projects", "encoded-workspace", sessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ref := ports.NativeSessionRef{NativeSessionID: sessionID, ConfigDir: configDir}
	p := &Plugin{}
	path, ok, err := p.LocateTranscript(context.Background(), ref)
	if err != nil || !ok {
		t.Fatalf("LocateTranscript = (%q, %v, %v), want transcript", path, ok, err)
	}
	if path != transcript {
		t.Fatalf("path = %q, want %q", path, transcript)
	}
	availability, err := p.ProbeNativeSession(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if availability != ports.NativeSessionAvailabilityAvailable {
		t.Fatalf("availability = %q, want available", availability)
	}

	ref.NativeSessionID = "019f9f7c-53c0-7f10-8d56-a8a979dd7002"
	availability, err = p.ProbeNativeSession(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if availability != ports.NativeSessionAvailabilityUnavailable {
		t.Fatalf("missing availability = %q, want unavailable", availability)
	}
}

func TestProbeNativeSessionUnknownWithoutConfigDir(t *testing.T) {
	availability, err := (&Plugin{}).ProbeNativeSession(context.Background(), ports.NativeSessionRef{
		NativeSessionID: "019f9f7c-53c0-7f10-8d56-a8a979dd7001",
	})
	if err != nil {
		t.Fatal(err)
	}
	if availability != ports.NativeSessionAvailabilityUnknown {
		t.Fatalf("availability = %q, want unknown", availability)
	}
}

func TestClaudeSessionUUIDDeterministicAndUnique(t *testing.T) {
	a1 := claudeSessionUUID("alpha")
	a2 := claudeSessionUUID("alpha")
	b := claudeSessionUUID("beta")
	if a1 != a2 {
		t.Fatalf("derivation not deterministic: %q != %q", a1, a2)
	}
	if a1 == b {
		t.Fatalf("distinct ids collided: both %q", a1)
	}
	if _, err := uuid.Parse(a1); err != nil {
		t.Fatalf("derived value is not a valid UUID: %q (%v)", a1, err)
	}
}

func TestGetAgentHooksInstallsClaudeHooks(t *testing.T) {
	p := &Plugin{resolvedBinary: "claude"}
	workspace := t.TempDir()
	settingsDir := filepath.Join(workspace, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(settingsDir, "settings.local.json")
	// Pre-seed a user's own Stop hook + an unrelated setting; both must survive.
	existing := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"my own stop hook","timeout":5}]}]},"permissions":{"defaultMode":"plan"}}`
	if err := os.WriteFile(settingsPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := ports.WorkspaceHookConfig{DataDir: t.TempDir(), SessionID: "sess-1", WorkspacePath: workspace}
	if err := p.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	// A second install must not duplicate AO hook commands.
	if err := p.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Hooks       map[string][]hooksjson.MatcherGroup `json:"hooks"`
		Permissions json.RawMessage                     `json:"permissions"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Hooks == nil {
		t.Fatalf("hooks object missing: %s", data)
	}

	// Every managed command is installed exactly once under its event.
	for _, spec := range claudeManagedHooks {
		if got := countClaudeHookCommand(config.Hooks[spec.Event], spec.Command); got != 1 {
			t.Fatalf("%s command %q count = %d, want 1", spec.Event, spec.Command, got)
		}
	}
	// Existing user hook preserved.
	if countClaudeHookCommand(config.Hooks["Stop"], "my own stop hook") != 1 {
		t.Fatalf("existing Stop hook not preserved: %#v", config.Hooks["Stop"])
	}
	// Unrelated settings preserved.
	if len(config.Permissions) == 0 {
		t.Fatalf("unrelated settings clobbered: %s", data)
	}
	// SessionStart must fire on resume (and clear/compact/fork) as well as a
	// fresh startup: a --resume relaunch otherwise never confirms the native
	// session id under the new RuntimeLaunchID until the user types again (#4122).
	if m := matcherForCommand(config.Hooks["SessionStart"], "ao hooks claude-code session-start"); m == nil || *m != "startup|resume|clear|compact|fork" {
		t.Fatalf("SessionStart matcher = %v, want startup|resume|clear|compact|fork", m)
	}
	if m := matcherForCommand(config.Hooks["UserPromptSubmit"], "ao hooks claude-code user-prompt-submit"); m != nil {
		t.Fatalf("UserPromptSubmit matcher = %v, want none", m)
	}
	// Notification and SessionEnd install with no matcher (they fire for all
	// sub-types; the handler filters on the payload).
	if m := matcherForCommand(config.Hooks["Notification"], "ao hooks claude-code notification"); m != nil {
		t.Fatalf("Notification matcher = %v, want none", m)
	}
	if m := matcherForCommand(config.Hooks["SessionEnd"], "ao hooks claude-code session-end"); m != nil {
		t.Fatalf("SessionEnd matcher = %v, want none", m)
	}
}

func TestGetAgentHooksMigratesSessionStartMatcher(t *testing.T) {
	p := &Plugin{resolvedBinary: "claude"}
	workspace := t.TempDir()
	settingsPath := filepath.Join(workspace, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// Workspaces installed before #4122 registered SessionStart only under
	// "startup". Reconcile must move the AO command onto the broader matcher
	// without duplicating it or dropping a user's own startup hook.
	existing := `{"hooks":{"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"ao hooks claude-code session-start","timeout":30},{"type":"command","command":"custom startup hook","timeout":5}]}]}}`
	if err := os.WriteFile(settingsPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := p.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: workspace}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Hooks map[string][]hooksjson.MatcherGroup `json:"hooks"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	groups := config.Hooks["SessionStart"]
	if m := matcherForCommand(groups, "ao hooks claude-code session-start"); m == nil || *m != "startup|resume|clear|compact|fork" {
		t.Fatalf("SessionStart matcher = %v, want startup|resume|clear|compact|fork", m)
	}
	if got := countClaudeHookCommand(groups, "ao hooks claude-code session-start"); got != 1 {
		t.Fatalf("session-start command count = %d, want 1 in %#v", got, groups)
	}
	if got := countClaudeHookCommand(groups, "custom startup hook"); got != 1 {
		t.Fatalf("custom startup hook count = %d, want 1 in %#v", got, groups)
	}
}

func TestUninstallHooksRemovesClaudeHooks(t *testing.T) {
	p := &Plugin{resolvedBinary: "claude"}
	workspace := t.TempDir()
	settingsPath := filepath.Join(workspace, ".claude", "settings.local.json")

	ctx := context.Background()
	cfg := ports.WorkspaceHookConfig{DataDir: t.TempDir(), SessionID: "sess-1", WorkspacePath: workspace}

	// Pre-seed a user's own Stop hook + an unrelated setting; both must survive.
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"my own stop hook","timeout":5}]}]},"permissions":{"defaultMode":"plan"}}`
	if err := os.WriteFile(settingsPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := p.GetAgentHooks(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if installed, err := p.AreHooksInstalled(ctx, workspace); err != nil || !installed {
		t.Fatalf("AreHooksInstalled after install = (%v, %v), want (true, nil)", installed, err)
	}

	if err := p.UninstallHooks(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	if installed, err := p.AreHooksInstalled(ctx, workspace); err != nil || installed {
		t.Fatalf("AreHooksInstalled after uninstall = (%v, %v), want (false, nil)", installed, err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Hooks       map[string][]hooksjson.MatcherGroup `json:"hooks"`
		Permissions json.RawMessage                     `json:"permissions"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	// No managed command survives; the SessionStart/UserPromptSubmit events,
	// which held only AO hooks, are removed entirely.
	for _, spec := range claudeManagedHooks {
		if got := countClaudeHookCommand(config.Hooks[spec.Event], spec.Command); got != 0 {
			t.Fatalf("%s command %q count = %d after uninstall, want 0", spec.Event, spec.Command, got)
		}
	}
	// The user's own Stop hook and unrelated settings are preserved.
	if countClaudeHookCommand(config.Hooks["Stop"], "my own stop hook") != 1 {
		t.Fatalf("user Stop hook not preserved: %#v", config.Hooks["Stop"])
	}
	if len(config.Permissions) == 0 {
		t.Fatalf("unrelated settings clobbered: %s", data)
	}

	// Uninstall is idempotent: a second call is a clean no-op.
	if err := p.UninstallHooks(ctx, workspace); err != nil {
		t.Fatalf("second uninstall: %v", err)
	}
}

func TestUninstallHooksNoSettingsFile(t *testing.T) {
	p := &Plugin{resolvedBinary: "claude"}
	workspace := t.TempDir()
	if err := p.UninstallHooks(context.Background(), workspace); err != nil {
		t.Fatalf("uninstall with no settings file: %v", err)
	}
	if installed, err := p.AreHooksInstalled(context.Background(), workspace); err != nil || installed {
		t.Fatalf("AreHooksInstalled = (%v, %v), want (false, nil)", installed, err)
	}
}

func TestSessionInfoReadsHookMetadata(t *testing.T) {
	info, ok, err := (&Plugin{resolvedBinary: "claude"}).SessionInfo(context.Background(), ports.SessionRef{
		WorkspacePath: "/some/path",
		Metadata: map[string]string{
			ports.MetadataKeyAgentSessionID: "claude-native-1",
			ports.MetadataKeyTitle:          "Fix login redirect",
			ports.MetadataKeySummary:        "Updated the auth callback and tests.",
			"ignored":                       "not returned",
		},
	})
	if err != nil || !ok {
		t.Fatalf("SessionInfo = (ok=%v, err=%v), want ok", ok, err)
	}
	if info.AgentSessionID != "claude-native-1" {
		t.Fatalf("AgentSessionID = %q", info.AgentSessionID)
	}
	if info.Title != "Fix login redirect" {
		t.Fatalf("Title = %q", info.Title)
	}
	if info.Summary != "Updated the auth callback and tests." {
		t.Fatalf("Summary = %q", info.Summary)
	}
	if info.Metadata != nil {
		t.Fatalf("Metadata = %#v, want nil for Claude", info.Metadata)
	}
}

func TestSessionInfoFalseWhenNoHookMetadata(t *testing.T) {
	info, ok, err := (&Plugin{resolvedBinary: "claude"}).SessionInfo(context.Background(), ports.SessionRef{
		WorkspacePath: "/some/path",
		Metadata:      map[string]string{},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if ok {
		t.Fatalf("ok = true, want false")
	}
	if !reflect.DeepEqual(info, ports.SessionInfo{}) {
		t.Fatalf("info = %#v, want zero", info)
	}
}

// countClaudeHookCommand counts how many hook entries under one event register
// the given command — used to prove no duplicate AO hooks.
func countClaudeHookCommand(groups []hooksjson.MatcherGroup, command string) int {
	count := 0
	for _, group := range groups {
		for _, hook := range group.Hooks {
			if hook.Command == command {
				count++
			}
		}
	}
	return count
}

// matcherForCommand returns the matcher on the group that registers the given
// command (nil if the group has no matcher).
func matcherForCommand(groups []hooksjson.MatcherGroup, command string) *string {
	for _, group := range groups {
		for _, hook := range group.Hooks {
			if hook.Command == command {
				return group.Matcher
			}
		}
	}
	return nil
}

func TestGetRestoreCommandReadsAgentSessionID(t *testing.T) {
	cmd, ok, err := (&Plugin{resolvedBinary: "claude"}).GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Permissions: ports.PermissionModeBypassPermissions,
		Prompt:      "continue from AO",
		Session: ports.SessionRef{
			ID:       "sess-r",
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "claude-native-1"},
		},
	})
	if err != nil || !ok {
		t.Fatalf("restore = (ok=%v, err=%v), want ok", ok, err)
	}
	// The hook-captured native id wins over the derived fallback.
	want := []string{"claude", "--permission-mode", "bypassPermissions", "--resume", "claude-native-1", "--", "continue from AO"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore cmd\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetRestoreCommandReappendsSystemPrompt(t *testing.T) {
	// --resume rebuilds the system prompt from flags, so standing instructions
	// (e.g. the orchestrator role) must be re-appended on restore.
	cmd, ok, err := (&Plugin{resolvedBinary: "claude"}).GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Permissions:  ports.PermissionModeBypassPermissions,
		SystemPrompt: "You are an orchestrator.",
		Session: ports.SessionRef{
			ID:       "sess-r",
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "claude-native-1"},
		},
	})
	if err != nil || !ok {
		t.Fatalf("restore = (ok=%v, err=%v), want ok", ok, err)
	}
	want := []string{"claude", "--permission-mode", "bypassPermissions", "--append-system-prompt", "You are an orchestrator.", "--resume", "claude-native-1"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore cmd\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetRestoreCommandReappendsSystemPromptFromFile(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "system.md")
	if err := os.WriteFile(promptFile, []byte("file instructions\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cmd, ok, err := (&Plugin{resolvedBinary: "claude"}).GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Permissions:      ports.PermissionModeBypassPermissions,
		Prompt:           "continue from AO",
		SystemPrompt:     "inline fallback",
		SystemPromptFile: promptFile,
		Session: ports.SessionRef{
			ID:       "sess-r",
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "claude-native-1"},
		},
	})
	if err != nil || !ok {
		t.Fatalf("restore = (ok=%v, err=%v), want ok", ok, err)
	}
	want := []string{
		"claude", "--permission-mode", "bypassPermissions",
		"--append-system-prompt-file", promptFile,
		"--resume", "claude-native-1",
		"--", "continue from AO",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore cmd\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetRestoreCommandRejectsNonRegularSystemPromptFile(t *testing.T) {
	_, ok, err := (&Plugin{resolvedBinary: "claude"}).GetRestoreCommand(context.Background(), ports.RestoreConfig{
		SystemPromptFile: t.TempDir(),
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "claude-native-1"},
		},
	})
	if err == nil || ok {
		t.Fatalf("restore = (ok=%v, err=%v), want non-regular-file error", ok, err)
	}
}

func TestGetRestoreCommandFallsBackToDerivedUUID(t *testing.T) {
	// No agentSessionId captured (pre-hook session) → derive deterministically
	// from the AO session id, the explicit fallback.
	cmd, ok, err := (&Plugin{resolvedBinary: "claude"}).GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Permissions: ports.PermissionModeBypassPermissions,
		Session:     ports.SessionRef{ID: "sess-r"},
	})
	if err != nil || !ok {
		t.Fatalf("restore = (ok=%v, err=%v), want ok", ok, err)
	}
	want := []string{"claude", "--permission-mode", "bypassPermissions", "--resume", claudeSessionUUID("sess-r")}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore cmd\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetRestoreCommandFalseWithoutSessionID(t *testing.T) {
	cases := []struct {
		name string
		ref  ports.SessionRef
	}{
		{"empty ref", ports.SessionRef{}},
		{"blank agent session, no id", ports.SessionRef{Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "   "}}},
		{"workspace path only", ports.SessionRef{WorkspacePath: "/some/path"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, ok, err := (&Plugin{resolvedBinary: "claude"}).GetRestoreCommand(context.Background(),
				ports.RestoreConfig{Permissions: ports.PermissionModeBypassPermissions, Session: tc.ref})
			if err != nil || ok || cmd != nil {
				t.Fatalf("restore = (%#v, %v, %v), want (nil,false,nil)", cmd, ok, err)
			}
		})
	}
}

func TestGetLaunchCommandAppliesAgentConfig(t *testing.T) {
	p := &Plugin{resolvedBinary: "claude"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Config: ports.AgentConfig{
			Model:       "claude-opus-4-5",
			Permissions: ports.PermissionModeAcceptEdits,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubsequence(cmd, []string{"--model", "claude-opus-4-5"}) {
		t.Fatalf("command %#v missing --model flag", cmd)
	}
	if !containsSubsequence(cmd, []string{"--permission-mode", "acceptEdits"}) {
		t.Fatalf("command %#v missing config-driven permission mode", cmd)
	}
}

func TestGetLaunchCommandExplicitPermissionsOverrideConfig(t *testing.T) {
	p := &Plugin{resolvedBinary: "claude"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions: ports.PermissionModeBypassPermissions,
		Config:      ports.AgentConfig{Permissions: ports.PermissionModeAcceptEdits},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubsequence(cmd, []string{"--permission-mode", "bypassPermissions"}) {
		t.Fatalf("explicit Permissions should win; got %#v", cmd)
	}
}

func TestGetLaunchCommandRejectsInvalidConfig(t *testing.T) {
	p := &Plugin{resolvedBinary: "claude"}
	if _, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Config: ports.AgentConfig{Permissions: "yolo"},
	}); err == nil {
		t.Fatal("expected error for invalid permission mode")
	}
}

func TestManifestID(t *testing.T) {
	if got := New().Manifest().ID; got != "claude-code" {
		t.Fatalf("manifest id = %q, want claude-code", got)
	}
}

func TestClaudeConfigAuthStatusAuthorizedWithOAuthSubscription(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	content := `{
		"hasAvailableSubscription": true,
		"oauthAccount": {
			"accountUuid": "account-1",
			"subscriptionCreatedAt": "2026-01-01T00:00:00Z"
		}
	}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := claudeConfigAuthStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestClaudeConfigAuthStatusAuthorizedWithOAuthAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	content := `{"oauthAccount":{"accountUuid":"account-1"}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := claudeConfigAuthStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestClaudeConfigAuthStatusAuthorizedWithUserID(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	if err := os.WriteFile(path, []byte(`{"userID":"user-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := claudeConfigAuthStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestClaudeConfigAuthStatusUnknownWithoutOAuthIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	content := `{"oauthAccount":{}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := claudeConfigAuthStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func TestClaudeAuthStatusFromOutputAuthorizedWithCleanJSON(t *testing.T) {
	status, ok := claudeAuthStatusFromOutput([]byte(`{"loggedIn":true,"authMethod":"oauth_token"}`))
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestClaudeAuthStatusFromOutputAuthorizedWithPrefixedWarning(t *testing.T) {
	output := []byte("warning: ignored config line\n{\"loggedIn\":true,\"authMethod\":\"oauth_token\"}\n")
	status, ok := claudeAuthStatusFromOutput(output)
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestClaudeAuthStatusFromOutputUnauthorized(t *testing.T) {
	status, ok := claudeAuthStatusFromOutput([]byte(`{"loggedIn":false}`))
	if !ok || status != ports.AgentAuthStatusUnauthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusUnauthorized)
	}
}

func TestClaudeAuthStatusFromOutputUnknownForUnrecognizedFailure(t *testing.T) {
	status, ok := claudeAuthStatusFromOutput([]byte("unsupported subcommand on this version"))
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func TestEnsureWorkspaceTrustedCreatesEntry(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, ".claude.json")
	// Seed an existing config with another project + a top-level key, to
	// prove we preserve unrelated state.
	seed := `{"userID":"abc","projects":{"/existing/proj":{"hasTrustDialogAccepted":true,"lastCost":1.5}}}`
	if err := os.WriteFile(cfgPath, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	work := "/Users/me/.ao/worktrees/01ABC"
	if err := ensureWorkspaceTrusted(cfgPath, work); err != nil {
		t.Fatalf("ensureWorkspaceTrusted: %v", err)
	}

	root := readJSON(t, cfgPath)
	projects := root["projects"].(map[string]any)

	// New entry trusted.
	newEntry := projects[work].(map[string]any)
	if newEntry["hasTrustDialogAccepted"] != true {
		t.Fatalf("new entry not trusted: %#v", newEntry)
	}
	// Existing project preserved (including its other fields).
	existing := projects["/existing/proj"].(map[string]any)
	if existing["hasTrustDialogAccepted"] != true || existing["lastCost"].(float64) != 1.5 {
		t.Fatalf("existing project clobbered: %#v", existing)
	}
	// Top-level key preserved.
	if root["userID"] != "abc" {
		t.Fatalf("top-level key clobbered: %#v", root["userID"])
	}
}

func TestEnsureWorkspaceTrustedNormalizesWindowsProjectKey(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, ".claude.json")
	if err := os.WriteFile(cfgPath, []byte(`{"projects":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	const workspacePath = `D:\dev\agent-orchestrator\.ao\worktrees\demo\worker-1`
	const wantKey = `D:/dev/agent-orchestrator/.ao/worktrees/demo/worker-1`
	if err := ensureWorkspaceTrustedForOS(cfgPath, workspacePath, "windows"); err != nil {
		t.Fatalf("ensureWorkspaceTrustedForOS: %v", err)
	}

	root := readJSON(t, cfgPath)
	projects := root["projects"].(map[string]any)
	entry, ok := projects[wantKey].(map[string]any)
	if !ok || entry["hasTrustDialogAccepted"] != true {
		t.Fatalf("forward-slash trust entry = %#v, want accepted entry", projects[wantKey])
	}
	if _, exists := projects[workspacePath]; exists {
		t.Fatalf("unexpected backslash-keyed trust entry: %#v", projects[workspacePath])
	}
}

func TestEnsureWorkspaceTrustedLeavesNonWindowsProjectKeyUnchanged(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, ".claude.json")
	if err := os.WriteFile(cfgPath, []byte(`{"projects":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	const workspacePath = `/worktrees/project\with-backslash`
	if err := ensureWorkspaceTrustedForOS(cfgPath, workspacePath, "linux"); err != nil {
		t.Fatalf("ensureWorkspaceTrustedForOS: %v", err)
	}

	root := readJSON(t, cfgPath)
	projects := root["projects"].(map[string]any)
	entry, ok := projects[workspacePath].(map[string]any)
	if !ok || entry["hasTrustDialogAccepted"] != true {
		t.Fatalf("unchanged trust entry = %#v, want accepted entry", projects[workspacePath])
	}
}

func TestEnsureWorkspaceTrustedIsIdempotentAndNoWriteWhenAlreadyTrusted(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, ".claude.json")
	work := "/w"
	if err := os.WriteFile(cfgPath, []byte(`{"projects":{"/w":{"hasTrustDialogAccepted":true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	info1, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := ensureWorkspaceTrusted(cfgPath, work); err != nil {
		t.Fatalf("ensureWorkspaceTrusted: %v", err)
	}

	// Already trusted → no rewrite → mtime unchanged.
	info2, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Fatal("expected no rewrite when already trusted")
	}
}

func TestEnsureWorkspaceTrustedCreatesMissingConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, ".claude.json") // does not exist yet
	work := "/fresh/worktree"

	if err := ensureWorkspaceTrusted(cfgPath, work); err != nil {
		t.Fatalf("ensureWorkspaceTrusted: %v", err)
	}

	root := readJSON(t, cfgPath)
	projects := root["projects"].(map[string]any)
	entry := projects[work].(map[string]any)
	if entry["hasTrustDialogAccepted"] != true {
		t.Fatalf("entry not trusted in freshly-created config: %#v", entry)
	}
}

func TestEnsureWorkspaceTrustedRefusesZeroByteConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, ".claude.json")
	// Simulate catching some other writer mid-truncate: the file exists
	// but reads back as zero bytes.
	if err := os.WriteFile(cfgPath, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}

	err := ensureWorkspaceTrusted(cfgPath, "/some/worktree")
	if err == nil {
		t.Fatal("expected error for zero-byte config, got nil")
	}

	data, readErr := os.ReadFile(cfgPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(data) != 0 {
		t.Fatalf("zero-byte config must not be overwritten; got %d bytes: %s", len(data), data)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return m
}

func TestGetLaunchCommandEmitsToolAllowlist(t *testing.T) {
	p := &Plugin{resolvedBinary: "claude"}

	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		AllowedTools:    []string{"Read", "Grep", "Bash(git diff:*)"},
		DisallowedTools: []string{"Edit", "Write", "Bash(git push:*)"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Each list is one comma-joined value so a rule with spaces stays intact.
	if !containsSubsequence(cmd, []string{"--allowedTools", "Read,Grep,Bash(git diff:*)"}) {
		t.Fatalf("missing joined --allowedTools value; got %#v", cmd)
	}
	if !containsSubsequence(cmd, []string{"--disallowedTools", "Edit,Write,Bash(git push:*)"}) {
		t.Fatalf("missing joined --disallowedTools value; got %#v", cmd)
	}
}

func TestGetLaunchCommandOmitsToolFlagsWhenUnset(t *testing.T) {
	p := &Plugin{resolvedBinary: "claude"}

	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	if contains(cmd, "--allowedTools") || contains(cmd, "--disallowedTools") {
		t.Fatalf("unrestricted launch should emit no tool flags; got %#v", cmd)
	}
}

func TestComposerIsEmptyUsesClaudePromptMarker(t *testing.T) {
	plugin := &Plugin{}
	if !plugin.ComposerIsEmpty("\x1b[39m❯\u00a0") {
		t.Fatal("blank Claude composer was not recognized")
	}
	rule := "\x1b[38;5;244m" + strings.Repeat("─", 48) + "\x1b[39m"
	footer := "\x1b[38;5;220mUpdate available!\x1b[39m\n\x1b[38;5;211m⏵⏵ bypass permissions on\x1b[39m"
	if !plugin.ComposerIsEmpty(rule + "\n\x1b[39m❯\u00a0\x1b[7m \x1b[0m\n" + rule + "\n" + footer) {
		t.Fatal("blank bordered Claude composer above status footer was not recognized")
	}
}

func TestInspectTerminalSurfaceSeparatesClaudeWorkFromComposer(t *testing.T) {
	plugin := &Plugin{}
	rule := "\x1b[38;5;244m" + strings.Repeat("─", 48) + "\x1b[39m"
	tests := []struct {
		name       string
		output     string
		wantWork   ports.TerminalSurfaceWorkState
		wantEditor ports.TerminalComposerState
	}{
		{
			name:       "idle empty composer",
			output:     rule + "\n\x1b[39m❯\u00a0\x1b[7m \x1b[0m\n" + rule + "\n⏵⏵ bypass permissions on",
			wantWork:   ports.TerminalSurfaceWorkIdle,
			wantEditor: ports.TerminalComposerEmpty,
		},
		{
			name:       "idle draft",
			output:     rule + "\n❯ keep this draft\n" + rule + "\n⏵⏵ bypass permissions on",
			wantWork:   ports.TerminalSurfaceWorkIdle,
			wantEditor: ports.TerminalComposerDraft,
		},
		{
			name:       "active wording inside draft is not current chrome",
			output:     rule + "\n❯ quote ✶ Generating… (esc to interrupt · 2s)\n" + rule + "\n⏵⏵ bypass permissions on",
			wantWork:   ports.TerminalSurfaceWorkIdle,
			wantEditor: ports.TerminalComposerDraft,
		},
		{
			name:       "active with empty composer",
			output:     "✶ Generating… (esc to interrupt · 2s)\n" + rule + "\n❯\n" + rule,
			wantWork:   ports.TerminalSurfaceWorkActive,
			wantEditor: ports.TerminalComposerEmpty,
		},
		{
			name: "active with interrupt hint in footer",
			output: "✻ Computing… (24s · ↓ 114 tokens)\n" + rule + "\n❯\n" + rule +
				"\n⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt · ← for agents",
			wantWork:   ports.TerminalSurfaceWorkActive,
			wantEditor: ports.TerminalComposerEmpty,
		},
		{
			name: "spinner transcript above idle composer without active footer",
			output: "✻ Computing… (24s · ↓ 114 tokens)\n" + rule + "\n❯\n" + rule +
				"\n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents",
			wantWork:   ports.TerminalSurfaceWorkIdle,
			wantEditor: ports.TerminalComposerEmpty,
		},
		{
			name:       "permission dialog",
			output:     "Do you want to proceed?\n❯ 1. Yes\n  2. No\nPress enter to confirm",
			wantWork:   ports.TerminalSurfaceWorkBlocked,
			wantEditor: ports.TerminalComposerUnknown,
		},
		{
			name: "permission dialog with provider-specific question and modern footer",
			output: "Claude wants to run Bash\n" +
				"❯ 1. Yes\n" +
				"  2. No\n" +
				"Esc to cancel · Tab to amend",
			wantWork:   ports.TerminalSurfaceWorkBlocked,
			wantEditor: ports.TerminalComposerUnknown,
		},
		{
			name: "permission dialog with a non-first selected option",
			output: "Claude wants to run Bash\n" +
				"  1. Yes\n" +
				"❯ 2. No\n" +
				"Esc to cancel · Tab to amend",
			wantWork:   ports.TerminalSurfaceWorkBlocked,
			wantEditor: ports.TerminalComposerUnknown,
		},
		{
			name: "permissions menu without a selected prompt marker",
			output: "❯ /permissions\n" + rule + "\n" +
				"  Permissions  Recently denied   Allow   Ask   Deny   Workspace\n\n" +
				"  Claude Code won't ask before using allowed tools.\n" +
				"  ╭───────────────────────────────────────────────╮\n" +
				"  │ ⌕ Search…                                     │\n" +
				"  ╰───────────────────────────────────────────────╯\n\n" +
				"    1. Add a new rule…\n\n" +
				"  ←/→ to switch · ↓ to select · Esc to cancel",
			wantWork:   ports.TerminalSurfaceWorkBlocked,
			wantEditor: ports.TerminalComposerUnknown,
		},
		{
			name: "completed permissions menu above current composer is not blocked",
			output: "  ╭─── Search… ───╮\n" +
				"  │ ⌕ Search… │\n" +
				"  ╰────────────╯\n" +
				"    1. Add a new rule…\n" +
				"  ←/→ to switch · ↓ to select · Esc to cancel\n" +
				rule + "\n❯\u00a0\x1b[7m \x1b[0m\n" + rule + "\n⏵⏵ auto mode on",
			wantWork:   ports.TerminalSurfaceWorkIdle,
			wantEditor: ports.TerminalComposerEmpty,
		},
		{
			name: "completed permission menu above the current composer is not blocked",
			output: "Claude wanted to run Bash\n" +
				"❯ 1. Yes\n" +
				"  2. No\n" +
				"Esc to cancel · Tab to amend\n" + rule + "\n❯\u00a0\x1b[7m \x1b[0m\n" + rule,
			wantWork:   ports.TerminalSurfaceWorkIdle,
			wantEditor: ports.TerminalComposerEmpty,
		},
		{
			name: "permission wording in completed response is not current chrome",
			output: "The command asked: Do you want to proceed?\n" +
				"It then said: Press enter to confirm.\n" + rule + "\n❯\u00a0\x1b[7m \x1b[0m\n" + rule + "\n⏵⏵ bypass permissions on",
			wantWork:   ports.TerminalSurfaceWorkIdle,
			wantEditor: ports.TerminalComposerEmpty,
		},
		{
			name: "interrupt wording in completed response is not active chrome",
			output: "The status previously read esc to interrupt while the command ran.\n" +
				rule + "\n❯\u00a0\x1b[7m \x1b[0m\n" + rule + "\n⏵⏵ bypass permissions on",
			wantWork:   ports.TerminalSurfaceWorkIdle,
			wantEditor: ports.TerminalComposerEmpty,
		},
		{
			name: "old active row separated from composer is not current chrome",
			output: "✶ Generating… (esc to interrupt · 2s)\nThe work finished.\n" +
				rule + "\n❯\u00a0\x1b[7m \x1b[0m\n" + rule + "\n⏵⏵ bypass permissions on",
			wantWork:   ports.TerminalSurfaceWorkIdle,
			wantEditor: ports.TerminalComposerEmpty,
		},
		{
			name: "transcript marker is outside current surface",
			output: "Earlier output mentioned esc to interrupt\n1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n" +
				"❯\u00a0",
			wantWork:   ports.TerminalSurfaceWorkIdle,
			wantEditor: ports.TerminalComposerEmpty,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := plugin.InspectTerminalSurface(tt.output)
			if got.Work != tt.wantWork || got.Composer != tt.wantEditor {
				t.Fatalf("InspectTerminalSurface() = %+v, want work=%v composer=%v", got, tt.wantWork, tt.wantEditor)
			}
		})
	}
}

func contains(values []string, needle string) bool {
	for _, v := range values {
		if v == needle {
			return true
		}
	}
	return false
}

func containsSubsequence(values, needle []string) bool {
	if len(needle) == 0 {
		return true
	}
	for start := 0; start+len(needle) <= len(values); start++ {
		ok := true
		for i, w := range needle {
			if values[start+i] != w {
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
