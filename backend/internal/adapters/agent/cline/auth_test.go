package cline

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestClineProviderAuthStatusAuthorizedWithOAuth(t *testing.T) {
	writeClineProvidersFile(t, `{
		"version": 1,
		"lastUsedProvider": "cline",
		"providers": {
			"cline": {
				"settings": {
					"provider": "cline",
					"auth": {
						"accessToken": "token",
						"refreshToken": "refresh",
						"expiresAt": `+futureMillis(t)+`
					}
				}
			}
		}
	}`)

	status, ok, err := clineProviderAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestClineProviderAuthStatusAuthorizedWithAPIKey(t *testing.T) {
	writeClineProvidersFile(t, `{
		"version": 1,
		"lastUsedProvider": "openai",
		"providers": {
			"openai": {
				"settings": {
					"provider": "openai",
					"apiKey": "sk-test"
				}
			}
		}
	}`)

	status, ok, err := clineProviderAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusAuthorized)
	}
}

func TestClineAuthStatusAuthorizedWithDocumentedEnvKey(t *testing.T) {
	t.Setenv("CLINE_API_KEY", "cline-key")
	status, err := (&Plugin{resolvedBinary: "cline"}).AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = %q, want %q", status, ports.AgentAuthStatusAuthorized)
	}
}

func TestClineProviderAuthStatusUnknownWithExpiredOAuth(t *testing.T) {
	writeClineProvidersFile(t, `{
		"version": 1,
		"lastUsedProvider": "cline",
		"providers": {
			"cline": {
				"settings": {
					"provider": "cline",
					"auth": {
						"accessToken": "token",
						"expiresAt": 1
					}
				}
			}
		}
	}`)

	status, ok, err := clineProviderAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func TestClineProviderAuthStatusUnknownWhenMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	status, ok, err := clineProviderAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func writeClineProvidersFile(t *testing.T, content string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	settingsDir := filepath.Join(home, ".cline", "data", "settings")
	if err := os.MkdirAll(settingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "providers.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func futureMillis(t *testing.T) string {
	t.Helper()
	return strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10)
}
