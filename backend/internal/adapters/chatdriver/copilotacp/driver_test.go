package copilotacp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakePlugin struct {
	binary string
	status ports.AgentAuthStatus
}

func (p fakePlugin) ResolveBinary(context.Context) (string, error) { return p.binary, nil }
func (p fakePlugin) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	return p.status, nil
}

func TestDriverReusesCopilotPluginAndEnforcesApprovals(t *testing.T) {
	driver := New(fakePlugin{binary: "/user/bin/copilot", status: ports.AgentAuthStatusAuthorized}, nil)
	if driver.Harness() != domain.HarnessCopilot {
		t.Fatalf("harness = %q, want %q", driver.Harness(), domain.HarnessCopilot)
	}
	caps, err := driver.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	// Copilot's ACP server raises session/request_permission itself, so unlike Pi
	// the approvals capability stays true and Chat is not bypass-only.
	for _, capability := range []ports.ChatCapability{
		ports.ChatCapabilityStreaming,
		ports.ChatCapabilityTools,
		ports.ChatCapabilityApprovals,
		ports.ChatCapabilityInterrupt,
		ports.ChatCapabilityResume,
		ports.ChatCapabilityUsage,
	} {
		if !caps.Has(capability) {
			t.Errorf("capability %q is false", capability)
		}
	}
	// Copilot emits no ACP diff content blocks and no plan updates.
	for _, capability := range []ports.ChatCapability{
		ports.ChatCapabilityDiffs,
		ports.ChatCapabilityPlans,
	} {
		if caps.Has(capability) {
			t.Errorf("capability %q is true, want false", capability)
		}
	}
}

func TestConfigureUsesACPFlagAndSharedApprovalMapping(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		permissions ports.PermissionMode
		want        []string
	}{
		{name: "defaults", want: []string{"--acp"}},
		{
			name:  "model",
			model: "claude-opus-4.7",
			want:  []string{"--acp", "--model", "claude-opus-4.7"},
		},
		{
			name:        "accept edits allows only the write tool",
			permissions: ports.PermissionModeAcceptEdits,
			want:        []string{"--acp", "--allow-tool", "write"},
		},
		{
			name:        "auto allows every tool",
			permissions: ports.PermissionModeAuto,
			want:        []string{"--acp", "--allow-all-tools"},
		},
		{
			name:        "bypass allows tools paths and urls",
			permissions: ports.PermissionModeBypassPermissions,
			want:        []string{"--acp", "--allow-all"},
		},
		{
			name:        "model and approvals compose",
			model:       "gpt-5.6-sol",
			permissions: ports.PermissionModeAuto,
			want:        []string{"--acp", "--model", "gpt-5.6-sol", "--allow-all-tools"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, env, err := configure(context.Background(), acpdriver.LaunchConfig{
				WorkspacePath: t.TempDir(), Model: tt.model, Permissions: tt.permissions,
			})
			if err != nil {
				t.Fatalf("configure: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) || env != nil {
				t.Fatalf("args/env = %#v, %#v; want %#v, nil", got, env, tt.want)
			}
		})
	}
}

// The system prompt cannot ride on --agent: Copilot parses the flag under --acp
// and then ignores it. configure installs the profile the ACP session selects by
// id instead, so no launch argument mentions the agent at all.
func TestConfigureInstallsAgentProfileWithoutAnAgentFlag(t *testing.T) {
	workspace := t.TempDir()
	args, _, err := configure(context.Background(), acpdriver.LaunchConfig{
		SessionID: domain.SessionID("ao_sess_01"), WorkspacePath: workspace,
		SystemPrompt: "AO worker instructions",
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	for _, arg := range args {
		if strings.Contains(arg, "--agent") {
			t.Fatalf("args carry an ignored --agent flag: %#v", args)
		}
	}
	profile, err := os.ReadFile(filepath.Join(workspace, ".github", "agents", "ao-ao-sess-01.agent.md"))
	if err != nil {
		t.Fatalf("read Copilot agent profile: %v", err)
	}
	for _, want := range []string{"name: ao-ao-sess-01", "AO worker instructions"} {
		if !strings.Contains(string(profile), want) {
			t.Errorf("agent profile missing %q:\n%s", want, profile)
		}
	}
}

func TestLaunchSessionOptionsSelectTheInstalledProfile(t *testing.T) {
	cfg := acpdriver.LaunchConfig{
		SessionID: domain.SessionID("ao_sess_01"), SystemPrompt: "AO worker instructions",
	}
	got := launchSessionOptions(cfg)
	want := []acpdriver.SessionOption{{ID: "agent", Value: "ao-ao-sess-01"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("launchSessionOptions = %#v, want %#v", got, want)
	}
}

// Without standing instructions there is no profile on disk, so selecting one
// would fail the session with an invalid config value.
func TestLaunchSessionOptionsAreEmptyWithoutASystemPrompt(t *testing.T) {
	for _, cfg := range []acpdriver.LaunchConfig{
		{SessionID: domain.SessionID("ao_sess_01")},
		{SystemPrompt: "AO worker instructions"},
	} {
		if got := launchSessionOptions(cfg); got != nil {
			t.Fatalf("launchSessionOptions(%#v) = %#v, want nil", cfg, got)
		}
	}
}

func TestSessionOptionsUseAdvertisedConfigOptionIDs(t *testing.T) {
	tests := []struct {
		name     string
		settings ports.ChatTurnSettings
		want     []acpdriver.SessionOption
	}{
		{name: "empty"},
		{
			name:     "model",
			settings: ports.ChatTurnSettings{Model: "claude-sonnet-5"},
			want:     []acpdriver.SessionOption{{ID: "model", Value: "claude-sonnet-5"}},
		},
		{
			name:     "effort uses the reasoning_effort option id",
			settings: ports.ChatTurnSettings{Effort: "xhigh"},
			want:     []acpdriver.SessionOption{{ID: "reasoning_effort", Value: "xhigh"}},
		},
		{
			name:     "bypass turns allow_all on",
			settings: ports.ChatTurnSettings{Approval: ports.PermissionModeBypassPermissions},
			want:     []acpdriver.SessionOption{{ID: "allow_all", Value: "on"}},
		},
		{
			// allow_all off restores the launch baseline rather than dropping to
			// default, so accept-edits keeps auto-approving edit tool calls.
			name:     "accept edits turns allow_all off",
			settings: ports.ChatTurnSettings{Approval: ports.PermissionModeAcceptEdits},
			want:     []acpdriver.SessionOption{{ID: "allow_all", Value: "off"}},
		},
		{
			name:     "auto turns allow_all off",
			settings: ports.ChatTurnSettings{Approval: ports.PermissionModeAuto},
			want:     []acpdriver.SessionOption{{ID: "allow_all", Value: "off"}},
		},
		{
			name:     "default turns allow_all off",
			settings: ports.ChatTurnSettings{Approval: ports.PermissionModeDefault},
			want:     []acpdriver.SessionOption{{ID: "allow_all", Value: "off"}},
		},
		{
			name: "model effort and approval",
			settings: ports.ChatTurnSettings{
				Model: "gpt-5.6-sol", Effort: "high",
				Approval: ports.PermissionModeBypassPermissions,
			},
			want: []acpdriver.SessionOption{
				{ID: "model", Value: "gpt-5.6-sol"},
				{ID: "reasoning_effort", Value: "high"},
				{ID: "allow_all", Value: "on"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sessionOptions(tt.settings); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("sessionOptions = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// The turn settings bar offers all four modes for every non-Codex harness, so
// the driver has to answer for each of them. allow_all is the only runtime
// permission control Copilot has: a session reaches full bypass and the baseline
// its launch flags set, and nothing else.
func TestValidateTurnSettingsAdmitsWhatAllowAllCanReach(t *testing.T) {
	tests := []struct {
		name     string
		initial  ports.PermissionMode
		settings ports.ChatTurnSettings
		wantErr  bool
	}{
		{name: "unset approval keeps the launch mode", initial: ports.PermissionModeAuto},
		{
			name:     "unchanged mode",
			initial:  ports.PermissionModeAcceptEdits,
			settings: ports.ChatTurnSettings{Approval: ports.PermissionModeAcceptEdits},
		},
		{
			name:     "normalized empty mode matches default",
			settings: ports.ChatTurnSettings{Approval: ports.PermissionModeDefault},
		},
		{
			name:     "any launch mode can switch to bypass",
			initial:  ports.PermissionModeAcceptEdits,
			settings: ports.ChatTurnSettings{Approval: ports.PermissionModeBypassPermissions},
		},
		{
			name:     "auto can switch to bypass",
			initial:  ports.PermissionModeAuto,
			settings: ports.ChatTurnSettings{Approval: ports.PermissionModeBypassPermissions},
		},
		{
			name:     "accept edits can come back from bypass",
			initial:  ports.PermissionModeAcceptEdits,
			settings: ports.ChatTurnSettings{Approval: ports.PermissionModeAcceptEdits},
		},
		{
			// --allow-all carries no other allow flag, so turning allow_all off
			// leaves the session in default.
			name:     "a bypass launch falls back to default",
			initial:  ports.PermissionModeBypassPermissions,
			settings: ports.ChatTurnSettings{Approval: ports.PermissionModeDefault},
		},
		{
			name:     "a default launch cannot become auto",
			settings: ports.ChatTurnSettings{Approval: ports.PermissionModeAuto},
			wantErr:  true,
		},
		{
			name:     "a default launch cannot become accept edits",
			settings: ports.ChatTurnSettings{Approval: ports.PermissionModeAcceptEdits},
			wantErr:  true,
		},
		{
			name:     "accept edits and auto are different launch flags",
			initial:  ports.PermissionModeAcceptEdits,
			settings: ports.ChatTurnSettings{Approval: ports.PermissionModeAuto},
			wantErr:  true,
		},
		{
			name:     "a bypass launch cannot become accept edits",
			initial:  ports.PermissionModeBypassPermissions,
			settings: ports.ChatTurnSettings{Approval: ports.PermissionModeAcceptEdits},
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTurnSettings(tt.initial, tt.settings)
			if tt.wantErr {
				if !errors.Is(err, acpdriver.ErrACPSetterUnsupported) {
					t.Fatalf("error = %v, want ErrACPSetterUnsupported", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// Every mode the driver admits must also produce an allow_all value, so an
// accepted turn actually changes the session instead of passing validation and
// then doing nothing.
func TestAdmittedModesAllProduceAnAllowAllValue(t *testing.T) {
	for _, initial := range []ports.PermissionMode{
		ports.PermissionModeDefault,
		ports.PermissionModeAcceptEdits,
		ports.PermissionModeAuto,
		ports.PermissionModeBypassPermissions,
	} {
		for _, requested := range []ports.PermissionMode{
			ports.PermissionModeDefault,
			ports.PermissionModeAcceptEdits,
			ports.PermissionModeAuto,
			ports.PermissionModeBypassPermissions,
		} {
			settings := ports.ChatTurnSettings{Approval: requested}
			if validateTurnSettings(initial, settings) != nil {
				continue
			}
			want := "off"
			if requested == ports.PermissionModeBypassPermissions {
				want = "on"
			}
			got := sessionOptions(settings)
			if len(got) != 1 || got[0].ID != "allow_all" || got[0].Value != want {
				t.Errorf("launch %s -> %s: sessionOptions = %#v, want allow_all=%s",
					initial, requested, got, want)
			}
		}
	}
}
