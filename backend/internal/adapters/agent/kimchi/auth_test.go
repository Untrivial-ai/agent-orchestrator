package kimchi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// clearKimchiAuthEnv unsets KIMCHI_API_KEY so env-var auth state doesn't leak
// between tests.
func clearKimchiAuthEnv(t *testing.T) {
	t.Helper()
	t.Setenv("KIMCHI_API_KEY", "")
}

// withTempHome sets HOME to a temp dir so tests don't read the real
// ~/.config/kimchi/config.json. Returns the temp dir.
func withTempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// writeKimchiGlobalConfig writes content to ~/.config/kimchi/config.json under
// the temp HOME. Returns the config path.
func writeKimchiGlobalConfig(t *testing.T, home, content string) string {
	t.Helper()
	configPath := filepath.Join(home, ".config", "kimchi", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}

// ---------------------------------------------------------------------------
// kimchiConfigAuthStatus — pure config-file probe (no binary dependency)
// ---------------------------------------------------------------------------

func TestConfigAuthStatusConfiguredFromAPIKey(t *testing.T) {
	home := withTempHome(t)
	path := writeKimchiGlobalConfig(t, home, `{"apiKey":"test-key-123"}`)

	got, err := kimchiConfigAuthStatus(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
	}
}

func TestConfigAuthStatusConfiguredFromSnakeCase(t *testing.T) {
	home := withTempHome(t)
	path := writeKimchiGlobalConfig(t, home, `{"api_key":"legacy-key-456"}`)

	got, err := kimchiConfigAuthStatus(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
	}
}

func TestConfigAuthStatusPrefersCamelCase(t *testing.T) {
	home := withTempHome(t)
	path := writeKimchiGlobalConfig(t, home, `{"apiKey":"new-key","api_key":"old-key"}`)

	got, err := kimchiConfigAuthStatus(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
	}
}

func TestConfigAuthStatusUnknownWhenNoKey(t *testing.T) {
	home := withTempHome(t)
	path := writeKimchiGlobalConfig(t, home, `{"otherField":"value","onboarding":{"done":true}}`)

	got, err := kimchiConfigAuthStatus(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusUnknown)
	}
}

func TestConfigAuthStatusUnknownWhenKeyEmpty(t *testing.T) {
	home := withTempHome(t)
	path := writeKimchiGlobalConfig(t, home, `{"apiKey":""}`)

	got, err := kimchiConfigAuthStatus(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusUnknown)
	}
}

func TestConfigAuthStatusUnknownWhenFileMissing(t *testing.T) {
	home := withTempHome(t)
	missingPath := filepath.Join(home, ".config", "kimchi", "config.json")

	got, err := kimchiConfigAuthStatus(context.Background(), missingPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusUnknown)
	}
}

func TestConfigAuthStatusUnknownWhenMalformed(t *testing.T) {
	home := withTempHome(t)
	path := writeKimchiGlobalConfig(t, home, `{not valid json`)

	got, err := kimchiConfigAuthStatus(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusUnknown)
	}
}

func TestConfigAuthStatusUnknownWhenPathEmpty(t *testing.T) {
	got, err := kimchiConfigAuthStatus(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusUnknown)
	}
}

func TestConfigAuthStatusUnknownWhenNonStringKey(t *testing.T) {
	home := withTempHome(t)
	path := writeKimchiGlobalConfig(t, home, `{"apiKey":12345}`)

	got, err := kimchiConfigAuthStatus(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusUnknown)
	}
}

// ---------------------------------------------------------------------------
// kimchiExtractAPIKey — unit test for the field extractor
// ---------------------------------------------------------------------------

func TestExtractAPIKey(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantKey string
	}{
		{"camelCase", `{"apiKey":"abc"}`, "abc"},
		{"snake_case", `{"api_key":"def"}`, "def"},
		{"both prefers camelCase", `{"apiKey":"new","api_key":"old"}`, "new"},
		{"empty string ignored", `{"apiKey":""}`, ""},
		{"whitespace-only ignored", `{"apiKey":"  "}`, ""},
		{"missing field", `{"other":"x"}`, ""},
		{"non-string value", `{"apiKey":42}`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var config map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.json), &config); err != nil {
				t.Fatal(err)
			}
			got := kimchiExtractAPIKey(config)
			if got != tc.wantKey {
				t.Fatalf("kimchiExtractAPIKey = %q, want %q", got, tc.wantKey)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Plugin.AuthStatus — integration of binary + env + config
// ---------------------------------------------------------------------------

func TestAuthStatusConfiguredFromEnvVar(t *testing.T) {
	clearKimchiAuthEnv(t)
	withTempHome(t) // ensure no real config file is read
	t.Setenv("KIMCHI_API_KEY", "env-key-789")
	plugin := &Plugin{resolvedBinary: "kimchi"}

	got, err := plugin.AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusConfigured {
		t.Fatalf("AuthStatus = %q, want %q", got, ports.AgentAuthStatusConfigured)
	}
}

func TestAuthStatusConfiguredFromConfig(t *testing.T) {
	clearKimchiAuthEnv(t)
	home := withTempHome(t)
	writeKimchiGlobalConfig(t, home, `{"apiKey":"file-key-012"}`)
	plugin := &Plugin{resolvedBinary: "kimchi"}

	got, err := plugin.AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusConfigured {
		t.Fatalf("AuthStatus = %q, want %q", got, ports.AgentAuthStatusConfigured)
	}
}

func TestAuthStatusUnknownFromConfigWithoutKey(t *testing.T) {
	clearKimchiAuthEnv(t)
	home := withTempHome(t)
	writeKimchiGlobalConfig(t, home, `{"preferences":{"theme":"dark"}}`)
	plugin := &Plugin{resolvedBinary: "kimchi"}

	got, err := plugin.AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusUnknown {
		t.Fatalf("AuthStatus = %q, want %q", got, ports.AgentAuthStatusUnknown)
	}
}

func TestAuthStatusUnknownWhenNoEnvAndNoConfig(t *testing.T) {
	clearKimchiAuthEnv(t)
	withTempHome(t) // no config file under temp home
	plugin := &Plugin{resolvedBinary: "kimchi"}

	got, err := plugin.AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusUnknown {
		t.Fatalf("AuthStatus = %q, want %q", got, ports.AgentAuthStatusUnknown)
	}
}

func TestAuthStatusEnvVarPrecedenceOverConfig(t *testing.T) {
	clearKimchiAuthEnv(t)
	home := withTempHome(t)
	writeKimchiGlobalConfig(t, home, `{"apiKey":"config-key"}`)
	t.Setenv("KIMCHI_API_KEY", "env-key")
	plugin := &Plugin{resolvedBinary: "kimchi"}

	got, err := plugin.AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusConfigured {
		t.Fatalf("AuthStatus = %q, want %q", got, ports.AgentAuthStatusConfigured)
	}
}

func TestKimchiImplementsScopedAuthChecker(t *testing.T) {
	if _, ok := any(&Plugin{resolvedBinary: "kimchi"}).(ports.AgentScopedAuthChecker); !ok {
		t.Fatal("Kimchi must implement AgentScopedAuthChecker")
	}
}

func TestAuthStatusContextCanceled(t *testing.T) {
	clearKimchiAuthEnv(t)
	plugin := &Plugin{resolvedBinary: "kimchi"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := plugin.AuthStatus(ctx)
	if err == nil {
		t.Fatal("AuthStatus: expected error from cancelled context, got nil")
	}
}

func TestAuthStatusUnknownWhenBinaryNotFound(t *testing.T) {
	clearKimchiAuthEnv(t)
	withTempHome(t)
	// Plugin with no resolved binary: kimchiBinary calls ResolveKimchiBinary.
	// On machines without kimchi installed (CI), this returns ErrAgentBinaryNotFound
	// → (Unknown, nil). On dev machines where kimchi IS installed, the binary
	// resolves and the test falls through to config — so we accept either Unknown
	// (binary not found) or Unknown (no config file under temp home).
	plugin := &Plugin{}

	got, err := plugin.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus should not return error for missing binary: %v", err)
	}
	if got != ports.AgentAuthStatusUnknown {
		t.Fatalf("AuthStatus = %q, want %q", got, ports.AgentAuthStatusUnknown)
	}
}

func TestKimchiScopedAuthResolution(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tests := map[string]struct {
		model       string
		global      string
		project     string
		auth        string
		env         map[string]string
		probeStatus ports.AgentAuthStatus
		want        ports.AgentAuthStatus
		wantProbe   bool
		wantKey     string
	}{
		"project key overrides global key": {
			global: `{"apiKey":"global-key"}`, project: `{"apiKey":"project-key"}`,
			probeStatus: ports.AgentAuthStatusAuthorized, want: ports.AgentAuthStatusAuthorized, wantProbe: true, wantKey: "project-key",
		},
		"environment key overrides project key": {
			project: `{"apiKey":"project-key"}`, env: map[string]string{"KIMCHI_API_KEY": "environment-key"},
			probeStatus: ports.AgentAuthStatusAuthorized, want: ports.AgentAuthStatusAuthorized, wantProbe: true, wantKey: "environment-key",
		},
		"local API key is configured without a probe": {
			global: `{"api_key":"service-key"}`, want: ports.AgentAuthStatusConfigured, wantKey: "service-key",
		},
		"Kimchi service key in harness auth is configured": {
			auth: `{"kimchi-dev":{"type":"api_key","key":"service-key"}}`, want: ports.AgentAuthStatusConfigured,
		},
		"upstream subscription OAuth is configured": {
			model: "openai-codex/gpt-5", auth: `{"openai-codex":{"type":"oauth","access":"access-token","refresh":"refresh-token","expires":1800003600000}}`,
			want: ports.AgentAuthStatusConfigured,
		},
		"custom endpoint pairs with the effective key": {
			global: `{"apiKey":"global-key"}`, project: `{"llmEndpoint":"https://proxy.example/v1"}`,
			want: ports.AgentAuthStatusConfigured, wantKey: "global-key",
		},
		"malformed effective endpoint is unknown": {
			global: `{"apiKey":"global-key"}`, project: `{"llmEndpoint":"file:///tmp/socket"}`,
			want: ports.AgentAuthStatusUnknown,
		},
		"endpoint without its paired key is unknown": {
			project: `{"llmEndpoint":"https://proxy.example/v1"}`,
			want:    ports.AgentAuthStatusUnknown,
		},
		"unrelated provider credential does not leak": {
			model: "openai-codex/gpt-5", auth: `{"anthropic":{"type":"oauth","access":"access-token","refresh":"refresh-token","expires":1800003600000}}`,
			want: ports.AgentAuthStatusUnknown,
		},
		"official probe rejection is unauthorized": {
			global: `{"apiKey":"revoked-key"}`, probeStatus: ports.AgentAuthStatusUnauthorized,
			want: ports.AgentAuthStatusUnauthorized, wantProbe: true, wantKey: "revoked-key",
		},
		"absence is unknown": {want: ports.AgentAuthStatusUnknown},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			home, work := t.TempDir(), t.TempDir()
			if tc.global != "" {
				writeKimchiTestFile(t, filepath.Join(home, ".config", "kimchi", "config.json"), tc.global)
			}
			if tc.project != "" {
				writeKimchiTestFile(t, filepath.Join(work, ".kimchi", "config.json"), tc.project)
			}
			if tc.auth != "" {
				writeKimchiTestFile(t, filepath.Join(home, ".config", "kimchi", "harness", "auth.json"), tc.auth)
			}
			env := map[string]string{"HOME": home}
			for key, value := range tc.env {
				env[key] = value
			}
			deps := authutil.Dependencies{Getenv: func(key string) string { return env[key] }, Now: func() time.Time { return now }}
			probeCalled := false
			var probe kimchiAPIKeyProbe
			if tc.probeStatus != "" {
				probe = func(_ context.Context, key string) (ports.AgentAuthStatus, error) {
					probeCalled = true
					if key != tc.wantKey {
						t.Fatalf("probe key = %q, want selected key", key)
					}
					return tc.probeStatus, nil
				}
			}
			got, err := kimchiAuthStatus(context.Background(), ports.AgentAuthCheck{
				WorkingDir: work,
				Config:     ports.AgentConfig{Model: tc.model},
			}, deps, probe)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
			if probeCalled != tc.wantProbe {
				t.Fatalf("probe called = %v, want %v", probeCalled, tc.wantProbe)
			}
		})
	}
}

func writeKimchiTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
