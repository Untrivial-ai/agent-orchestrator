package auggie

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestAuthStatusIsUnknownWithoutLocalEvidence(t *testing.T) {
	t.Setenv("AUGMENT_SESSION_AUTH", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	status, err := (&Plugin{resolvedBinary: "auggie"}).AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", status, ports.AgentAuthStatusUnknown)
	}
}

func TestAuggieLocalAuthStatusAuthorizedWithSessionEnv(t *testing.T) {
	t.Setenv("AUGMENT_SESSION_AUTH", `{"token":"session-token"}`)

	status, ok, err := auggieLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestAuggieSessionAuthStatusAuthorizedWithStoredSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	if err := os.WriteFile(path, []byte(`{"accessToken":"session-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := auggieSessionAuthStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}
