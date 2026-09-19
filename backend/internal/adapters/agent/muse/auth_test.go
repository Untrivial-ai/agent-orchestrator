package muse

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestMuseLocalAuthStatusConfiguredWithMetaAPIKey(t *testing.T) {
	clearMuseAuthEnv(t)
	t.Setenv(museAPIKeyEnvVar, "test-api-key")
	status, ok, err := museLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusConfigured)
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
	if !ok || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusConfigured)
	}
}

func TestMuseLocalAuthStatusHonorsExplicitAuthPath(t *testing.T) {
	clearMuseAuthEnv(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "credentials.json")
	t.Setenv("MUSE_AUTH_PATH", path)
	writeMuseAuthFixture(t, path, `{"providers":{"meta":{"mechanism":"oauth","access_token":"fixture-access-token"}}}`)

	status, ok, err := museLocalAuthStatus(context.Background())
	if err != nil || !ok || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = (%q, %v, %v), want (configured, true, nil)", status, ok, err)
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
	if err != nil || !ok || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = (%q, %v, %v), want (configured, true, nil)", status, ok, err)
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
	if status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, want %q", status, ports.AgentAuthStatusConfigured)
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
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func TestMuseAuthStatusStorageMetadataIsNotCredential(t *testing.T) {
	for _, body := range []string{
		`{"providers":{"meta":{"storage":"keychain","mechanism":"oauth"}}}`,
		`{"providers":{"meta":{"storage":"keychain","mechanism":"oauth","access_token":"stale-file-token"}}}`,
		`{"storage":"keychain","providers":{"meta":{"mechanism":"api_key","api_key":"stale-file-key"}}}`,
		`{"providers":{"meta":{"storage":"unsupported","mechanism":"oauth","access_token":"stale-file-token"}}}`,
	} {
		t.Run(body, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "muse", "auth.json")
			writeMuseAuthFixture(t, path, body)
			d := museAuthDependencies(t, map[string]string{"XDG_CONFIG_HOME": dir})
			status, err := museAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
			if err != nil || status != ports.AgentAuthStatusUnknown {
				t.Fatalf("status = (%q, %v), want unknown", status, err)
			}
		})
	}
}

func TestMuseAuthStatusScopedPathOverridesInheritedPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scoped.json")
	writeMuseAuthFixture(t, path, `{"providers":{"meta":{"storage":"file","mechanism":"api_key","api_key":"fixture-key"}}}`)
	d := museAuthDependencies(t, map[string]string{"MUSE_AUTH_PATH": filepath.Join(t.TempDir(), "missing.json")})
	scope := ports.AgentAuthCheck{Env: map[string]string{"MUSE_AUTH_PATH": path}}
	status, err := museAuthStatus(context.Background(), scope, d)
	if err != nil || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = (%q, %v), want configured", status, err)
	}
}

func TestMuseAuthStatusScopedEmptyEnvOverridesInheritedKey(t *testing.T) {
	d := museAuthDependencies(t, map[string]string{"META_API_KEY": "inherited-key"})
	scope := ports.AgentAuthCheck{Env: map[string]string{"META_API_KEY": ""}}
	status, err := museAuthStatus(context.Background(), scope, d)
	if err != nil || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want unknown", status, err)
	}
}

func TestMuseAuthStatusEnvPrecedesKeychainMetadata(t *testing.T) {
	dir := t.TempDir()
	writeMuseAuthFixture(t, filepath.Join(dir, "muse", "auth.json"), `{"providers":{"meta":{"storage":"keychain","mechanism":"oauth"}}}`)
	d := museAuthDependencies(t, map[string]string{"META_API_KEY": "fixture-key", "XDG_CONFIG_HOME": dir})
	d.ReadFile = func(string) ([]byte, error) {
		t.Fatal("env key must precede file/keychain lookup")
		return nil, os.ErrNotExist
	}
	status, err := museAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
	if err != nil || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = (%q, %v), want configured", status, err)
	}
}

func TestMuseAuthStatusIgnoresSettingsAndMalformedFiles(t *testing.T) {
	for _, body := range []string{"", `{bad-json`, `{"providers":{"other":{"mechanism":"oauth","access_token":"unrelated"}}}`} {
		t.Run(body, func(t *testing.T) {
			dir := t.TempDir()
			writeMuseAuthFixture(t, filepath.Join(dir, "muse", "settings.json"), `{"providers":{"meta":{"mechanism":"api_key","api_key":"not-credentials"}}}`)
			if body != "" {
				writeMuseAuthFixture(t, filepath.Join(dir, "muse", "auth.json"), body)
			}
			status, err := museAuthStatus(context.Background(), ports.AgentAuthCheck{}, museAuthDependencies(t, map[string]string{"XDG_CONFIG_HOME": dir}))
			if err != nil || status != ports.AgentAuthStatusUnknown {
				t.Fatalf("status = (%q, %v), want unknown", status, err)
			}
		})
	}
}

func TestMuseAuthStatusRejectsSymlinkAndOversizedCredentials(t *testing.T) {
	for _, symlink := range []bool{true, false} {
		t.Run(map[bool]string{true: "symlink", false: "oversized"}[symlink], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth.json")
			if symlink {
				target := filepath.Join(t.TempDir(), "target.json")
				writeMuseAuthFixture(t, target, `{"providers":{"meta":{"mechanism":"api_key","api_key":"fixture-key"}}}`)
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(path, make([]byte, authutil.MaxFileSize+1), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			status, err := museAuthStatus(context.Background(), ports.AgentAuthCheck{}, museAuthDependencies(t, map[string]string{"MUSE_AUTH_PATH": path}))
			if err != nil || status != ports.AgentAuthStatusUnknown {
				t.Fatalf("status = (%q, %v), want unknown", status, err)
			}
		})
	}
}

func museAuthDependencies(t *testing.T, env map[string]string) authutil.Dependencies {
	t.Helper()
	if _, ok := env["HOME"]; !ok {
		env["HOME"] = t.TempDir()
	}
	return authutil.Dependencies{
		Getenv: func(key string) string { return env[key] }, GOOS: "darwin",
		Run: func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("unexpected command or unconfirmed keychain selector")
			return nil, errors.New("unexpected command")
		},
	}
}
