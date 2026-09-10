package omp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestManifest(t *testing.T) {
	m := (&Plugin{}).Manifest()
	if m.ID != "omp" {
		t.Fatalf("ID = %q, want omp", m.ID)
	}
	if m.Name != "OMP" {
		t.Fatalf("Name = %q, want OMP", m.Name)
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
	want := []ports.ConfigField{{
		Key:         "model",
		Type:        ports.ConfigFieldString,
		Description: "Model override passed to `omp --model`.",
	}}
	if !reflect.DeepEqual(spec.Fields, want) {
		t.Fatalf("config fields\nwant: %#v\n got: %#v", want, spec.Fields)
	}
}

func TestGetPromptDeliveryStrategyIsInCommand(t *testing.T) {
	got, err := (&Plugin{}).GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != ports.PromptDeliveryInCommand {
		t.Fatalf("strategy = %q, want %q", got, ports.PromptDeliveryInCommand)
	}
}

func TestGetLaunchCommandStartsInteractiveTUIWithPrompt(t *testing.T) {
	p := &Plugin{resolvedBinary: "omp"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Prompt: "add a health check",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"omp", "add a health check"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandExplicitlyLoadsManagedExtension(t *testing.T) {
	workspace := t.TempDir()
	p := &Plugin{resolvedBinary: "omp"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		WorkspacePath: workspace,
		Prompt:        "add a health check",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"omp", "--extension", filepath.Join(workspace, ".omp", "extensions", "ao-activity.ts"), "add a health check"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandAppendsSystemPromptAndModel(t *testing.T) {
	p := &Plugin{resolvedBinary: "omp"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPrompt: "follow repo rules",
		Config:       ports.AgentConfig{Model: "  anthropic/claude-sonnet-4  "},
		Prompt:       "implement it",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"omp", "--append-system-prompt", "follow repo rules", "--model", "anthropic/claude-sonnet-4", "implement it"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandReadsSystemPromptFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "system.md")
	if err := os.WriteFile(file, []byte("file prompt"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := &Plugin{resolvedBinary: "omp"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPromptFile: file,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"omp", "--append-system-prompt", "file prompt"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandUsesNativeSessionID(t *testing.T) {
	p := &Plugin{resolvedBinary: "omp"}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		SystemPrompt: "restore rules",
		Config:       ports.AgentConfig{Model: "openai/gpt-5-codex"},
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "019e950e-52e0-7411-961b-d380ca7e610f"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}
	want := []string{"omp", "--append-system-prompt", "restore rules", "--model", "openai/gpt-5-codex", "--resume", "019e950e-52e0-7411-961b-d380ca7e610f"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandExplicitlyLoadsManagedExtension(t *testing.T) {
	workspace := t.TempDir()
	p := &Plugin{resolvedBinary: "omp"}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			WorkspacePath: workspace,
			Metadata:      map[string]string{ports.MetadataKeyAgentSessionID: "native-omp-1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}
	want := []string{"omp", "--extension", filepath.Join(workspace, ".omp", "extensions", "ao-activity.ts"), "--resume", "native-omp-1"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandWithoutNativeSessionIDReturnsNotOK(t *testing.T) {
	p := &Plugin{resolvedBinary: "omp"}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if ok || cmd != nil {
		t.Fatalf("cmd=%#v ok=%v, want nil false", cmd, ok)
	}
}

func TestGetAgentHooksInstallsManagedActivityExtension(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake OMP version binary uses a Unix shebang")
	}
	workspace := t.TempDir()
	plugin := &Plugin{resolvedBinary: fakeOMPVersionBinary(t, "17.1.0")}
	if err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: workspace}); err != nil {
		t.Fatalf("GetAgentHooks err = %v", err)
	}

	extensionPath := filepath.Join(workspace, ".omp", "extensions", "ao-activity.ts")
	data, err := os.ReadFile(extensionPath)
	if err != nil {
		t.Fatalf("read managed extension: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"agent-orchestrator: managed omp activity extension",
		`omp.on("session_start"`,
		`omp.on("session_switch"`,
		`omp.on("before_agent_start"`,
		`omp.on("agent_end"`,
		`omp.on("tool_approval_requested"`,
		`omp.on("tool_approval_resolved"`,
		`omp.on("session_shutdown"`,
		`"hooks", "omp", hookName`,
		"getSessionId()",
		"if (!event.willContinue)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("managed extension missing %q:\n%s", want, text)
		}
	}

	gitignore, err := os.ReadFile(filepath.Join(workspace, ".omp", "extensions", ".gitignore"))
	if err != nil {
		t.Fatalf("read extension .gitignore: %v", err)
	}
	if !strings.Contains(string(gitignore), "/ao-activity.ts") {
		t.Fatalf("extension .gitignore does not ignore AO file:\n%s", gitignore)
	}
}

func TestGetAgentHooksRequiresOMP171LifecycleContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake OMP version binary uses a Unix shebang")
	}
	for _, tc := range []struct {
		version string
		wantErr bool
	}{
		{version: "16.9.9", wantErr: true},
		{version: "17.0.0", wantErr: true},
		{version: "17.1.0", wantErr: false},
		{version: "18.1.0", wantErr: false},
	} {
		t.Run(tc.version, func(t *testing.T) {
			plugin := &Plugin{resolvedBinary: fakeOMPVersionBinary(t, tc.version)}
			err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: t.TempDir()})
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "requires OMP 17.1.0 or newer") {
					t.Fatalf("GetAgentHooks with OMP %s error = %v, want minimum-version error", tc.version, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetAgentHooks with OMP %s: %v", tc.version, err)
			}
		})
	}
}

func TestGetAgentHooksRefusesForeignManagedPath(t *testing.T) {
	workspace := t.TempDir()
	extensionPath := filepath.Join(workspace, ".omp", "extensions", "ao-activity.ts")
	want := "export default function userExtension() {}\n"
	if err := os.MkdirAll(filepath.Dir(extensionPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extensionPath, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}

	err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: workspace})
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("GetAgentHooks err = %v, want foreign-file refusal", err)
	}
	got, readErr := os.ReadFile(extensionPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != want {
		t.Fatalf("foreign extension changed to %q", got)
	}
}

func TestDeriveActivityState(t *testing.T) {
	tests := []struct {
		event string
		want  domain.ActivityState
	}{
		{"session-start", domain.ActivityIdle},
		{"user-prompt-submit", domain.ActivityActive},
		{"permission-request", domain.ActivityWaitingInput},
		{"permission-resolved", domain.ActivityActive},
		{"stop", domain.ActivityIdle},
		{"session-end", domain.ActivityExited},
	}
	for _, tt := range tests {
		t.Run(tt.event, func(t *testing.T) {
			got, ok := DeriveActivityState(tt.event, nil)
			if !ok || got != tt.want {
				t.Fatalf("DeriveActivityState(%q) = (%q, %v), want (%q, true)", tt.event, got, ok, tt.want)
			}
		})
	}
	if got, ok := DeriveActivityState("unknown", nil); ok {
		t.Fatalf("DeriveActivityState(unknown) = (%q, true), want no signal", got)
	}
}

func TestGetAgentHooksHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (&Plugin{}).GetAgentHooks(ctx, ports.WorkspaceHookConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetAgentHooks err = %v, want context.Canceled", err)
	}
}

func fakeOMPVersionBinary(t *testing.T, version string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "omp")
	script := "#!/bin/sh\nif [ \"${1:-}\" = \"--version\" ]; then\n  echo \"omp/" + version + "\"\n  exit 0\nfi\nexit 2\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
