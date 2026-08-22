package continueagent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestContinueLocalAuthStatusAuthorizedFromEnv(t *testing.T) {
	t.Setenv("CONTINUE_API_KEY", "continue-key")

	status, ok, err := continueLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestContinueLocalAuthStatusUnknownWithoutEnv(t *testing.T) {
	for _, name := range continueAPIKeyEnvVars {
		t.Setenv(name, "")
	}
	t.Setenv("HOME", t.TempDir())

	status, ok, err := continueLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func TestContinueConfigAuthStatusAuthorizedWithAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("models:\n  - provider: anthropic\n    apiKey: continue-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := continueConfigAuthStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestContinueConfigAuthStatusUnknownWithOnlyTokenLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	config := "models:\n  - provider: anthropic\n    defaultCompletionOptions:\n      maxTokens: 1500\n"
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := continueConfigAuthStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}
