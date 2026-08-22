package ompacp

import (
	"context"
	"errors"
	"reflect"
	"testing"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestConfigureUsesNativeACP(t *testing.T) {
	tests := []struct {
		name string
		cfg  acpdriver.LaunchConfig
		want []string
	}{
		{name: "defaults", want: []string{"acp"}},
		{
			name: "model prompt and write approvals",
			cfg: acpdriver.LaunchConfig{
				Model: "anthropic/claude-sonnet", SystemPrompt: "Follow AO rules.",
				Permissions: ports.PermissionModeAcceptEdits,
			},
			want: []string{"acp", "--model", "anthropic/claude-sonnet", "--append-system-prompt", "Follow AO rules.", "--approval-mode", "write"},
		},
		{
			name: "auto uses write approvals",
			cfg:  acpdriver.LaunchConfig{Permissions: ports.PermissionModeAuto},
			want: []string{"acp", "--approval-mode", "write"},
		},
		{
			name: "bypass uses yolo",
			cfg:  acpdriver.LaunchConfig{Permissions: ports.PermissionModeBypassPermissions},
			want: []string{"acp", "--approval-mode", "yolo"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, env, err := configure(tt.cfg)
			if err != nil {
				t.Fatalf("configure: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) || env != nil {
				t.Fatalf("args/env = %#v, %#v; want %#v, nil", got, env, tt.want)
			}
		})
	}
}

func TestSessionOptionsUseAdvertisedModelOption(t *testing.T) {
	if got := sessionOptions(ports.ChatTurnSettings{}); got != nil {
		t.Fatalf("empty settings = %#v", got)
	}
	got := sessionOptions(ports.ChatTurnSettings{Model: "zai/glm-5.2"})
	want := []acpdriver.SessionOption{{ID: "model", Value: "zai/glm-5.2"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("settings = %#v, want %#v", got, want)
	}
}

func TestValidateTurnSettingsRejectsApprovalModeChange(t *testing.T) {
	tests := []struct {
		name      string
		initial   ports.PermissionMode
		requested ports.PermissionMode
		wantErr   bool
	}{
		{name: "empty keeps launch mode", initial: ports.PermissionModeAcceptEdits},
		{name: "same default", initial: ports.PermissionModeDefault, requested: ports.PermissionModeDefault},
		{name: "same write spelling", initial: ports.PermissionModeAcceptEdits, requested: ports.PermissionModeAuto},
		{name: "same yolo", initial: ports.PermissionModeBypassPermissions, requested: ports.PermissionModeBypassPermissions},
		{name: "write to yolo", initial: ports.PermissionModeAcceptEdits, requested: ports.PermissionModeBypassPermissions, wantErr: true},
		{name: "write back to default", initial: ports.PermissionModeAuto, requested: ports.PermissionModeDefault, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTurnSettings(tt.initial, ports.ChatTurnSettings{Approval: tt.requested})
			if tt.wantErr && !errors.Is(err, acpdriver.ErrACPSetterUnsupported) {
				t.Fatalf("error = %v, want ErrACPSetterUnsupported", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("error = %v, want nil", err)
			}
		})
	}
}

func TestDriverReusesOMPPluginForProbe(t *testing.T) {
	plugin := &fakePlugin{status: ports.AgentAuthStatusAuthorized, binary: "/usr/bin/omp"}
	driver := New(plugin, nil)

	caps, err := driver.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if driver.Harness() != domain.HarnessOMP {
		t.Fatalf("harness = %q", driver.Harness())
	}
	for _, capability := range []ports.ChatCapability{
		ports.ChatCapabilityStreaming, ports.ChatCapabilityTools, ports.ChatCapabilityApprovals,
		ports.ChatCapabilityInterrupt, ports.ChatCapabilityResume, ports.ChatCapabilityUsage,
		ports.ChatCapabilityDiffs, ports.ChatCapabilityPlans,
	} {
		if !caps.Has(capability) {
			t.Errorf("missing capability %q", capability)
		}
	}
	if plugin.resolveCalls != 1 || plugin.authCalls != 1 {
		t.Fatalf("plugin calls = resolve %d, auth %d", plugin.resolveCalls, plugin.authCalls)
	}
}

func TestDriverRejectsUnauthenticatedOMP(t *testing.T) {
	driver := New(&fakePlugin{status: ports.AgentAuthStatusUnauthorized, binary: "/usr/bin/omp"}, nil)
	if _, err := driver.Probe(context.Background()); !errors.Is(err, ports.ErrChatAuthRequired) {
		t.Fatalf("Probe error = %v, want ErrChatAuthRequired", err)
	}
}

type fakePlugin struct {
	binary       string
	status       ports.AgentAuthStatus
	resolveCalls int
	authCalls    int
}

func (p *fakePlugin) ResolveBinary(context.Context) (string, error) {
	p.resolveCalls++
	return p.binary, nil
}

func (p *fakePlugin) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	p.authCalls++
	return p.status, nil
}
