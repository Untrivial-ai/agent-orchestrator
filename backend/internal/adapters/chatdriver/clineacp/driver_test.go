package clineacp

import (
	"context"
	"reflect"
	"testing"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestConfigureStartsACPMode(t *testing.T) {
	args, env, err := configure(context.Background(), acpdriver.LaunchConfig{})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	want := []string{"--acp"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	if env != nil {
		t.Fatalf("env = %#v, want nil", env)
	}
}

func TestConfigureEnablesAutoApproveForAutoAndBypass(t *testing.T) {
	for _, mode := range []ports.PermissionMode{
		ports.PermissionModeAuto,
		ports.PermissionModeBypassPermissions,
	} {
		args, _, err := configure(context.Background(), acpdriver.LaunchConfig{Permissions: mode})
		if err != nil {
			t.Fatalf("configure(%q): %v", mode, err)
		}
		want := []string{"--acp", "--auto-approve", "true"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("mode %q: args = %#v, want %#v", mode, args, want)
		}
	}
}

func TestConfigureKeepsPromptingForDefaultAndAcceptEdits(t *testing.T) {
	for _, mode := range []ports.PermissionMode{
		ports.PermissionModeDefault,
		ports.PermissionModeAcceptEdits,
	} {
		args, _, err := configure(context.Background(), acpdriver.LaunchConfig{Permissions: mode})
		if err != nil {
			t.Fatalf("configure(%q): %v", mode, err)
		}
		want := []string{"--acp"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("mode %q: args = %#v, want %#v", mode, args, want)
		}
	}
}

func TestConfigureDeliversModelThroughLaunchEnv(t *testing.T) {
	args, env, err := configure(context.Background(), acpdriver.LaunchConfig{Model: "anthropic/claude-sonnet-4"})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if want := []string{"--acp"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	wantEnv := map[string]string{"CLINE_MODEL": "anthropic/claude-sonnet-4"}
	if !reflect.DeepEqual(env, wantEnv) {
		t.Fatalf("env = %#v, want %#v", env, wantEnv)
	}
}
