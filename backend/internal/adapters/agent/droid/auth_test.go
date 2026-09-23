package droid

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestAuthStatusAuthorizedFromFactoryAPIKey(t *testing.T) {
	t.Setenv("FACTORY_API_KEY", "fk-test")

	got, err := (&Plugin{resolvedBinary: "droid"}).AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusAuthorized {
		t.Fatalf("AuthStatus = %q, want %q", got, ports.AgentAuthStatusAuthorized)
	}
}

func TestDroidFactoryAuthStatusIgnoresUndocumentedAuthFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "auth.v2.file"), []byte("encrypted auth"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.v2.key"), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := droidFactoryAuthStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func TestDroidFactoryAuthStatusDoesNotTreatUnresolvedEnvPlaceholderAsKey(t *testing.T) {
	dir := t.TempDir()
	settings := `{"customModels":[{"model":"custom","baseUrl":"https://api.example.com","apiKey":"${PROVIDER_API_KEY}"}]}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := droidFactoryAuthStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func TestDroidFactoryAuthStatusAuthorizedFromCustomModelAPIKey(t *testing.T) {
	dir := t.TempDir()
	settings := `{"customModels":[{"model":"claude-sonnet-4-5-20250929","baseUrl":"https://api.anthropic.com","apiKey":"sk-test"}],"model":"custom:Sonnet-0"}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := droidFactoryAuthStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestDroidFactoryAuthStatusUnknownFromCustomModelWithoutAPIKey(t *testing.T) {
	dir := t.TempDir()
	settings := `{"customModels":[{"model":"claude-sonnet-4-5-20250929","baseUrl":"https://api.anthropic.com","apiKey":""}]}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := droidFactoryAuthStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}
