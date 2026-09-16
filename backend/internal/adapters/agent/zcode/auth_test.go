package zcode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func writeZcodeCredentials(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".zcode", "v2")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir credentials dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
}

// testJWT builds a minimal unsigned JWT with the given exp claim.
func testJWT(t *testing.T, exp int64) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	claims, err := json.Marshal(map[string]any{"exp": exp})
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(header) + "." + enc.EncodeToString(claims) + ".sig"
}

func TestAuthStatusAuthorizedWithUnexpiredToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeZcodeCredentials(t, home, `{"oauth:zai:access_token": "`+testJWT(t, time.Now().Add(time.Hour).Unix())+`"}`)
	plugin := &Plugin{resolvedBinary: "zcode"}
	status, err := plugin.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = %q, want authorized", status)
	}
}

func TestAuthStatusUnauthorizedWithExpiredToken(t *testing.T) {
	// The live incident: OAuth expired while apiKey strings stayed in
	// cli/config.json. The checker must read the real credential's exp.
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeZcodeCredentials(t, home, `{"oauth:zai:access_token": "`+testJWT(t, time.Now().Add(-time.Hour).Unix())+`"}`)
	plugin := &Plugin{resolvedBinary: "zcode"}
	status, err := plugin.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if status != ports.AgentAuthStatusUnauthorized {
		t.Fatalf("status = %q, want unauthorized", status)
	}
}

func TestAuthStatusUnauthorizedWithoutCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	plugin := &Plugin{resolvedBinary: "zcode"}
	status, err := plugin.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if status != ports.AgentAuthStatusUnauthorized {
		t.Fatalf("status = %q, want unauthorized", status)
	}
}

func TestAuthStatusUnauthorizedWithEmptyToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeZcodeCredentials(t, home, `{"oauth:zai:access_token": ""}`)
	plugin := &Plugin{resolvedBinary: "zcode"}
	status, err := plugin.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if status != ports.AgentAuthStatusUnauthorized {
		t.Fatalf("status = %q, want unauthorized", status)
	}
}

func TestAuthStatusUnknownWithMalformedToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeZcodeCredentials(t, home, `{"oauth:zai:access_token": "not-a-jwt"}`)
	plugin := &Plugin{resolvedBinary: "zcode"}
	status, err := plugin.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want unknown (malformed token is not evidence either way)", status)
	}
}
