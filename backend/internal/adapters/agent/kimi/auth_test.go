package kimi

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestKimiLocalAuthStatusAuthorizedWithEnvKey(t *testing.T) {
	t.Setenv("KIMI_API_KEY", "kimi-key")

	status, ok, err := kimiLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestKimiLocalAuthStatusAuthorizedWithOpenAIEnvKey(t *testing.T) {
	clearKimiAuthEnv(t)
	t.Setenv("OPENAI_API_KEY", "openai-key")

	status, ok, err := kimiLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestKimiLocalAuthStatusUsesCurrentShareDirJSONConfig(t *testing.T) {
	clearKimiAuthEnv(t)
	home := t.TempDir()
	t.Setenv("KIMI_SHARE_DIR", home)
	t.Setenv("KIMI_CODE_HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{
  "providers": {
    "openai": {"type": "openai_responses", "api_key": "secret"}
  }
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := kimiLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestKimiConfigAuthStatusAuthorizedWithOAuthReference(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(configPath, []byte(`
[providers."managed:kimi-code"]
type = "kimi"
api_key = ""
oauth = { storage = "file", key = "oauth/kimi-code" }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	credentialsDir := filepath.Join(home, "credentials")
	if err := os.MkdirAll(credentialsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credentialsDir, "kimi-code.json"), []byte(`{"refresh_token":"token"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := kimiConfigAuthStatus(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestKimiConfigAuthStatusUnknownWithOAuthReferenceButNoToken(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte(`
[providers."managed:kimi-code"]
oauth = { storage = "file", key = "oauth/kimi-code" }
`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := kimiConfigAuthStatus(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func TestKimiLocalAuthStatusUsesKimiCodeHome(t *testing.T) {
	clearKimiAuthEnv(t)
	home := t.TempDir()
	t.Setenv("KIMI_SHARE_DIR", t.TempDir())
	t.Setenv("KIMI_CODE_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(`
[providers.zai-coding-plan]
type = "openai-compatible"
api_key = "secret"
base_url = "https://api.z.ai/api/coding/paas/v4"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := kimiLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestKimiLocalAuthStatusUsesKimiCredentials(t *testing.T) {
	clearKimiAuthEnv(t)
	home := t.TempDir()
	t.Setenv("KIMI_SHARE_DIR", t.TempDir())
	t.Setenv("KIMI_CODE_HOME", home)
	credentialsDir := filepath.Join(home, "credentials")
	if err := os.MkdirAll(credentialsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credentialsDir, "kimi-code.json"), []byte(`{"access_token":"token"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := kimiLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestKimiLocalAuthStatusCredentialsOverrideEmptyConfigAPIKeys(t *testing.T) {
	clearKimiAuthEnv(t)
	home := t.TempDir()
	t.Setenv("KIMI_SHARE_DIR", t.TempDir())
	t.Setenv("KIMI_CODE_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(`
[providers.zai-coding-plan]
api_key = ""
[providers.moonshot]
api_key = ""
`), 0o600); err != nil {
		t.Fatal(err)
	}
	credentialsDir := filepath.Join(home, "credentials")
	if err := os.MkdirAll(credentialsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credentialsDir, "kimi-code.json"), []byte(`{"access_token":"token"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := kimiLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func clearKimiAuthEnv(t *testing.T) {
	t.Helper()
	for _, name := range kimiAPIKeyEnvVars {
		t.Setenv(name, "")
	}
}

func TestKimiConfigAuthStatusAuthorizedWithProviderAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`
[providers.zai-coding-plan]
api_key = "secret"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := kimiConfigAuthStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestKimiConfigAuthStatusUnknownWithEmptyAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`
[providers.zai-coding-plan]
api_key = ""
`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := kimiConfigAuthStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func TestKimiCredentialsAuthStatusAuthorizedWithRefreshToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kimi-code.json")
	if err := os.WriteFile(path, []byte(`{"refresh_token":"token"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := kimiCredentialsAuthStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestKimiCredentialsAuthStatusUnknownWithEmptyTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kimi-code.json")
	if err := os.WriteFile(path, []byte(`{"access_token":"","refresh_token":"","token_type":"bearer"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := kimiCredentialsAuthStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}
