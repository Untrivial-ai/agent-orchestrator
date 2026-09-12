package auggie

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

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hooksjson"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestManifest(t *testing.T) {
	m := (&Plugin{}).Manifest()
	if m.ID != "auggie" {
		t.Fatalf("ID = %q, want auggie", m.ID)
	}
	if m.Name != "Auggie" {
		t.Fatalf("Name = %q, want Auggie", m.Name)
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

func TestGetConfigSpecReportsModelField(t *testing.T) {
	spec, err := (&Plugin{}).GetConfigSpec(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []ports.ConfigField{
		{
			Key:         "model",
			Type:        ports.ConfigFieldString,
			Description: "Model override passed to `auggie --model`.",
		},
	}
	if !reflect.DeepEqual(spec.Fields, want) {
		t.Fatalf("config fields\nwant: %#v\n got: %#v", want, spec.Fields)
	}
}

func TestGetPromptDeliveryStrategy(t *testing.T) {
	s, err := (&Plugin{}).GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if s != ports.PromptDeliveryInCommand {
		t.Fatalf("strategy = %q, want %q", s, ports.PromptDeliveryInCommand)
	}
}

func TestGetLaunchCommandWithPrompt(t *testing.T) {
	p := &Plugin{resolvedBinary: "auggie"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions: ports.PermissionModeBypassPermissions,
		Prompt:      "-add a health check",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"auggie", "--print", "--", "-add a health check"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandAppendsConfiguredModel(t *testing.T) {
	p := &Plugin{resolvedBinary: "auggie"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Config: ports.AgentConfig{Model: "  claude-sonnet-4  "},
		Prompt: "fix this",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"auggie", "--print", "--model", "claude-sonnet-4", "--", "fix this"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandOmitsBlankConfiguredModel(t *testing.T) {
	p := &Plugin{resolvedBinary: "auggie"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Config: ports.AgentConfig{Model: " \t "},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"auggie", "--print"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

// TestGetLaunchCommandPermissionModesEmitNoFlag documents that Auggie has no
// blanket auto-approve flag, so every AO permission mode produces the same argv
// (no permission flag) and defers to the user's Auggie config.
func TestGetLaunchCommandPermissionModesEmitNoFlag(t *testing.T) {
	modes := []ports.PermissionMode{
		ports.PermissionModeDefault,
		"",
		ports.PermissionModeAcceptEdits,
		ports.PermissionModeAuto,
		ports.PermissionModeBypassPermissions,
	}
	want := []string{"auggie", "--print"}
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			p := &Plugin{resolvedBinary: "auggie"}
			cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{Permissions: mode})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cmd, want) {
				t.Fatalf("cmd = %#v, want %#v", cmd, want)
			}
			for _, arg := range cmd {
				if arg == "--permission" || arg == "--permission-mode" {
					t.Fatalf("cmd = %#v unexpectedly contains a permission flag", cmd)
				}
			}
		})
	}
}

func TestGetLaunchCommandAppendsRulesFile(t *testing.T) {
	p := &Plugin{resolvedBinary: "auggie"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPromptFile: "/tmp/system.md",
		Prompt:           "do the thing",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"auggie", "--print", "--rules", "/tmp/system.md", "--", "do the thing"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandIgnoresInlineSystemPromptWithoutFile(t *testing.T) {
	p := &Plugin{resolvedBinary: "auggie"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPrompt: "inline ignored",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"auggie", "--print"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
	for _, arg := range cmd {
		if arg == "--instruction" || arg == "inline ignored" {
			t.Fatalf("cmd = %#v unexpectedly contains inline instruction text", cmd)
		}
	}
}

func TestGetRestoreCommand(t *testing.T) {
	p := &Plugin{resolvedBinary: "auggie"}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "sess-abc123"},
		},
		Permissions:      ports.PermissionModeBypassPermissions,
		SystemPrompt:     "restore inline wins",
		SystemPromptFile: "/tmp/system.md",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}

	want := []string{"auggie", "--print", "--rules", "/tmp/system.md", "--resume", "sess-abc123"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandAppendsConfiguredModel(t *testing.T) {
	p := &Plugin{resolvedBinary: "auggie"}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Config: ports.AgentConfig{Model: "  claude-sonnet-4  "},
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "sess-abc123"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}

	want := []string{"auggie", "--print", "--model", "claude-sonnet-4", "--resume", "sess-abc123"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandNoID(t *testing.T) {
	p := &Plugin{resolvedBinary: "auggie"}
	_, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{Metadata: map[string]string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("ok=true with no agentSessionId, want false")
	}
}

func TestGetAgentHooksInstallsExecutableAuggieHooks(t *testing.T) {
	workspace := t.TempDir()
	if err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: workspace}); err != nil {
		t.Fatalf("GetAgentHooks err = %v", err)
	}

	settingsPath := filepath.Join(workspace, ".augment", "settings.local.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read Auggie settings: %v", err)
	}
	var settings struct {
		Hooks map[string][]hooksjson.MatcherGroup `json:"hooks"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parse Auggie settings: %v", err)
	}

	wantEvents := map[string]string{
		"SessionStart": "session-start",
		"PreToolUse":   "pre-tool-use",
		"PostToolUse":  "post-tool-use",
		"Stop":         "stop",
		"SessionEnd":   "session-end",
	}
	for nativeEvent, aoEvent := range wantEvents {
		groups := settings.Hooks[nativeEvent]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("%s hooks = %#v, want one AO hook", nativeEvent, groups)
		}
		hook := groups[0].Hooks[0]
		if hook.Type != "command" || hook.Timeout != 5000 {
			t.Fatalf("%s hook = %#v, want command timeout 5000", nativeEvent, hook)
		}
		if !hookutil.IsExecutableFile(hook.Command) {
			t.Fatalf("%s command %q is not an executable file", nativeEvent, hook.Command)
		}
		script, err := os.ReadFile(hook.Command)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(script), "hooks auggie "+aoEvent) {
			t.Fatalf("%s script does not dispatch %s:\n%s", nativeEvent, aoEvent, script)
		}
		if runtime.GOOS == "windows" && filepath.Ext(hook.Command) != ".cmd" {
			t.Fatalf("Windows hook command = %q, want .cmd", hook.Command)
		}
		if runtime.GOOS != "windows" && filepath.Ext(hook.Command) != ".sh" {
			t.Fatalf("Unix hook command = %q, want .sh", hook.Command)
		}
	}
}

func TestGetAgentHooksPreservesUserSettingsAndIsIdempotent(t *testing.T) {
	workspace := t.TempDir()
	settingsPath := filepath.Join(workspace, ".augment", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o750); err != nil {
		t.Fatal(err)
	}
	original := `{
  "theme": "dark",
  "hooks": {
    "Stop": [{"hooks": [{"type": "command", "command": "/usr/local/bin/user-stop.sh", "timeout": 1234}]}]
  }
}`
	if err := os.WriteFile(settingsPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	plugin := &Plugin{}
	cfg := ports.WorkspaceHookConfig{WorkspacePath: workspace}
	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	var theme string
	if err := json.Unmarshal(settings["theme"], &theme); err != nil || theme != "dark" {
		t.Fatalf("theme = %q, err = %v; want preserved dark", theme, err)
	}
	var hooks map[string][]hooksjson.MatcherGroup
	if err := json.Unmarshal(settings["hooks"], &hooks); err != nil {
		t.Fatal(err)
	}
	stopCommands := map[string]int{}
	for _, group := range hooks["Stop"] {
		for _, hook := range group.Hooks {
			stopCommands[hook.Command]++
		}
	}
	if stopCommands["/usr/local/bin/user-stop.sh"] != 1 {
		t.Fatalf("user Stop hook count = %d, want 1", stopCommands["/usr/local/bin/user-stop.sh"])
	}
	managed := 0
	for command, count := range stopCommands {
		if strings.Contains(command, filepath.Join(".augment", "ao-hooks")) {
			managed += count
		}
	}
	if managed != 1 {
		t.Fatalf("managed Stop hook count = %d, want 1; commands = %#v", managed, stopCommands)
	}
}

func TestSessionInfoReadsHookMetadata(t *testing.T) {
	info, ok, err := (&Plugin{}).SessionInfo(context.Background(), ports.SessionRef{
		Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "sess-abc123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok=false, want hook-derived session info")
	}
	if info.AgentSessionID != "sess-abc123" {
		t.Fatalf("AgentSessionID = %q, want sess-abc123", info.AgentSessionID)
	}
}

func TestResolveAuggieBinaryFallback(t *testing.T) {
	// When the binary is not on PATH or any well-known location, the resolver
	// MUST surface ports.ErrAgentBinaryNotFound rather than a silent string
	// fallback that lets a missing CLI launch into an empty tmux pane.
	bin, err := ResolveAuggieBinary(context.Background())
	if err != nil {
		if !errors.Is(err, ports.ErrAgentBinaryNotFound) {
			t.Fatalf("err = %v, want ports.ErrAgentBinaryNotFound", err)
		}
		return
	}
	if bin == "" {
		t.Fatal("ResolveAuggieBinary returned empty path with no error")
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := (&Plugin{}).GetConfigSpec(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetConfigSpec err = %v, want context.Canceled", err)
	}
	if _, err := (&Plugin{}).GetLaunchCommand(ctx, ports.LaunchConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetLaunchCommand err = %v, want context.Canceled", err)
	}
	if _, err := (&Plugin{}).GetPromptDeliveryStrategy(ctx, ports.LaunchConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetPromptDeliveryStrategy err = %v, want context.Canceled", err)
	}
	if err := (&Plugin{}).GetAgentHooks(ctx, ports.WorkspaceHookConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetAgentHooks err = %v, want context.Canceled", err)
	}
	if _, _, err := (&Plugin{}).GetRestoreCommand(ctx, ports.RestoreConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetRestoreCommand err = %v, want context.Canceled", err)
	}
	if _, _, err := (&Plugin{}).SessionInfo(ctx, ports.SessionRef{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("SessionInfo err = %v, want context.Canceled", err)
	}
}
