package kimiacp

import (
	"context"
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

func TestDriverReusesKimiPluginAndDeclaresNativeFeatures(t *testing.T) {
	driver := New(fakePlugin{binary: "/user/bin/kimi", status: ports.AgentAuthStatusAuthorized}, nil)
	if driver.Harness() != domain.HarnessKimi {
		t.Fatalf("harness = %q, want %q", driver.Harness(), domain.HarnessKimi)
	}
	caps, err := driver.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	for _, capability := range []ports.ChatCapability{
		ports.ChatCapabilityStreaming,
		ports.ChatCapabilityTools,
		ports.ChatCapabilityApprovals,
		ports.ChatCapabilityInterrupt,
		ports.ChatCapabilityResume,
		ports.ChatCapabilityHistory,
		ports.ChatCapabilityPlans,
	} {
		if !caps.Has(capability) {
			t.Errorf("capability %q is false", capability)
		}
	}
}

func TestConfigureLaunchesNativeACPSubcommand(t *testing.T) {
	workspace := t.TempDir()
	args, env, err := configure(acpdriver.LaunchConfig{
		WorkspacePath: workspace,
		Model:         "kimi-code/kimi-for-coding", Permissions: ports.PermissionModeBypassPermissions,
		SystemPrompt: "AO worker instructions",
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if want := []string{"acp"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	if env != nil {
		t.Fatalf("env = %#v, want nil", env)
	}
	instructions, err := os.ReadFile(filepath.Join(workspace, ".kimi-code", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read Kimi ACP instructions: %v", err)
	}
	for _, want := range []string{
		"<!-- managed by agent-orchestrator: kimi system prompt -->",
		"AO worker instructions",
		"<!-- /managed by agent-orchestrator: kimi system prompt -->",
	} {
		if !strings.Contains(string(instructions), want) {
			t.Errorf("Kimi ACP instructions missing %q:\n%s", want, instructions)
		}
	}
	gitignore, err := os.ReadFile(filepath.Join(workspace, ".kimi-code", ".gitignore"))
	if err != nil {
		t.Fatalf("read Kimi ACP gitignore: %v", err)
	}
	if !strings.Contains(string(gitignore), "/AGENTS.md\n") {
		t.Fatalf("Kimi ACP instructions are not gitignored:\n%s", gitignore)
	}
}

func TestSessionOptionsMapModelsAndPermissions(t *testing.T) {
	tests := []struct {
		name     string
		settings ports.ChatTurnSettings
		want     []acpdriver.SessionOption
	}{
		{name: "empty"},
		{
			name:     "model",
			settings: ports.ChatTurnSettings{Model: "kimi-code/kimi-for-coding"},
			want:     []acpdriver.SessionOption{{ID: "model", Value: "kimi-code/kimi-for-coding"}},
		},
		{
			name:     "accept edits",
			settings: ports.ChatTurnSettings{Approval: ports.PermissionModeAcceptEdits},
			want:     []acpdriver.SessionOption{{ID: "mode", Value: "auto"}},
		},
		{
			name:     "auto",
			settings: ports.ChatTurnSettings{Approval: ports.PermissionModeAuto},
			want:     []acpdriver.SessionOption{{ID: "mode", Value: "auto"}},
		},
		{
			name:     "bypass",
			settings: ports.ChatTurnSettings{Approval: ports.PermissionModeBypassPermissions},
			want:     []acpdriver.SessionOption{{ID: "mode", Value: "yolo"}},
		},
		{
			name: "model and permission",
			settings: ports.ChatTurnSettings{
				Model: "kimi-code/kimi-for-coding", Approval: ports.PermissionModeBypassPermissions,
			},
			want: []acpdriver.SessionOption{
				{ID: "model", Value: "kimi-code/kimi-for-coding"},
				{ID: "mode", Value: "yolo"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionOptions(tc.settings); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("sessionOptions(%#v) = %#v, want %#v", tc.settings, got, tc.want)
			}
		})
	}
}

func TestKimiModeCatalogKeepsPlanProviderOwned(t *testing.T) {
	// Plan is a distinct Kimi ACP mode exposed through its live config-option
	// catalog. It must not be inferred from AO's permission setting or collapsed
	// into auto/yolo here.
	if got := permissionMode(ports.PermissionModeDefault); got != "" {
		t.Fatalf("default permission mapped to %q, want provider mode unchanged", got)
	}
}
