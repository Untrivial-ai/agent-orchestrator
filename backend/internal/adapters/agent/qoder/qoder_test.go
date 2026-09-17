package qoder

import (
	"context"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestLaunchCommand(t *testing.T) {
	tests := []struct {
		name string
		cfg  ports.LaunchConfig
		want []string
	}{
		{name: "worker", cfg: ports.LaunchConfig{Kind: domain.KindWorker, NativeSessionID: "native-1", Permissions: ports.PermissionModeAuto, Prompt: "task"}, want: []string{"qoder", "--session-id", "native-1", "--permission-mode", "auto", "--prompt-interactive", "task"}},
		{name: "orchestrator", cfg: ports.LaunchConfig{Kind: domain.KindOrchestrator, NativeSessionID: "native-1", Permissions: ports.PermissionModeAuto, Prompt: "coordinate"}, want: []string{"qoder", "--session-id", "native-1", "--permission-mode", "auto", "--prompt-interactive", "coordinate"}},
		{name: "configured", cfg: ports.LaunchConfig{NativeSessionID: "native-1", Config: ports.AgentConfig{Model: "performance", Effort: "high"}, Permissions: ports.PermissionModeAcceptEdits, SystemPrompt: "AO role", AllowedTools: []string{"Read", "Bash"}, DisallowedTools: []string{"Write"}, Prompt: "task"}, want: []string{"qoder", "--session-id", "native-1", "--model", "performance", "--reasoning-effort", "high", "--permission-mode", "accept_edits", "--allowed-tools", "Read", "--allowed-tools", "Bash", "--disallowed-tools", "Write", "--append-system-prompt", "AO role", "--prompt-interactive", "task"}},
		{name: "default permission and leading dash", cfg: ports.LaunchConfig{NativeSessionID: "native-1", Prompt: "-keep-as-one-argument"}, want: []string{"qoder", "--session-id", "native-1", "--prompt-interactive", "-keep-as-one-argument"}},
		{name: "bypass", cfg: ports.LaunchConfig{NativeSessionID: "native-1", Permissions: ports.PermissionModeBypassPermissions}, want: []string{"qoder", "--session-id", "native-1", "--permission-mode", "bypass_permissions"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New()
			p.resolvedBinary = "qoder"
			got, err := p.GetLaunchCommand(context.Background(), tt.cfg)
			if err != nil {
				t.Fatalf("GetLaunchCommand: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("command = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestLaunchRequiresNativeSessionID(t *testing.T) {
	p := New()
	p.resolvedBinary = "qoder"
	if _, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{Prompt: "task"}); err == nil {
		t.Fatal("expected missing native session id error")
	}
}

func TestFreshNativeSessionIDsAreCallerAssignedUUIDs(t *testing.T) {
	p := New()
	if got := p.ContinuationCapabilities().FreshNativeSessionID; got != ports.FreshNativeSessionIDCallerAssigned {
		t.Fatalf("fresh native session ID mode = %q, want caller assigned", got)
	}
	first := p.NewNativeSessionID()
	second := p.NewNativeSessionID()
	if first == "" || second == "" || first == second {
		t.Fatalf("NewNativeSessionID returned %q and %q, want distinct non-empty UUIDs", first, second)
	}
}

func TestRestoreCommandUsesExactID(t *testing.T) {
	p := New()
	p.resolvedBinary = "qoder"
	got, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		SystemPrompt: "AO role", Session: ports.SessionRef{Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "native-123"}},
	})
	if err != nil || !ok {
		t.Fatalf("restore = %#v, %v, %v", got, ok, err)
	}
	want := []string{"qoder", "--append-system-prompt", "AO role", "--resume", "native-123"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command = %#v, want %#v", got, want)
	}
	for _, arg := range got {
		if arg == "--continue" {
			t.Fatal("restore must not use --continue")
		}
	}
}

func TestPromptDeliveryAndActivityCapabilities(t *testing.T) {
	p := New()
	strategy, err := p.GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{})
	if err != nil || strategy != ports.PromptDeliveryInCommand {
		t.Fatalf("strategy = %q, %v", strategy, err)
	}
	if !p.EmitsSubmitActivity() || !p.EmitsBlockedActivity() || !p.FirstSignalProvesInputReady() {
		t.Fatal("missing hook capability")
	}
}
