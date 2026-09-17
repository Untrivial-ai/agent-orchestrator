package qoderacp

import (
	"context"
	"errors"
	"reflect"
	"testing"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestConfigureLaunchesNativeQoderACP(t *testing.T) {
	tests := []struct {
		name string
		cfg  acpdriver.LaunchConfig
		want []string
	}{
		{"default", acpdriver.LaunchConfig{}, []string{"--acp"}},
		{"standing instructions", acpdriver.LaunchConfig{SystemPrompt: "AO role"}, []string{"--acp", "--append-system-prompt", "AO role"}},
		{"bypass", acpdriver.LaunchConfig{Permissions: ports.PermissionModeBypassPermissions}, []string{"--acp", "--permission-mode", "bypass_permissions"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, env, err := configure(context.Background(), tt.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) || env != nil {
				t.Fatalf("args/env = %#v/%#v want %#v/nil", got, env, tt.want)
			}
		})
	}
}

func TestDriverUsesQoderIdentityAndVersionGate(t *testing.T) {
	p := &fakePlugin{binary: "/bin/qoder", auth: ports.AgentAuthStatusUnknown}
	called := false
	d := newDriver(p, func(_ context.Context, bin string) error {
		called = true
		if bin != p.binary {
			t.Fatalf("bin = %q", bin)
		}
		return nil
	}, nil)
	caps, err := d.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.Harness() != domain.HarnessQoder || !called || !caps.Has(ports.ChatCapabilityResume) || !caps.Has(ports.ChatCapabilityApprovals) {
		t.Fatalf("driver/caps = %q/%#v", d.Harness(), caps)
	}
}

func TestDriverRejectsIncompatibleVersion(t *testing.T) {
	d := newDriver(&fakePlugin{binary: "/bin/qoder"}, func(context.Context, string) error { return errors.New("old") }, nil)
	if _, err := d.Probe(context.Background()); !errors.Is(err, ports.ErrChatDriverIncompatible) {
		t.Fatalf("error = %v", err)
	}
}

type fakePlugin struct {
	binary string
	auth   ports.AgentAuthStatus
}

func (p *fakePlugin) ResolveBinary(context.Context) (string, error)             { return p.binary, nil }
func (p *fakePlugin) AuthStatus(context.Context) (ports.AgentAuthStatus, error) { return p.auth, nil }
