package muse

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestMuseLocalAuthStatusAuthorizedWithMetaAPIKey(t *testing.T) {
	clearMuseAuthEnv(t)
	t.Setenv(museAPIKeyEnvVar, "test-api-key")
	status, ok, err := museLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestMuseLocalAuthStatusUsesXDGMetaOAuth(t *testing.T) {
	clearMuseAuthEnv(t)
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	path := filepath.Join(root, "muse", "auth.json")
	writeMuseAuthFixture(t, path, `{"schema_version":1,"providers":{"meta":{"mechanism":"oauth","access_token":"fixture-access-token"}}}`)

	status, ok, err := museLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestMuseLocalAuthStatusHonorsExplicitAuthPath(t *testing.T) {
	clearMuseAuthEnv(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "credentials.json")
	t.Setenv("MUSE_AUTH_PATH", path)
	writeMuseAuthFixture(t, path, `{"providers":{"meta":{"mechanism":"oauth","access_token":"fixture-access-token"}}}`)

	status, ok, err := museLocalAuthStatus(context.Background())
	if err != nil || !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v, %v), want (authorized, true, nil)", status, ok, err)
	}
}

func TestMuseAuthJSONStatusOAuthRequiresAccessToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeMuseAuthFixture(t, path, `{"providers":{"meta":{"mechanism":"oauth","access_token":""}}}`)
	status, ok, err := museAuthJSONStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func TestMuseAuthJSONStatusSupportsStoredMetaAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeMuseAuthFixture(t, path, `{"providers":{"meta":{"mechanism":"api_key","api_key":"fixture-api-key"}}}`)
	status, ok, err := museAuthJSONStatus(path)
	if err != nil || !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v, %v), want (authorized, true, nil)", status, ok, err)
	}
}

func TestMuseAuthJSONStatusUnknownWithoutMetaCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeMuseAuthFixture(t, path, `{"schema_version":1,"providers":{}}`)
	status, ok, err := museAuthJSONStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func TestMuseAuthJSONStatusRejectsMalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeMuseAuthFixture(t, path, `{not-json`)
	status, ok, err := museAuthJSONStatus(path)
	if err == nil || ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v, %v), want (unknown, false, error)", status, ok, err)
	}
}

func TestMuseLocalAuthStatusUnknownWhenMissing(t *testing.T) {
	clearMuseAuthEnv(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	status, ok, err := museLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func TestAuthStatusUsesLocalCredentialProbe(t *testing.T) {
	clearMuseAuthEnv(t)
	t.Setenv(museAPIKeyEnvVar, "configured")
	status, err := (&Plugin{resolvedBinary: "muse"}).AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = %q, want %q", status, ports.AgentAuthStatusAuthorized)
	}
}

func TestMuseLocalAuthStatusHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	status, ok, err := museLocalAuthStatus(ctx)
	if !errors.Is(err, context.Canceled) || ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v, %v), want (unknown, false, context.Canceled)", status, ok, err)
	}
}

func writeMuseAuthFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func clearMuseAuthEnv(t *testing.T) {
	t.Helper()
	t.Setenv(museAPIKeyEnvVar, "")
	t.Setenv("MUSE_AUTH_PATH", "")
	t.Setenv("XDG_CONFIG_HOME", "")
}
