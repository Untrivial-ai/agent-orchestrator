package grok

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestGrokLocalAuthStatusAuthorizedWithAPIKeyEnv(t *testing.T) {
	t.Setenv("XAI_API_KEY", "xai-test")

	status, ok, err := grokLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestGrokLocalAuthStatusIgnoresUndocumentedGrokAPIKeyEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GROK_API_KEY", "grok-test")
	t.Setenv("XAI_API_KEY", "")

	status, ok, err := grokLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func TestGrokLocalAuthStatusAuthorizedWithConfiguredModelAPIKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	grokDir := filepath.Join(home, ".grok")
	if err := os.MkdirAll(grokDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(grokDir, "config.toml"), []byte("[model.grok-build]\napi_key = \"xai-test\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := grokLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestGrokLocalAuthStatusUsesGrokHome(t *testing.T) {
	grokHome := t.TempDir()
	t.Setenv("GROK_HOME", grokHome)
	if err := os.WriteFile(filepath.Join(grokHome, "auth.json"), []byte(`{"default":{"access_token":"token"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := grokLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestGrokLocalAuthStatusAuthorizedWithAuthFile(t *testing.T) {
	writeGrokAuthFile(t, `{
		"https://auth.x.ai::account": {
			"access_token": "token",
			"refresh_token": "refresh"
		}
	}`)

	status, ok, err := grokLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestGrokLocalAuthStatusUnknownWithEmptyAuthFile(t *testing.T) {
	writeGrokAuthFile(t, `{}`)

	status, ok, err := grokLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func TestGrokLocalAuthStatusIgnoresAuthEntryWithoutTokens(t *testing.T) {
	writeGrokAuthFile(t, `{"https://auth.x.ai::account":{"email":"user@example.com"}}`)

	status, ok, err := grokLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func TestGrokLocalAuthStatusUnknownWhenMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	status, ok, err := grokLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func writeGrokAuthFile(t *testing.T, content string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	grokDir := filepath.Join(home, ".grok")
	if err := os.MkdirAll(grokDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(grokDir, "auth.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
