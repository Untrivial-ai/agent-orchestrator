package aider

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestAiderLocalAuthStatusAuthorizedWithProviderEnv(t *testing.T) {
	clearAiderAuthEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-test")

	status, ok, err := aiderLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestAiderLocalAuthStatusAuthorizedWithConfigFile(t *testing.T) {
	clearAiderAuthEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.WriteFile(filepath.Join(home, ".aider.conf.yml"), []byte("openai-api-key: sk-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := aiderLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestAiderLocalAuthStatusAuthorizedWithDocumentedAPIKeyList(t *testing.T) {
	clearAiderAuthEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".aider.conf.yml"), []byte("api-key:\n  - openrouter=sk-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, ok, err := aiderLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestAiderLocalAuthStatusAuthorizedWithDotEnv(t *testing.T) {
	clearAiderAuthEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.WriteFile(filepath.Join(home, ".env"), []byte("ANTHROPIC_API_KEY=sk-ant-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := aiderLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestAiderLocalAuthStatusUnknownWhenMissing(t *testing.T) {
	clearAiderAuthEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	status, ok, err := aiderLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func clearAiderAuthEnv(t *testing.T) {
	t.Helper()
	for _, name := range aiderAPIKeyEnvVars {
		t.Setenv(name, "")
	}
}
