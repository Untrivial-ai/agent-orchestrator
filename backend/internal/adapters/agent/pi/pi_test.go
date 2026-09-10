package pi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestManifest(t *testing.T) {
	m := (&Plugin{}).Manifest()
	if m.ID != "pi" {
		t.Fatalf("ID = %q, want pi", m.ID)
	}
	if m.Name != "Pi" {
		t.Fatalf("Name = %q, want Pi", m.Name)
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
			Description: "Model override passed to `pi --model`.",
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

func TestGetLaunchCommandWorkerWithPromptIsInteractive(t *testing.T) {
	p := &Plugin{resolvedBinary: "pi"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Kind:   domain.KindWorker,
		Prompt: "add a health check",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"pi", "add a health check"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandAppendsConfiguredModelBeforePrompt(t *testing.T) {
	p := &Plugin{resolvedBinary: "pi"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Config:       ports.AgentConfig{Model: "  openai/gpt-4o  "},
		SystemPrompt: "follow repo rules",
		Prompt:       "add a health check",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"pi", "--append-system-prompt", "follow repo rules", "--model", "openai/gpt-4o", "add a health check"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandOmitsBlankConfiguredModel(t *testing.T) {
	p := &Plugin{resolvedBinary: "pi"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Config: ports.AgentConfig{Model: " \t "},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"pi"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandOrchestratorAppendsSystemPromptInteractively(t *testing.T) {
	p := &Plugin{resolvedBinary: "pi"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Kind:         domain.KindOrchestrator,
		SystemPrompt: "coordinate work and avoid implementation",
		Prompt:       "plan the issue",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"pi", "--append-system-prompt", "coordinate work and avoid implementation", "plan the issue"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandEmitsNoPermissionFlag(t *testing.T) {
	// Pi has no permission CLI surface; every mode must produce the same argv
	// and never emit a permission flag.
	modes := []ports.PermissionMode{
		ports.PermissionModeDefault,
		"",
		ports.PermissionModeAcceptEdits,
		ports.PermissionModeAuto,
		ports.PermissionModeBypassPermissions,
	}

	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			p := &Plugin{resolvedBinary: "pi"}
			cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{Permissions: mode})
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"pi"}
			if !reflect.DeepEqual(cmd, want) {
				t.Fatalf("cmd = %#v, want %#v", cmd, want)
			}
			for _, arg := range cmd {
				if arg == "--permission-mode" {
					t.Fatalf("cmd = %#v unexpectedly contains a permission flag", cmd)
				}
			}
		})
	}
}

func TestGetLaunchCommandAppendsSystemPrompt(t *testing.T) {
	p := &Plugin{resolvedBinary: "pi"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPrompt: "follow repo rules",
		Prompt:       "do the thing",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"pi", "--append-system-prompt", "follow repo rules", "do the thing"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandPrefersInlineSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "system.md")
	if err := os.WriteFile(file, []byte("file contents win"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := &Plugin{resolvedBinary: "pi"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPromptFile: file,
		SystemPrompt:     "inline wins",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"pi", "--append-system-prompt", "inline wins"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandSystemPromptFileReadError(t *testing.T) {
	p := &Plugin{resolvedBinary: "pi"}
	_, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPromptFile: filepath.Join(t.TempDir(), "missing.md"),
	})
	if err == nil {
		t.Fatal("expected error for unreadable system-prompt file, got nil")
	}
}

func TestGetRestoreCommand(t *testing.T) {
	p := &Plugin{resolvedBinary: "pi"}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		SystemPrompt:     "restore inline wins",
		SystemPromptFile: filepath.Join(t.TempDir(), "missing.md"),
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "019e950e-52e0-7411-961b-d380ca7e610f"},
		},
		Permissions: ports.PermissionModeBypassPermissions,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}

	want := []string{"pi", "--append-system-prompt", "restore inline wins", "--session", "019e950e-52e0-7411-961b-d380ca7e610f"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandAppendsConfiguredModelBeforeSession(t *testing.T) {
	p := &Plugin{resolvedBinary: "pi"}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Config: ports.AgentConfig{Model: "  openai/gpt-4o  "},
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "019e950e-52e0-7411-961b-d380ca7e610f"},
		},
		SystemPrompt: "follow repo rules",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}

	want := []string{"pi", "--append-system-prompt", "follow repo rules", "--model", "openai/gpt-4o", "--session", "019e950e-52e0-7411-961b-d380ca7e610f"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandReappendsSystemPromptInteractively(t *testing.T) {
	p := &Plugin{resolvedBinary: "pi"}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Kind:         domain.KindOrchestrator,
		Session:      ports.SessionRef{Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "019e950e-52e0-7411-961b-d380ca7e610f"}},
		SystemPrompt: "coordinate work and avoid implementation",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}

	want := []string{"pi", "--append-system-prompt", "coordinate work and avoid implementation", "--session", "019e950e-52e0-7411-961b-d380ca7e610f"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandNoID(t *testing.T) {
	p := &Plugin{resolvedBinary: "pi"}
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

func TestGetLaunchCommandExplicitlyLoadsManagedExtension(t *testing.T) {
	workspace := t.TempDir()
	p := &Plugin{resolvedBinary: "pi"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		WorkspacePath: workspace,
		Prompt:        "fix the bug",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"pi", "--extension", filepath.Join(workspace, ".pi", "extensions", "ao-activity.ts"), "fix the bug"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandExplicitlyLoadsManagedExtension(t *testing.T) {
	workspace := t.TempDir()
	p := &Plugin{resolvedBinary: "pi"}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			WorkspacePath: workspace,
			Metadata:      map[string]string{ports.MetadataKeyAgentSessionID: "native-pi-1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}
	want := []string{"pi", "--extension", filepath.Join(workspace, ".pi", "extensions", "ao-activity.ts"), "--session", "native-pi-1"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetAgentHooksInstallsManagedActivityExtension(t *testing.T) {
	workspace := t.TempDir()
	if err := (&Plugin{resolvedBinary: fakePiBinary(t, "0.80.6")}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: workspace}); err != nil {
		t.Fatalf("GetAgentHooks err = %v", err)
	}

	extensionPath := filepath.Join(workspace, ".pi", "extensions", "ao-activity.ts")
	data, err := os.ReadFile(extensionPath)
	if err != nil {
		t.Fatalf("read managed extension: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"agent-orchestrator: managed pi activity extension",
		`pi.on("session_start"`,
		`pi.on("before_agent_start"`,
		`pi.on("agent_end"`,
		`pi.on("agent_settled"`,
		`pi.on("session_shutdown"`,
		`"hooks", "pi", hookName`,
		"getSessionId()",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("managed extension missing %q:\n%s", want, text)
		}
	}

	gitignore, err := os.ReadFile(filepath.Join(workspace, ".pi", "extensions", ".gitignore"))
	if err != nil {
		t.Fatalf("read extension .gitignore: %v", err)
	}
	if !strings.Contains(string(gitignore), "/ao-activity.ts") {
		t.Fatalf("extension .gitignore does not ignore AO file:\n%s", gitignore)
	}
}

func TestGetAgentHooksRefusesForeignManagedPath(t *testing.T) {
	workspace := t.TempDir()
	extensionPath := filepath.Join(workspace, ".pi", "extensions", "ao-activity.ts")
	if err := os.MkdirAll(filepath.Dir(extensionPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extensionPath, []byte("export default function userExtension() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := (&Plugin{resolvedBinary: fakePiBinary(t, "0.80.6")}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: workspace})
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("GetAgentHooks err = %v, want foreign-file refusal", err)
	}
}

func TestSessionInfoReadsHookMetadata(t *testing.T) {
	info, ok, err := (&Plugin{}).SessionInfo(context.Background(), ports.SessionRef{
		Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "019e950e-52e0-7411-961b-d380ca7e610f"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok=false, want hook-derived session info")
	}
	if info.AgentSessionID != "019e950e-52e0-7411-961b-d380ca7e610f" {
		t.Fatalf("AgentSessionID = %q, want native Pi id", info.AgentSessionID)
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := (&Plugin{}).GetConfigSpec(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetConfigSpec err = %v, want context.Canceled", err)
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

func TestResolvePiBinaryContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ResolvePiBinary(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolvePiBinary err = %v, want context.Canceled", err)
	}
}
