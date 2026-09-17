package junieacp

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/junie"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type builder struct{}

func (builder) Prepare(context.Context, junie.RuntimeFileRequest) (junie.RuntimeFiles, error) {
	return junie.RuntimeFiles{ConfigPath: "config", GuidelinesPath: "guidelines"}, nil
}

func TestConfigureUsesIsolatedRuntimeFiles(t *testing.T) {
	args, env, err := configure(builder{})(context.Background(), acpdriver.LaunchConfig{DataDir: "data", SessionID: "session", SystemPrompt: "guidelines"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--acp", "true", "--skip-update-check", "--config-location", "config", "--guidelines-filename", "guidelines"}
	if !reflect.DeepEqual(args, want) || env != nil {
		t.Fatalf("args=%q env=%v", args, env)
	}
}

func TestValidateTurnSettingsAllowsSharedStartDefaultApproval(t *testing.T) {
	err := validateTurnSettings(ports.PermissionModeDefault, ports.ChatTurnSettings{
		Approval: ports.PermissionModeDefault,
	})
	if err != nil {
		t.Fatalf("shared ACP Start default approval: %v", err)
	}
}

func TestValidateTurnSettingsFailsClosedForUnprovenSettings(t *testing.T) {
	tests := []struct {
		name    string
		setting ports.ChatTurnSettings
		want    error
	}{
		{name: "model", setting: ports.ChatTurnSettings{Model: "unproven"}, want: acpdriver.ErrACPSetterUnsupported},
		{name: "effort", setting: ports.ChatTurnSettings{Effort: "high"}, want: acpdriver.ErrACPSetterUnsupported},
		{name: "accept edits", setting: ports.ChatTurnSettings{Approval: ports.PermissionModeAcceptEdits}, want: ports.ErrChatPermissionModeUnsupported},
		{name: "auto", setting: ports.ChatTurnSettings{Approval: ports.PermissionModeAuto}, want: ports.ErrChatPermissionModeUnsupported},
		{name: "bypass", setting: ports.ChatTurnSettings{Approval: ports.PermissionModeBypassPermissions}, want: ports.ErrChatPermissionModeUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateTurnSettings(ports.PermissionModeDefault, test.setting)
			if !errors.Is(err, test.want) {
				t.Fatalf("validateTurnSettings() error = %v, want %v", err, test.want)
			}
		})
	}
}
