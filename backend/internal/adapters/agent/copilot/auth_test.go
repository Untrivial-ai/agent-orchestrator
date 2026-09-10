package copilot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authprobe"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestCopilotLocalAuthStatusAuthorizedWithBYOKProvider(t *testing.T) {
	clearCopilotAuthProbeEnv(t)
	t.Setenv("COPILOT_PROVIDER_BASE_URL", "http://localhost:11434")
	t.Setenv("COPILOT_MODEL", "llama3.2")

	status, ok, err := copilotLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestCopilotLocalAuthStatusUsesCopilotHome(t *testing.T) {
	clearCopilotAuthProbeEnv(t)
	dir := t.TempDir()
	t.Setenv("COPILOT_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"authToken":"oauth-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := copilotLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestCopilotLocalAuthStatusFallsBackToGHTokenWhenConfigIsMalformed(t *testing.T) {
	clearCopilotAuthProbeEnv(t)
	dir := t.TempDir()
	t.Setenv("COPILOT_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{malformed`), 0o600); err != nil {
		t.Fatal(err)
	}

	previous := authprobe.CmdRunner
	t.Cleanup(func() { authprobe.CmdRunner = previous })
	authprobe.CmdRunner = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "gh" || !reflect.DeepEqual(args, []string{"auth", "token"}) {
			return nil, errors.New("unexpected auth probe command")
		}
		return []byte("gho_test-token"), nil
	}

	status, ok, err := copilotLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func clearCopilotAuthProbeEnv(t *testing.T) {
	t.Helper()
	clearCopilotAuthEnv(t)
	for _, name := range []string{"COPILOT_PROVIDER_BASE_URL", "COPILOT_PROVIDER_TYPE", "COPILOT_PROVIDER_API_KEY", "COPILOT_MODEL", "COPILOT_HOME"} {
		t.Setenv(name, "")
	}
}
