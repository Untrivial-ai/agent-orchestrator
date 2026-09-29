package fx

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestManifestIdentifiesFXAgent(t *testing.T) {
	manifest := (&Plugin{}).Manifest()
	if manifest.ID != "fx" || manifest.Name != "fx" {
		t.Fatalf("manifest identity = (%q, %q), want (fx, fx)", manifest.ID, manifest.Name)
	}
	if !reflect.DeepEqual(manifest.Capabilities, []adapters.Capability{adapters.CapabilityAgent}) {
		t.Fatalf("capabilities = %#v, want [agent]", manifest.Capabilities)
	}
}

func TestGetConfigSpecExposesOptionalFreeFormModel(t *testing.T) {
	spec, err := (&Plugin{}).GetConfigSpec(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.ConfigField{{
		Key:         "model",
		Type:        ports.ConfigFieldString,
		Description: "Model override passed through `FX_MODEL`.",
	}}
	if !reflect.DeepEqual(spec.Fields, want) {
		t.Fatalf("fields = %#v, want %#v", spec.Fields, want)
	}
}

func TestGetLaunchCommandMapsEnvironmentOverrides(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		permissions ports.PermissionMode
		want        []string
	}{
		{name: "empty defaults omitted", want: []string{"fx"}},
		{name: "default permissions omitted", permissions: ports.PermissionModeDefault, want: []string{"fx"}},
		{name: "model preserved exactly", model: "  anthropic/claude-sonnet-4-6  ", want: []string{"env", "FX_MODEL=  anthropic/claude-sonnet-4-6  ", "fx"}},
		{name: "accept edits", permissions: ports.PermissionModeAcceptEdits, want: []string{"env", "FX_PERMISSION_MODE=ask", "fx"}},
		{name: "auto", permissions: ports.PermissionModeAuto, want: []string{"env", "FX_PERMISSION_MODE=auto", "fx"}},
		{name: "bypass permissions", permissions: ports.PermissionModeBypassPermissions, want: []string{"env", "FX_PERMISSION_MODE=full-access", "fx"}},
		{name: "model and permissions", model: "openai/gpt-5", permissions: ports.PermissionModeAuto, want: []string{"env", "FX_MODEL=openai/gpt-5", "FX_PERMISSION_MODE=auto", "fx"}},
		{name: "unknown permissions omitted", permissions: ports.PermissionMode("future"), want: []string{"fx"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plugin := &Plugin{resolvedBinary: "fx"}
			got, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
				Config:      ports.AgentConfig{Model: tc.model},
				Permissions: tc.permissions,
				Prompt:      "deliver after startup",
			})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("command = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestPromptDeliveryWaitsBrieflyAfterStart(t *testing.T) {
	plugin := &Plugin{}
	strategy, err := plugin.GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if strategy != ports.PromptDeliveryAfterStart {
		t.Fatalf("strategy = %q, want %q", strategy, ports.PromptDeliveryAfterStart)
	}

	hints, err := plugin.PromptReadinessHints(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if hints.InitialDelay != 750*time.Millisecond {
		t.Fatalf("initial delay = %s, want 750ms", hints.InitialDelay)
	}
	if len(hints.Patterns) != 0 || hints.Timeout != 0 {
		t.Fatalf("hints = %#v, want delay-only readiness without terminal text matching", hints)
	}
}

func TestContinuationCapabilitiesUseProviderAssignedNativeSessionID(t *testing.T) {
	provider, ok := any(New()).(ports.AgentContinuationCapabilityProvider)
	if !ok {
		t.Fatal("fx must declare its native-session ownership for agent switching")
	}
	if got := provider.ContinuationCapabilities().FreshNativeSessionID; got != ports.FreshNativeSessionIDProviderAssigned {
		t.Fatalf("fresh native-session id mode = %q, want %q", got, ports.FreshNativeSessionIDProviderAssigned)
	}
}

func TestNativeSessionConfigDirUsesInvocationHome(t *testing.T) {
	provider, ok := any(New()).(ports.AgentNativeSessionConfigProvider)
	if !ok {
		t.Fatal("fx must expose the native session state root used by switching")
	}
	home := t.TempDir()
	got, err := provider.NativeSessionConfigDir(context.Background(), map[string]string{"HOME": home})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".fx"); got != want {
		t.Fatalf("native session config dir = %q, want %q", got, want)
	}
}

func TestAfterStartPromptWrapsStandingInstructionsAndTaskInOneTurn(t *testing.T) {
	builder, ok := any(New()).(ports.AgentAfterStartPromptBuilder)
	if !ok {
		t.Fatal("fx must compose its standing instructions and task for after-start delivery")
	}
	got, err := builder.BuildAfterStartPrompt(context.Background(), ports.LaunchConfig{
		SystemPrompt: "Keep changes surgical.",
		Prompt:       "Implement agent switching.",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `## AO Standing Instructions

Keep changes surgical.

## AO Task

Implement agent switching.`
	if got != want {
		t.Fatalf("after-start prompt = %q, want %q", got, want)
	}
}

func TestGetRestoreCommandUsesNativeSessionIDAndOverrides(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "fx"}
	got, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Config:      ports.AgentConfig{Model: "openai/gpt-5"},
		Permissions: ports.PermissionModeBypassPermissions,
		Prompt:      "deliver after startup",
		Session: ports.SessionRef{Metadata: map[string]string{
			ports.MetadataKeyAgentSessionID: "  native-fx-session  ",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{"env", "FX_MODEL=openai/gpt-5", "FX_PERMISSION_MODE=full-access", "fx", "resume", "native-fx-session"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command = %#v, want %#v", got, want)
	}
}

func TestGetRestoreCommandWithoutNativeSessionIDReturnsNotOK(t *testing.T) {
	got, ok, err := (&Plugin{resolvedBinary: "fx"}).GetRestoreCommand(context.Background(), ports.RestoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if ok || got != nil {
		t.Fatalf("command = %#v, ok = %v; want nil, false", got, ok)
	}
}

func TestSessionInfoUsesStandardMetadata(t *testing.T) {
	info, ok, err := (&Plugin{}).SessionInfo(context.Background(), ports.SessionRef{Metadata: map[string]string{
		ports.MetadataKeyAgentSessionID: "native-1",
		ports.MetadataKeyTitle:          "Fix tests",
		ports.MetadataKeySummary:        "Updated the adapter",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || info.AgentSessionID != "native-1" || info.Title != "Fix tests" || info.Summary != "Updated the adapter" {
		t.Fatalf("SessionInfo = (%#v, %v), want standard metadata", info, ok)
	}
}

func TestGetAgentHooksIsNoOp(t *testing.T) {
	workspace := t.TempDir()
	if err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: workspace}); err != nil {
		t.Fatal(err)
	}
}

func TestAdapterMethodsHonorCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plugin := &Plugin{resolvedBinary: "fx"}

	if _, err := plugin.GetConfigSpec(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetConfigSpec error = %v, want context.Canceled", err)
	}
	if _, err := plugin.GetLaunchCommand(ctx, ports.LaunchConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetLaunchCommand error = %v, want context.Canceled", err)
	}
	if _, err := plugin.GetPromptDeliveryStrategy(ctx, ports.LaunchConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetPromptDeliveryStrategy error = %v, want context.Canceled", err)
	}
	if _, err := plugin.PromptReadinessHints(ctx, ports.LaunchConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("PromptReadinessHints error = %v, want context.Canceled", err)
	}
	if _, err := plugin.BuildAfterStartPrompt(ctx, ports.LaunchConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("BuildAfterStartPrompt error = %v, want context.Canceled", err)
	}
	if _, err := plugin.NativeSessionConfigDir(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("NativeSessionConfigDir error = %v, want context.Canceled", err)
	}
	if err := plugin.GetAgentHooks(ctx, ports.WorkspaceHookConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetAgentHooks error = %v, want context.Canceled", err)
	}
	if _, _, err := plugin.GetRestoreCommand(ctx, ports.RestoreConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetRestoreCommand error = %v, want context.Canceled", err)
	}
	if _, _, err := plugin.SessionInfo(ctx, ports.SessionRef{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("SessionInfo error = %v, want context.Canceled", err)
	}
}
