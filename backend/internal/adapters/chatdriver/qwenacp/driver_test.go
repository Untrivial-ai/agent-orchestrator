package qwenacp

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
		{name: "defaults", want: []string{"--acp"}},
		{
			name: "model prompt and auto-edit approvals",
			cfg: acpdriver.LaunchConfig{
				Model: "qwen3-coder-plus", SystemPrompt: "Follow AO rules.",
				Permissions: ports.PermissionModeAcceptEdits,
			},
			want: []string{"--acp", "--approval-mode", "auto-edit", "--model", "qwen3-coder-plus", "--append-system-prompt", "Follow AO rules."},
		},
		{
			name: "auto uses auto approvals",
			cfg:  acpdriver.LaunchConfig{Permissions: ports.PermissionModeAuto},
			want: []string{"--acp", "--approval-mode", "auto"},
		},
		{
			name: "bypass uses yolo",
			cfg:  acpdriver.LaunchConfig{Permissions: ports.PermissionModeBypassPermissions},
			want: []string{"--acp", "--approval-mode", "yolo"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, env, err := configure(context.Background(), tt.cfg)
			if err != nil {
				t.Fatalf("configure: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) || env != nil {
				t.Fatalf("args/env = %#v, %#v; want %#v, nil", got, env, tt.want)
			}
		})
	}
}

func TestSessionModeMapsQwenVocabulary(t *testing.T) {
	tests := map[ports.PermissionMode]string{
		ports.PermissionModeDefault:           "",
		ports.PermissionModeAcceptEdits:       "auto-edit",
		ports.PermissionModeAuto:              "auto",
		ports.PermissionModeBypassPermissions: "yolo",
		"":                                    "",
	}
	for permission, want := range tests {
		if got := sessionMode(permission); got != want {
			t.Errorf("sessionMode(%q) = %q, want %q", permission, got, want)
		}
	}
}

func TestSessionOptionsUseAdvertisedModelOption(t *testing.T) {
	if got := sessionOptions(ports.ChatTurnSettings{}); got != nil {
		t.Fatalf("empty settings = %#v", got)
	}
	got := sessionOptions(ports.ChatTurnSettings{Model: "qwen3-coder-plus"})
	want := []acpdriver.SessionOption{{ID: "model", Value: "qwen3-coder-plus"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("settings = %#v, want %#v", got, want)
	}
}

func TestDriverReusesQwenPluginForProbe(t *testing.T) {
	plugin := &fakePlugin{status: ports.AgentAuthStatusAuthorized, binary: "/usr/bin/qwen"}
	versionCalls := 0
	driver := newDriver(plugin, func(_ context.Context, bin string) error {
		versionCalls++
		if bin != "/usr/bin/qwen" {
			t.Fatalf("version probe binary = %q, want /usr/bin/qwen", bin)
		}
		return nil
	}, nil)

	caps, err := driver.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if driver.Harness() != domain.HarnessQwen {
		t.Fatalf("harness = %q", driver.Harness())
	}
	for _, capability := range []ports.ChatCapability{
		ports.ChatCapabilityStreaming, ports.ChatCapabilityTools,
		ports.ChatCapabilityInterrupt, ports.ChatCapabilityResume,
	} {
		if !caps.Has(capability) {
			t.Errorf("missing capability %q", capability)
		}
	}
	if caps.Has(ports.ChatCapabilityApprovals) {
		t.Error("qwen must not advertise approvals: Qwen ACP ignores --approval-mode and never asks")
	}
	if plugin.resolveCalls != 1 || plugin.authCalls != 1 || versionCalls != 1 {
		t.Fatalf("plugin calls = resolve %d, auth %d, version %d; want one each",
			plugin.resolveCalls, plugin.authCalls, versionCalls)
	}
}

func TestDriverRejectsUnauthenticatedQwen(t *testing.T) {
	driver := newDriver(
		&fakePlugin{status: ports.AgentAuthStatusUnauthorized, binary: "/usr/bin/qwen"},
		func(context.Context, string) error { return nil },
		nil,
	)
	if _, err := driver.Probe(context.Background()); !errors.Is(err, ports.ErrChatAuthRequired) {
		t.Fatalf("Probe error = %v, want ErrChatAuthRequired", err)
	}
}

func TestDriverAdmitsChatOnlyUnderBypass(t *testing.T) {
	driver := newDriver(
		&fakePlugin{status: ports.AgentAuthStatusAuthorized, binary: "/usr/bin/qwen"},
		func(context.Context, string) error { return nil },
		nil,
	)
	caps, err := driver.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	// Qwen ACP ignores --approval-mode, so the only production-floor gap is the
	// approval channel it never opens.
	if missing := ports.MissingProductionCapabilities(caps); !reflect.DeepEqual(missing, []ports.ChatCapability{ports.ChatCapabilityApprovals}) {
		t.Fatalf("production floor gap = %v, want [approvals]", missing)
	}
	// A default (ask-me) Chat is refused because that missing channel is required;
	// bypass-permissions opts out of approvals, so admission is clean.
	if missing := ports.MissingCapabilitiesForPermissions(caps, ports.PermissionModeDefault); len(missing) == 0 {
		t.Fatal("default-mode Qwen Chat must be refused for the missing approval channel")
	}
	if missing := ports.MissingCapabilitiesForPermissions(caps, ports.PermissionModeBypassPermissions); len(missing) != 0 {
		t.Fatalf("bypass Qwen Chat should admit cleanly; missing=%v", missing)
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
