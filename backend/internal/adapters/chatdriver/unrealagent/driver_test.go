//go:build darwin || linux

package unrealagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type authPlugin struct {
	status    ports.AgentAuthStatus
	envStatus ports.AgentAuthStatus
}

func (plugin authPlugin) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	return plugin.status, nil
}

func (plugin authPlugin) AuthStatusWithEnv(context.Context, map[string]string) (ports.AgentAuthStatus, error) {
	return plugin.envStatus, nil
}

func TestDriverRequiresExplicitBypassPermissions(t *testing.T) {
	driver := New(authPlugin{status: ports.AgentAuthStatusAuthorized}, nil)
	driver.executable = func() (string, error) { return "/path/to/ao", nil }
	caps, err := driver.Probe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if missing := ports.MissingCapabilitiesForPermissions(caps, ports.PermissionModeBypassPermissions); len(missing) != 0 {
		t.Fatalf("bypass production capabilities missing %v", missing)
	}
	if err := validateStart(t.TempDir(), ports.PermissionModeBypassPermissions, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := validateStart(t.TempDir(), ports.PermissionModeDefault, false, nil, nil); err == nil {
		t.Fatal("default permissions unexpectedly accepted without an approval channel")
	}
}

func TestDriverDefersAuthenticationToLaunchEnvironment(t *testing.T) {
	driver := New(authPlugin{
		status: ports.AgentAuthStatusUnauthorized, envStatus: ports.AgentAuthStatusAuthorized,
	}, nil)
	driver.executable = func() (string, error) { return "/path/to/ao", nil }
	if _, err := driver.Probe(t.Context()); err != nil {
		t.Fatalf("Probe() error = %v, want launch environment to decide auth", err)
	}
	if err := driver.validateLaunchAuth(t.Context(), map[string]string{"OPENAI_API_KEY": "project-key"}); err != nil {
		t.Fatalf("validateLaunchAuth() error = %v", err)
	}
	missing := New(authPlugin{envStatus: ports.AgentAuthStatusUnauthorized}, nil)
	if err := missing.validateLaunchAuth(t.Context(), nil); !errors.Is(err, ports.ErrChatAuthRequired) {
		t.Fatalf("validateLaunchAuth() error = %v, want ErrChatAuthRequired", err)
	}
}

func TestReplayFailureTerminatesFreshHost(t *testing.T) {
	driver := New(authPlugin{
		status: ports.AgentAuthStatusAuthorized, envStatus: ports.AgentAuthStatusAuthorized,
	}, nil)
	providerInput, hostInput := io.Pipe()
	hostOutput, providerOutput := io.Pipe()
	t.Cleanup(func() {
		_ = providerInput.Close()
		_ = hostInput.Close()
		_ = hostOutput.Close()
		_ = providerOutput.Close()
	})
	driver.connectHost = func(context.Context, persistenthost.Config) (*persistenthost.Transport, error) {
		go func() {
			_ = json.NewEncoder(providerOutput).Encode(frame{Version: protocolVersion, Type: "ready"})
			var replay command
			if err := json.NewDecoder(providerInput).Decode(&replay); err == nil {
				_ = json.NewEncoder(providerOutput).Encode(frame{
					Version: protocolVersion, Type: "result", RequestID: replay.RequestID,
					Error: "forced replay failure",
				})
			}
		}()
		return &persistenthost.Transport{Stdin: hostInput, Stdout: hostOutput}, nil
	}
	terminated := make(chan string, 1)
	driver.shutdownHost = func(_ context.Context, _ string, sessionID string) error {
		terminated <- sessionID
		return nil
	}
	cfg := providerConfig{
		AOSessionID: "ao-session", ProviderConversationID: "provider-session",
		DataDir: t.TempDir(), WorkspacePath: t.TempDir(),
	}

	_, err := driver.connect(t.Context(), cfg, nil, nil, "scope")
	if err == nil || !strings.Contains(err.Error(), "forced replay failure") {
		t.Fatalf("connect error = %v, want replay failure", err)
	}
	select {
	case sessionID := <-terminated:
		if sessionID != cfg.AOSessionID {
			t.Fatalf("terminated session = %q, want %q", sessionID, cfg.AOSessionID)
		}
	default:
		t.Fatal("replay failure left the fresh host running")
	}
}
