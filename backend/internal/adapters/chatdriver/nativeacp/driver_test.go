package nativeacp

import (
	"context"
	"errors"
	"testing"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakePlugin struct {
	binary  string
	binErr  error
	status  ports.AgentAuthStatus
	authErr error
}

type fakeAugmentingPlugin struct {
	fakePlugin
	dataDir string
}

func (p *fakeAugmentingPlugin) AugmentRuntimeEnv(env map[string]string, dataDir string) {
	p.dataDir = dataDir
	env["PROVIDER_DATA_DIR"] = dataDir + "/provider"
}

func (p fakePlugin) ResolveBinary(context.Context) (string, error) { return p.binary, p.binErr }
func (p fakePlugin) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	return p.status, p.authErr
}

func TestBindingLaunchesExactUserInstalledBinary(t *testing.T) {
	const userBinary = "/Users/test/.local/bin/provider"
	var configured acpdriver.LaunchConfig
	type contextKey struct{}
	launchCtx := context.WithValue(context.Background(), contextKey{}, "launch-context")
	cfg := buildConfig(fakePlugin{binary: userBinary, status: ports.AgentAuthStatusAuthorized}, Config{
		Harness: domain.HarnessOpenCode,
		Configure: func(ctx context.Context, in acpdriver.LaunchConfig) ([]string, map[string]string, error) {
			if ctx.Value(contextKey{}) != "launch-context" {
				t.Fatal("Configure did not receive the launch context")
			}
			configured = in
			return []string{"acp"}, map[string]string{"PROVIDER_CONFIG": "/ao/config.json"}, nil
		},
	}, nil)

	if err := cfg.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	launch, err := cfg.Launch(launchCtx, acpdriver.LaunchConfig{
		SessionID: "session-1", DataDir: "/ao", WorkspacePath: "/worktree",
		Env: map[string]string{"PATH": "/user/bin", "KEEP": "yes"}, Model: "provider/model",
		Permissions: ports.PermissionModeAcceptEdits, SystemPrompt: "AO rules",
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if launch.Command != userBinary {
		t.Fatalf("command = %q, want exact plugin-resolved binary %q", launch.Command, userBinary)
	}
	if len(launch.Args) != 1 || launch.Args[0] != "acp" {
		t.Fatalf("args = %#v", launch.Args)
	}
	if launch.Env["PATH"] != "/user/bin" || launch.Env["KEEP"] != "yes" ||
		launch.Env["PROVIDER_CONFIG"] != "/ao/config.json" {
		t.Fatalf("merged env = %#v", launch.Env)
	}
	if configured.DataDir != "/ao" || configured.WorkspacePath != "/worktree" ||
		configured.Model != "provider/model" || configured.Permissions != ports.PermissionModeAcceptEdits ||
		configured.SystemPrompt != "AO rules" || configured.SessionID != "session-1" {
		t.Fatalf("configure input = %#v", configured)
	}
	for _, capability := range []ports.ChatCapability{
		ports.ChatCapabilityStreaming,
		ports.ChatCapabilityTools,
		ports.ChatCapabilityApprovals,
		ports.ChatCapabilityInterrupt,
		ports.ChatCapabilityResume,
	} {
		if !cfg.Capabilities[capability] {
			t.Errorf("base capability %q is false", capability)
		}
	}
}

func TestBindingMapsPluginDiscoveryAndAuth(t *testing.T) {
	t.Run("incomplete provider binding", func(t *testing.T) {
		cfg := buildConfig(fakePlugin{binary: "/user/provider"}, Config{Harness: domain.HarnessDroid}, nil)
		if err := cfg.Probe(context.Background()); !errors.Is(err, ports.ErrChatDriverUnavailable) {
			t.Fatalf("Probe error = %v", err)
		}
		if _, err := cfg.Launch(context.Background(), acpdriver.LaunchConfig{}); !errors.Is(err, ports.ErrChatDriverUnavailable) {
			t.Fatalf("Launch error = %v", err)
		}
	})

	t.Run("missing user binary", func(t *testing.T) {
		cfg := buildConfig(fakePlugin{binErr: ports.ErrAgentBinaryNotFound}, Config{
			Harness: domain.HarnessDroid, Configure: func(context.Context, acpdriver.LaunchConfig) ([]string, map[string]string, error) {
				return []string{"exec"}, nil, nil
			},
		}, nil)
		if err := cfg.Probe(context.Background()); !errors.Is(err, ports.ErrChatDriverUnavailable) ||
			!errors.Is(err, ports.ErrAgentBinaryNotFound) {
			t.Fatalf("Probe error = %v", err)
		}
	})

	t.Run("installed but logged out", func(t *testing.T) {
		cfg := buildConfig(fakePlugin{binary: "/user/droid", status: ports.AgentAuthStatusUnauthorized}, Config{
			Harness: domain.HarnessDroid, Configure: func(context.Context, acpdriver.LaunchConfig) ([]string, map[string]string, error) {
				return []string{"exec"}, nil, nil
			},
		}, nil)
		if err := cfg.Probe(context.Background()); !errors.Is(err, ports.ErrChatAuthRequired) {
			t.Fatalf("Probe error = %v, want ErrChatAuthRequired", err)
		}
	})

	t.Run("inconclusive auth is not logged out", func(t *testing.T) {
		cfg := buildConfig(fakePlugin{binary: "/user/opencode", authErr: errors.New("probe timed out")}, Config{
			Harness: domain.HarnessOpenCode, Configure: func(context.Context, acpdriver.LaunchConfig) ([]string, map[string]string, error) {
				return []string{"acp"}, nil, nil
			},
		}, nil)
		if err := cfg.Probe(context.Background()); err != nil {
			t.Fatalf("Probe: %v", err)
		}
	})
}

func TestBindingReusesPluginRuntimeEnvironment(t *testing.T) {
	plugin := &fakeAugmentingPlugin{fakePlugin: fakePlugin{
		binary: "/user/provider", status: ports.AgentAuthStatusAuthorized,
	}}
	cfg := buildConfig(plugin, Config{
		Harness: domain.HarnessCursor,
		Configure: func(context.Context, acpdriver.LaunchConfig) ([]string, map[string]string, error) {
			return []string{"acp"}, nil, nil
		},
	}, nil)

	launch, err := cfg.Launch(context.Background(), acpdriver.LaunchConfig{
		DataDir: "/ao", Env: map[string]string{"KEEP": "yes"},
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if plugin.dataDir != "/ao" || launch.Env["PROVIDER_DATA_DIR"] != "/ao/provider" ||
		launch.Env["KEEP"] != "yes" {
		t.Fatalf("plugin data dir/env = %q, %#v", plugin.dataDir, launch.Env)
	}
}
