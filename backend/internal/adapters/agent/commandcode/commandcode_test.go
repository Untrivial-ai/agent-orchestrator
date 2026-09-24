package commandcode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestManifest(t *testing.T) {
	m := (&Plugin{}).Manifest()
	if m.ID != "command-code" {
		t.Fatalf("ID = %q, want command-code", m.ID)
	}
	if m.Name != "Command Code" {
		t.Fatalf("Name = %q, want Command Code", m.Name)
	}
	hasAgent := false
	for _, c := range m.Capabilities {
		if c == adapters.CapabilityAgent {
			hasAgent = true
		}
	}
	if !hasAgent {
		t.Fatal("missing CapabilityAgent")
	}
}

func TestGetConfigSpecReportsModel(t *testing.T) {
	spec, err := (&Plugin{}).GetConfigSpec(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(spec.Fields) != 1 || spec.Fields[0].Key != "model" {
		t.Fatalf("unexpected fields: %#v", spec.Fields)
	}
}

func TestGetPromptDeliveryStrategy(t *testing.T) {
	s, err := (&Plugin{}).GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if s != ports.PromptDeliveryAfterStart {
		t.Fatalf("strategy = %q, want after_start", s)
	}
}

func TestPromptReadinessHints(t *testing.T) {
	hints, err := (&Plugin{}).PromptReadinessHints(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if hints.InitialDelay != 750*time.Millisecond || hints.PollInterval != 200*time.Millisecond || hints.Timeout != 8*time.Second || hints.Lines != 80 {
		t.Fatalf("hints = %#v", hints)
	}
	if !reflect.DeepEqual(hints.Patterns, []string{"Command Code"}) {
		t.Fatalf("patterns = %#v, want Command Code", hints.Patterns)
	}
}

func TestGetLaunchCommandDefaultPerms(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cmd"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{Prompt: "do the thing"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"cmd", "--skip-onboarding", "--no-auto-update", "--trust"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
	assertPromptNotInArgv(t, cmd)
}

func TestGetLaunchCommandAcceptEdits(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cmd"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{Permissions: ports.PermissionModeAcceptEdits})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cmd", "--skip-onboarding", "--no-auto-update", "--trust", "--permission-mode", "accept-edits"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandAutoUsesAcceptEdits(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cmd"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{Permissions: ports.PermissionModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	if !containsPair(cmd, "--permission-mode", "accept-edits") {
		t.Fatalf("cmd = %#v, want --permission-mode accept-edits", cmd)
	}
}

func TestGetLaunchCommandBypassUsesYolo(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cmd"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{Permissions: ports.PermissionModeBypassPermissions})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSequence(cmd, "--yolo") {
		t.Fatalf("cmd = %#v, want --yolo", cmd)
	}
	if containsSequence(cmd, "--permission-mode") {
		t.Fatalf("cmd = %#v, --yolo and --permission-mode are mutually exclusive", cmd)
	}
}

func TestGetLaunchCommandForwardsModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cmd"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{Config: ports.AgentConfig{Model: "  anthropic/claude-sonnet-4-6  "}})
	if err != nil {
		t.Fatal(err)
	}
	if !containsPair(cmd, "--model", "anthropic/claude-sonnet-4-6") {
		t.Fatalf("cmd = %#v, want --model with trimmed override", cmd)
	}
}

func TestGetRestoreCommandWithCapturedSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cmd"}
	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{Metadata: map[string]string{ports.MetadataKeyAgentSessionID: " 01hxsession "}},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true when a native session id is captured")
	}
	want := []string{"cmd", "--skip-onboarding", "--no-auto-update", "--trust", "--resume", "01hxsession"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandWithoutSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cmd"}
	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ok || cmd != nil {
		t.Fatalf("cmd, ok = (%#v, %v), want (nil, false)", cmd, ok)
	}
}

func TestSessionInfoReadsMetadata(t *testing.T) {
	info, ok, err := (&Plugin{}).SessionInfo(context.Background(), ports.SessionRef{
		Metadata: map[string]string{
			ports.MetadataKeyAgentSessionID: "01hx",
			ports.MetadataKeyTitle:          "fix the thing",
		},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !ok || info.AgentSessionID != "01hx" || info.Title != "fix the thing" {
		t.Fatalf("info, ok = (%#v, %v)", info, ok)
	}
}

func TestSessionInfoFalseWithoutMetadata(t *testing.T) {
	_, ok, err := (&Plugin{}).SessionInfo(context.Background(), ports.SessionRef{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ok {
		t.Fatal("ok = true, want false with no metadata")
	}
}

func TestGetAgentHooksInstallsCommandCodeLifecycleHooks(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cmd"}
	workspace := t.TempDir()
	settingsPath := filepath.Join(workspace, ".commandcode", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o750); err != nil {
		t.Fatal(err)
	}
	seed := `{"theme":"dark","hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo mine"}]}]}}`
	if err := os.WriteFile(settingsPath, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: workspace}); err != nil {
			t.Fatalf("GetAgentHooks #%d: %v", i+1, err)
		}
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Theme string `json:"theme"`
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		"ao hooks command-code session-start",
		"ao hooks command-code pre-tool-use",
		"ao hooks command-code post-tool-use",
		"ao hooks command-code stop",
	} {
		count := 0
		for _, groups := range settings.Hooks {
			for _, group := range groups {
				for _, hook := range group.Hooks {
					if hook.Command == command {
						count++
					}
				}
			}
		}
		if count != 1 {
			t.Fatalf("managed hook %q count = %d, want 1", command, count)
		}
	}
	if settings.Theme != "dark" {
		t.Fatalf("theme = %q, want dark", settings.Theme)
	}
	userHookFound := false
	for _, group := range settings.Hooks["Stop"] {
		for _, hook := range group.Hooks {
			userHookFound = userHookFound || hook.Command == "echo mine"
		}
	}
	if !userHookFound {
		t.Fatal("user Stop hook was not preserved")
	}

	gitignore, err := os.ReadFile(filepath.Join(workspace, ".commandcode", ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gitignore), "settings.json") {
		t.Fatalf(".gitignore does not cover managed settings:\n%s", gitignore)
	}
}

func TestUninstallHooksRemovesOnlyAOHooks(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cmd"}
	workspace := t.TempDir()
	settingsPath := filepath.Join(workspace, ".commandcode", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o750); err != nil {
		t.Fatal(err)
	}
	seed := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo mine"}]}]}}`
	if err := os.WriteFile(settingsPath, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: workspace}); err != nil {
		t.Fatal(err)
	}
	uninstaller, ok := any(plugin).(interface {
		UninstallHooks(context.Context, string) error
	})
	if !ok {
		t.Fatal("Plugin does not implement UninstallHooks")
	}
	if err := uninstaller.UninstallHooks(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if strings.Contains(body, "ao hooks command-code ") {
		t.Fatalf("AO hooks remain after uninstall:\n%s", body)
	}
	if !strings.Contains(body, "echo mine") {
		t.Fatalf("user hook was removed:\n%s", body)
	}
}

func TestResolveBinaryFindsCommandCodeOnPath(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "command-code")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	t.Setenv("PATH", dir)

	path, err := (&Plugin{}).ResolveBinary(context.Background())
	if err != nil {
		t.Fatalf("ResolveBinary: %v", err)
	}
	if path != binPath {
		t.Fatalf("path = %q, want %q", path, binPath)
	}
}

func TestResolveBinaryMissingReturnsErrAgentBinaryNotFound(t *testing.T) {
	for _, candidate := range commandCodeBinarySpec.UnixPaths {
		if _, err := os.Stat(candidate); err == nil {
			t.Skipf("machine has %s installed; cannot assert not-found", candidate)
		}
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	_, err := (&Plugin{}).ResolveBinary(context.Background())
	if !errors.Is(err, ports.ErrAgentBinaryNotFound) {
		t.Fatalf("err = %v, want ErrAgentBinaryNotFound", err)
	}
}

func assertPromptNotInArgv(t *testing.T, cmd []string) {
	t.Helper()
	if strings.Contains(strings.Join(cmd, " "), "do the thing") {
		t.Fatalf("cmd = %#v must not carry the prompt; AO delivers it after startup", cmd)
	}
}

func containsSequence(cmd []string, value string) bool {
	for _, arg := range cmd {
		if arg == value {
			return true
		}
	}
	return false
}

func containsPair(cmd []string, flag, value string) bool {
	for i := 0; i+1 < len(cmd); i++ {
		if cmd[i] == flag && cmd[i+1] == value {
			return true
		}
	}
	return false
}
