package autohand

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestAutohandAuthStatusUnknownWithoutDocumentedLocalCredential(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AUTOHAND_CONFIG", "")
	for _, name := range []string{
		"AUTOHAND_API_KEY", "AUTOHAND_AUTH_TOKEN", "ANTHROPIC_API_KEY", "OPENAI_API_KEY",
		"GEMINI_API_KEY", "GOOGLE_API_KEY", "OPENROUTER_API_KEY", "MISTRAL_API_KEY", "GROQ_API_KEY",
	} {
		t.Setenv(name, "")
	}
	status, err := (&Plugin{resolvedBinary: "autohand"}).AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", status, ports.AgentAuthStatusUnknown)
	}
}

func TestAutohandAuthStatusUsesAUTOHAND_CONFIG(t *testing.T) {
	t.Setenv("AUTOHAND_CONFIG", writeAutohandAuthConfig(t, `{
  "auth": {"token": "session-token"}
}`))
	status, err := (&Plugin{resolvedBinary: "autohand"}).AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = %q, want %q", status, ports.AgentAuthStatusAuthorized)
	}
}

func TestAutohandConfigAuthStatusAuthorized(t *testing.T) {
	path := writeAutohandAuthConfig(t, `{
  "auth": {"token": "session-token", "user": {"email": "agent@example.com"}},
  "provider": "zai",
  "zai": {"apiKey": "real-provider-key", "model": "glm-5.1"}
}`)

	got, err := autohandConfigAuthStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusAuthorized)
	}
}

func TestAutohandConfigAuthStatusUnknownWithAPIKeyHelper(t *testing.T) {
	path := writeAutohandAuthConfig(t, `{
  "auth": {"apiKeyHelper": "security find-generic-password -w -s autohand"}
}`)

	got, err := autohandConfigAuthStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusUnknown)
	}
}

func TestAutohandConfigAuthStatusUnknownWithMissingCloudToken(t *testing.T) {
	path := writeAutohandAuthConfig(t, `{
  "auth": {"token": ""},
  "provider": "zai",
  "zai": {"apiKey": "real-provider-key"}
}`)

	got, err := autohandConfigAuthStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusUnknown)
	}
}

func TestAutohandConfigAuthStatusAuthorizedWithPlaceholderProviderKey(t *testing.T) {
	path := writeAutohandAuthConfig(t, `{
  "auth": {"token": "session-token"},
  "provider": "zai",
  "zai": {"apiKey": "api key ", "model": "glm-5.1"}
}`)

	got, err := autohandConfigAuthStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusAuthorized)
	}
}

func TestAutohandConfigAuthStatusUnknownWhenMissing(t *testing.T) {
	got, err := autohandConfigAuthStatus(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusUnknown)
	}
}

func writeAutohandAuthConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
