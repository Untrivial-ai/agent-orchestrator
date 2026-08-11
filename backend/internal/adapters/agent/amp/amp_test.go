package amp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestManifest(t *testing.T) {
	m := (&Plugin{}).Manifest()
	if m.ID != "amp" {
		t.Fatalf("ID = %q, want amp", m.ID)
	}
	if m.Name != "Amp" {
		t.Fatalf("Name = %q, want Amp", m.Name)
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

func TestGetConfigSpecReportsModes(t *testing.T) {
	spec, err := (&Plugin{}).GetConfigSpec(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(spec.Fields) != 1 || spec.Fields[0].Key != "mode" {
		t.Fatalf("unexpected fields: %#v", spec.Fields)
	}
	if want := []string{"low", "medium", "high", "ultra"}; !reflect.DeepEqual(spec.Fields[0].Enum, want) {
		t.Fatalf("mode enum = %#v, want %#v", spec.Fields[0].Enum, want)
	}
}

func TestGetPromptDeliveryStrategy(t *testing.T) {
	s, err := (&Plugin{}).GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if s != ports.PromptDeliveryAfterStart {
		t.Fatalf("strategy = %q, want %q", s, ports.PromptDeliveryAfterStart)
	}
}

func TestPromptReadinessHints(t *testing.T) {
	hints, err := (&Plugin{}).PromptReadinessHints(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if hints.Timeout <= 0 || len(hints.Patterns) == 0 {
		t.Fatalf("hints = %#v, want bounded readiness patterns", hints)
	}
}

func TestGetLaunchCommandBypassWithPromptLeavesPromptForAfterStartDelivery(t *testing.T) {
	p := &Plugin{resolvedBinary: "amp"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions: ports.PermissionModeBypassPermissions,
		Prompt:      "-add a health check",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"amp"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
	assertAmpPermissionFlagsAbsent(t, cmd)
}

func TestGetLaunchCommandForwardsMode(t *testing.T) {
	p := &Plugin{resolvedBinary: "amp"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{Config: ports.AgentConfig{Mode: " high "}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"amp", "--mode", "high"}; !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandPermissionModesEmitNoFlag(t *testing.T) {
	modes := []ports.PermissionMode{
		ports.PermissionModeDefault,
		"",
		ports.PermissionModeAcceptEdits,
		ports.PermissionModeAuto,
		ports.PermissionModeBypassPermissions,
	}
	want := []string{"amp"}

	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			p := &Plugin{resolvedBinary: "amp"}
			cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{Permissions: mode})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cmd, want) {
				t.Fatalf("cmd = %#v, want %#v", cmd, want)
			}
			assertAmpPermissionFlagsAbsent(t, cmd)
		})
	}
}

func TestGetLaunchCommandIgnoresModelConfig(t *testing.T) {
	// Amp has no CLI/config surface for model selection (see amp.go's
	// GetLaunchCommand doc comment and issue #2902), so a configured model
	// must never appear in the launch argv.
	p := &Plugin{resolvedBinary: "amp"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Config: ports.AgentConfig{Model: "claude-opus-4-5"},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"amp"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v (model must not be forwarded)", cmd, want)
	}
}

func TestGetLaunchCommandUsesPluginForSystemPrompt(t *testing.T) {
	p := &Plugin{resolvedBinary: "amp"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPrompt:     "follow repo rules",
		SystemPromptFile: "/tmp/system.md",
		Prompt:           "do the thing",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"amp"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
	for _, arg := range cmd {
		if arg == "--append-system-prompt" || arg == "--append-system-prompt-file" {
			t.Fatalf("cmd = %#v unexpectedly contains system prompt flag", cmd)
		}
	}
	assertAmpSystemPromptFlagsAbsent(t, cmd)
}

func TestGetLaunchCommandOmitsExecuteModeWithoutPrompt(t *testing.T) {
	p := &Plugin{resolvedBinary: "amp"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPromptFile: "/tmp/system.md",
		SystemPrompt:     "inline wins",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"amp"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
	assertAmpSystemPromptFlagsAbsent(t, cmd)
}

func TestGetLaunchCommandIgnoresSystemPromptFile(t *testing.T) {
	p := &Plugin{resolvedBinary: "amp"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPromptFile: "/tmp/system.md",
		SystemPrompt:     "inline ignored",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"amp"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
	assertAmpSystemPromptFlagsAbsent(t, cmd)
}

func assertAmpPermissionFlagsAbsent(t *testing.T, cmd []string) {
	t.Helper()
	for _, arg := range cmd {
		if arg == "--permission-mode" {
			t.Fatalf("cmd = %#v unexpectedly contains unsupported Amp permission flag", cmd)
		}
	}
}

func assertAmpSystemPromptFlagsAbsent(t *testing.T, cmd []string) {
	t.Helper()
	for _, arg := range cmd {
		switch arg {
		case "--append-system-prompt", "--append-system-prompt-file":
			t.Fatalf("cmd = %#v unexpectedly contains unsupported Amp system prompt flag %q", cmd, arg)
		}
	}
}

func TestGetLaunchCommandPromptlessOmitsPluginReadyTimeout(t *testing.T) {
	p := &Plugin{resolvedBinary: "amp"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"amp"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommand(t *testing.T) {
	p := &Plugin{resolvedBinary: "amp"}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "T-abc123"},
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

	want := []string{"amp", "--resume", "T-abc123"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandIgnoresModelConfig(t *testing.T) {
	// Same rationale as TestGetLaunchCommandIgnoresModelConfig: Amp has no
	// per-model override surface, so a configured model must never appear
	// in the resume argv either.
	p := &Plugin{resolvedBinary: "amp"}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Config: ports.AgentConfig{Model: "claude-opus-4-5"},
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "T-abc123"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}

	want := []string{"amp", "--resume", "T-abc123"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v (model must not be forwarded)", cmd, want)
	}
}

func TestGetRestoreCommandNoID(t *testing.T) {
	p := &Plugin{resolvedBinary: "amp"}
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

func TestGetAgentHooksInstallsSystemPromptPlugin(t *testing.T) {
	workspace := t.TempDir()
	promptFile := filepath.Join(t.TempDir(), "system.md")
	if err := os.WriteFile(promptFile, []byte("AO standing instructions\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		WorkspacePath:    workspace,
		SystemPromptFile: promptFile,
	}); err != nil {
		t.Fatalf("GetAgentHooks err = %v", err)
	}

	data, err := os.ReadFile(ampPluginPath(workspace))
	if err != nil {
		t.Fatalf("read plugin: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		ampPluginSentinel,
		strconv.Quote(promptFile),
		"agent.start",
		"display: false",
		"readFile(systemPromptFile",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("plugin missing %q:\n%s", want, text)
		}
	}
}

func TestGetAgentHooksGitignoresSystemPromptPlugin(t *testing.T) {
	workspace := t.TempDir()
	if err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		WorkspacePath: workspace,
		SystemPrompt:  "AO standing instructions",
	}); err != nil {
		t.Fatalf("GetAgentHooks err = %v", err)
	}

	gitignorePath := filepath.Join(workspace, ampPluginDirName, ampPluginSubDir, ".gitignore")
	data, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	text := string(data)
	for _, want := range []string{hookutil.GitignoreSentinel, "/" + ampPluginFileName, "/.gitignore"} {
		if !strings.Contains(text, want) {
			t.Fatalf(".gitignore missing %q:\n%s", want, text)
		}
	}
}

func TestGetAgentHooksPreservesForeignPluginFiles(t *testing.T) {
	workspace := t.TempDir()
	foreignPath := filepath.Join(workspace, ampPluginDirName, ampPluginSubDir, "user-plugin.ts")
	if err := os.MkdirAll(filepath.Dir(foreignPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreignPath, []byte("export default function userPlugin() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		WorkspacePath:    workspace,
		SystemPromptFile: filepath.Join(t.TempDir(), "missing.md"),
	}); err != nil {
		t.Fatalf("GetAgentHooks err = %v", err)
	}

	data, err := os.ReadFile(foreignPath)
	if err != nil {
		t.Fatalf("read foreign plugin: %v", err)
	}
	if got := string(data); got != "export default function userPlugin() {}\n" {
		t.Fatalf("foreign plugin changed:\n%s", got)
	}
}

func TestGetAgentHooksRequiresWorkspacePath(t *testing.T) {
	err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{})
	if err == nil {
		t.Fatal("GetAgentHooks err = nil, want error")
	}
	if !strings.Contains(err.Error(), "WorkspacePath is required") {
		t.Fatalf("GetAgentHooks err = %v, want WorkspacePath message", err)
	}
}

func TestGetAgentHooksSystemPromptFileTakesPrecedenceOverInline(t *testing.T) {
	workspace := t.TempDir()
	promptFile := filepath.Join(t.TempDir(), "system.md")
	if err := os.WriteFile(promptFile, []byte("file rules\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		WorkspacePath:    workspace,
		SystemPrompt:     "inline rules",
		SystemPromptFile: promptFile,
	}); err != nil {
		t.Fatalf("GetAgentHooks err = %v", err)
	}

	data, err := os.ReadFile(ampPluginPath(workspace))
	if err != nil {
		t.Fatalf("read plugin: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, strconv.Quote(promptFile)) {
		t.Fatalf("plugin missing prompt file path:\n%s", text)
	}
	if strings.Contains(text, "inline rules") {
		t.Fatalf("inline prompt should not be embedded when prompt file is provided:\n%s", text)
	}
}

func TestGetAgentHooksUsesInlineSystemPromptWithoutFile(t *testing.T) {
	workspace := t.TempDir()
	if err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		WorkspacePath: workspace,
		SystemPrompt:  "inline rules",
	}); err != nil {
		t.Fatalf("GetAgentHooks err = %v", err)
	}

	data, err := os.ReadFile(ampPluginPath(workspace))
	if err != nil {
		t.Fatalf("read plugin: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "inline rules") {
		t.Fatalf("plugin missing inline prompt:\n%s", text)
	}
	if !strings.Contains(text, `const systemPromptFile = ""`) {
		t.Fatalf("plugin should not point at a prompt file:\n%s", text)
	}
}

func TestSessionInfoNoOp(t *testing.T) {
	info, ok, err := (&Plugin{}).SessionInfo(context.Background(), ports.SessionRef{
		Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "T-abc123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("ok=true with info %#v, want no-op false", info)
	}
	if !reflect.DeepEqual(info, ports.SessionInfo{}) {
		t.Fatalf("info = %#v, want zero", info)
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
	if _, err := (&Plugin{}).PromptReadinessHints(ctx, ports.LaunchConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("PromptReadinessHints err = %v, want context.Canceled", err)
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
