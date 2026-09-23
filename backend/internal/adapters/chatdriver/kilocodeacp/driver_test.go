package kilocodeacp

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestConfigureStartsACPSubcommand(t *testing.T) {
	args, env, err := configure(context.Background(), acpdriver.LaunchConfig{})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if want := []string{"acp"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	if env != nil {
		t.Fatalf("env = %#v, want nil", env)
	}
}

func TestConfigureCarriesStandingInstructionsOnConfigContent(t *testing.T) {
	args, env, err := configure(context.Background(), acpdriver.LaunchConfig{
		SessionID: "worker-1", SystemPrompt: "Follow AO worker rules.",
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if want := []string{"acp"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	var config struct {
		DefaultAgent string `json:"default_agent"`
		Agent        map[string]struct {
			Prompt string `json:"prompt"`
		} `json:"agent"`
	}
	if err := json.Unmarshal([]byte(env["KILO_CONFIG_CONTENT"]), &config); err != nil {
		t.Fatalf("decode KILO_CONFIG_CONTENT %q: %v", env["KILO_CONFIG_CONTENT"], err)
	}
	if config.DefaultAgent != "ao-worker-1" {
		t.Fatalf("default_agent = %q, want ao-worker-1", config.DefaultAgent)
	}
	if got := config.Agent["ao-worker-1"].Prompt; got != "Follow AO worker rules." {
		t.Fatalf("agent prompt = %q", got)
	}
}

func TestConfigureMapsPermissionModesOnConfigContent(t *testing.T) {
	tests := map[ports.PermissionMode]map[string]string{
		ports.PermissionModeAcceptEdits:       {"edit": "allow"},
		ports.PermissionModeAuto:              {"edit": "allow", "bash": "allow"},
		ports.PermissionModeBypassPermissions: {"*": "allow"},
	}
	for mode, want := range tests {
		args, env, err := configure(context.Background(), acpdriver.LaunchConfig{Permissions: mode})
		if err != nil {
			t.Fatalf("configure(%q): %v", mode, err)
		}
		if wantArgs := []string{"acp"}; !reflect.DeepEqual(args, wantArgs) {
			t.Fatalf("mode %q: args = %#v, want %#v", mode, args, wantArgs)
		}
		var config struct {
			Permission map[string]string `json:"permission"`
		}
		if err := json.Unmarshal([]byte(env["KILO_CONFIG_CONTENT"]), &config); err != nil {
			t.Fatalf("mode %q: decode config: %v", mode, err)
		}
		if !reflect.DeepEqual(config.Permission, want) {
			t.Fatalf("mode %q: permission = %#v, want %#v", mode, config.Permission, want)
		}
	}
}

func TestSessionOptionsUseModelConfigOption(t *testing.T) {
	if got := sessionOptions(ports.ChatTurnSettings{}); len(got) != 0 {
		t.Fatalf("empty settings = %#v", got)
	}
	got := sessionOptions(ports.ChatTurnSettings{Model: "anthropic/claude-sonnet-4"})
	want := []acpdriver.SessionOption{{ID: "model", Value: "anthropic/claude-sonnet-4"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %#v, want %#v", got, want)
	}
}

// Kilo CLI 7.7.5 advertises an "effort" select alongside "model", so AO's
// durable effort choice must reach it instead of being dropped.
func TestSessionOptionsUseEffortConfigOption(t *testing.T) {
	got := sessionOptions(ports.ChatTurnSettings{Model: "m1", Effort: "thinking"})
	want := []acpdriver.SessionOption{
		{ID: "model", Value: "m1"},
		{ID: "effort", Value: "thinking"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %#v, want %#v", got, want)
	}
	if got := sessionOptions(ports.ChatTurnSettings{Effort: "thinking"}); !reflect.DeepEqual(
		got, []acpdriver.SessionOption{{ID: "effort", Value: "thinking"}}) {
		t.Fatalf("effort only = %#v", got)
	}
}
