package primeagent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestPrimeLocalAuthStatusAuthorizedFromProviderEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	status, ok, err := primeLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestPrimeLocalAuthStatusAuthorizedFromAuthFile(t *testing.T) {
	clearPrimeCredentialEnv(t)
	dir := t.TempDir()
	t.Setenv(primeAgentCodingAgentDirEnv, dir)
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte(`{"anthropic":{"type":"api_key","key":"test-key"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := primeLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestPrimeLocalAuthStatusUnknownForEmptyAuthFile(t *testing.T) {
	clearPrimeCredentialEnv(t)
	dir := t.TempDir()
	t.Setenv(primeAgentCodingAgentDirEnv, dir)
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := primeLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func TestPrimeLocalAuthStatusAuthorizedFromModelsFileCredentials(t *testing.T) {
	clearPrimeCredentialEnv(t)
	dir := t.TempDir()
	t.Setenv(primeAgentCodingAgentDirEnv, dir)
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"providers":{"custom":{"apiKey":"looks-like-a-key"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	status, ok, err := primeLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestPrimeCredentialJSONIgnoresMCPOnlyCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(`{"mcp:notion":{"accessToken":"mcp-token"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	status, ok, err := primeCredentialJSONStatus(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func TestPrimeModelsJSONIgnoresMCPOnlyCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(path, []byte(`{"mcp:notion":{"accessToken":"mcp-token"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	status, ok, err := primeCredentialJSONStatus(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func TestPrimeCredentialJSONAuthorizedFromOAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(`{"openai-codex":{"type":"oauth","access":"oauth-token"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	status, ok, err := primeCredentialJSONStatus(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func clearPrimeCredentialEnv(t *testing.T) {
	t.Helper()
	for _, name := range primeAPIKeyEnvVars {
		t.Setenv(name, "")
	}
	for _, name := range []string{
		"AWS_PROFILE", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI",
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN",
		"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_PROJECT_ID",
		"GCLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION", "XDG_CONFIG_HOME", "APPDATA",
	} {
		t.Setenv(name, "")
	}
}
