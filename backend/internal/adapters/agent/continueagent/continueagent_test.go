package continueagent

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestManifest(t *testing.T) {
	m := (&Plugin{}).Manifest()
	if m.ID != "continue" {
		t.Fatalf("ID = %q, want continue", m.ID)
	}
	if m.Name != "Continue" {
		t.Fatalf("Name = %q, want Continue", m.Name)
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

func TestImplementsAuthChecker(t *testing.T) {
	if _, ok := any(&Plugin{}).(ports.AgentAuthChecker); !ok {
		t.Fatal("Continue must implement AgentAuthChecker using local credential evidence")
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

func TestGetPromptDeliveryStrategyNoPrompt(t *testing.T) {
	s, err := (&Plugin{}).GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if s != ports.PromptDeliveryAfterStart {
		t.Fatalf("strategy = %q, want after_start", s)
	}
}

func TestGetPromptDeliveryStrategyWithPrompt(t *testing.T) {
	s, err := (&Plugin{}).GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{Prompt: "fix it"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if s != ports.PromptDeliveryInCommand {
		t.Fatalf("strategy = %q, want in_command", s)
	}
}

func TestGetPromptDeliveryStrategyContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&Plugin{}).GetPromptDeliveryStrategy(ctx, ports.LaunchConfig{}); err == nil {
		t.Fatal("expected error from canceled context, got nil")
	}
}

func TestGetLaunchCommandWorkerBypassIsInteractive(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Kind:        domain.KindWorker,
		Prompt:      "do the thing",
		Permissions: ports.PermissionModeBypassPermissions,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"cn", "--auto", "--", "do the thing"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandForwardsModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Config: ports.AgentConfig{Model: "  anthropic/claude-sonnet  "},
		Prompt: "fix it",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cn", "--model", "anthropic/claude-sonnet", "--", "fix it"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandWorkerAutoIsInteractive(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Kind:        domain.KindWorker,
		Prompt:      "refactor auth",
		Permissions: ports.PermissionModeAuto,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"cn", "--auto", "--", "refactor auth"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandWorkerDefaultPermsIsInteractive(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Kind:   domain.KindWorker,
		Prompt: "fix it",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"cn", "--", "fix it"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
	joined := strings.Join(cmd, " ")
	if strings.Contains(joined, "--print") || strings.Contains(joined, "--auto") || strings.Contains(joined, "--readonly") {
		t.Fatal("should launch interactively and emit no permission flag for default perms")
	}
}

func TestGetLaunchCommandAppendsInlineRule(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Prompt:           "fix it",
		SystemPrompt:     "follow AO rules",
		SystemPromptFile: "/tmp/system.md",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"cn", "--rule", "follow AO rules", "--", "fix it"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandAppendsRuleFile(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Prompt:           "fix it",
		SystemPromptFile: "/tmp/system.md",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"cn", "--rule", "/tmp/system.md", "--", "fix it"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandAcceptEditsNoFlag(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Prompt:      "tidy up",
		Permissions: ports.PermissionModeAcceptEdits,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"cn", "--", "tidy up"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v (accept-edits should emit no flag)", cmd, want)
	}
}

func TestGetLaunchCommandNoPrompt(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"cn"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandNoPromptWithAuto(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions: ports.PermissionModeAuto,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"cn", "--auto"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Force binary resolution (unset cache) so ctx.Err() is hit.
	_, err := (&Plugin{}).GetLaunchCommand(ctx, ports.LaunchConfig{Prompt: "x"})
	if err == nil {
		t.Fatal("expected error from canceled context, got nil")
	}
}

func TestGetRestoreCommand(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			Metadata: map[string]string{
				ports.MetadataKeyAgentSessionID: "sess-abc123",
			},
		},
		Permissions: ports.PermissionModeBypassPermissions,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}
	want := []string{"cn", "--auto", "--fork", "sess-abc123"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandDefaultPerms(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{
			Metadata: map[string]string{
				ports.MetadataKeyAgentSessionID: "sess-xyz",
			},
		},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}
	want := []string{"cn", "--fork", "sess-xyz"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandAppendsRule(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		SystemPrompt: "restore rules",
		Session: ports.SessionRef{
			Metadata: map[string]string{
				ports.MetadataKeyAgentSessionID: "sess-xyz",
			},
		},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}
	want := []string{"cn", "--rule", "restore rules", "--fork", "sess-xyz"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandNoID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	_, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{Metadata: map[string]string{}},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ok {
		t.Fatal("ok=true with no agentSessionId, want false")
	}
}

func TestGetRestoreCommandWhitespaceID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	_, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{Metadata: map[string]string{
			ports.MetadataKeyAgentSessionID: "   ",
		}},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ok {
		t.Fatal("ok=true with whitespace agentSessionId, want false")
	}
}

func TestSessionInfoReadsHookMetadata(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	info, ok, err := plugin.SessionInfo(context.Background(), ports.SessionRef{
		Metadata: map[string]string{
			ports.MetadataKeyAgentSessionID: "cn-ses-1",
			ports.MetadataKeyTitle:          "Fix login redirect",
			ports.MetadataKeySummary:        "Updated the auth callback and tests.",
		},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}
	if info.AgentSessionID != "cn-ses-1" {
		t.Fatalf("AgentSessionID = %q, want cn-ses-1", info.AgentSessionID)
	}
	if info.Title != "Fix login redirect" {
		t.Fatalf("Title = %q", info.Title)
	}
	if info.Summary != "Updated the auth callback and tests." {
		t.Fatalf("Summary = %q", info.Summary)
	}
}

func TestSessionInfoFalseWhenNoHookMetadata(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "cn"}
	info, ok, err := plugin.SessionInfo(context.Background(), ports.SessionRef{
		Metadata: map[string]string{},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ok {
		t.Fatalf("ok=true with empty metadata, want false")
	}
	if !reflect.DeepEqual(info, ports.SessionInfo{}) {
		t.Fatalf("info = %#v, want zero", info)
	}
}

func TestGetAgentHooksDelegates(t *testing.T) {
	// We don't exercise the full hook merge here (claude tests cover it); just
	// ensure delegation is wired and succeeds against a temp workspace.
	plugin := &Plugin{resolvedBinary: "cn"}
	ws := t.TempDir()
	if err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{
		WorkspacePath: ws,
		SessionID:     "continue-test-1",
	}); err != nil {
		t.Fatalf("GetAgentHooks: %v", err)
	}
}

func TestResolveContinueBinaryContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ResolveContinueBinary(ctx); err == nil {
		t.Fatal("expected error from canceled context, got nil")
	}
}
