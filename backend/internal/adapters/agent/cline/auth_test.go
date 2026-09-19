package cline

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func isolateClineAuth(t *testing.T) string {
	t.Helper()
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		t.Setenv(key, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func writeClineAuthFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func clineProviders(provider, settings string) string {
	return `{"version":1,"lastUsedProvider":"` + provider + `","modes":{},"providers":{` + settings + `}}`
}

func scopedClineStatus(t *testing.T, scope ports.AgentAuthCheck) ports.AgentAuthStatus {
	t.Helper()
	checker, ok := any(&Plugin{resolvedBinary: "cline"}).(ports.AgentScopedAuthChecker)
	if !ok {
		t.Fatal("Cline does not implement scoped authentication")
	}
	status, err := checker.AuthStatusFor(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func TestClineProviderSettingsPathResolution(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		args []string
		path func(string) string
	}{
		{name: "default", path: func(home string) string { return filepath.Join(home, ".cline", "data", "settings", "providers.json") }},
		{name: "CLINE_DIR", env: map[string]string{"CLINE_DIR": "config-root"}, path: func(home string) string {
			return filepath.Join(home, "config-root", "data", "settings", "providers.json")
		}},
		{name: "CLINE_DATA_DIR", env: map[string]string{"CLINE_DATA_DIR": "data-root"}, path: func(home string) string { return filepath.Join(home, "data-root", "settings", "providers.json") }},
		{name: "CLINE_PROVIDER_SETTINGS_PATH", env: map[string]string{"CLINE_PROVIDER_SETTINGS_PATH": "provider-settings.json"}, path: func(home string) string { return filepath.Join(home, "provider-settings.json") }},
		{name: "scoped config", args: []string{"cline", "--config", "config-root"}, path: func(home string) string {
			return filepath.Join(home, "config-root", "data", "settings", "providers.json")
		}},
		{name: "scoped data dir", args: []string{"cline", "--data-dir=data-root"}, path: func(home string) string { return filepath.Join(home, "data-root", "settings", "providers.json") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := isolateClineAuth(t)
			for key, value := range tc.env {
				t.Setenv(key, filepath.Join(home, value))
			}
			writeClineAuthFile(t, tc.path(home), clineProviders("openai", `"openai":{"settings":{"provider":"openai","apiKey":"test-key"},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}`))
			if got := scopedClineStatus(t, ports.AgentAuthCheck{WorkingDir: home, Args: tc.args}); got != ports.AgentAuthStatusConfigured {
				t.Fatalf("status = %q, want configured", got)
			}
		})
	}
}

func TestClineScopedEnvironmentAndKeyPrecedence(t *testing.T) {
	home := isolateClineAuth(t)
	dataDir := filepath.Join(home, "scoped-data")
	writeClineAuthFile(t, filepath.Join(dataDir, "settings", "providers.json"), clineProviders("anthropic", `"anthropic":{"settings":{"provider":"anthropic"},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}`))
	scope := ports.AgentAuthCheck{
		WorkingDir: home,
		Env:        map[string]string{"CLINE_DATA_DIR": dataDir, "ANTHROPIC_API_KEY": "unrelated-environment-key"},
		Args:       []string{"cline", "--provider", "anthropic", "--key", "scoped-key"},
	}
	if got := scopedClineStatus(t, scope); got != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, want configured", got)
	}
}

func TestClineTypedProviderEvidence(t *testing.T) {
	future := strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10)
	cases := []struct {
		name, selected, entries string
		env                     map[string]string
		files                   map[string]string
		want                    ports.AgentAuthStatus
	}{
		{name: "top-level api key", selected: "openai", entries: `"openai":{"settings":{"provider":"openai","apiKey":"test-key"},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}`, want: ports.AgentAuthStatusConfigured},
		{name: "nested auth api key", selected: "anthropic", entries: `"anthropic":{"settings":{"provider":"anthropic","auth":{"apiKey":"test-key"}},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}`, want: ports.AgentAuthStatusConfigured},
		{name: "fresh oauth", selected: "cline", entries: `"cline":{"settings":{"provider":"cline","auth":{"accessToken":"token","expiresAt":` + future + `}},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"oauth"}`, want: ports.AgentAuthStatusConfigured},
		{name: "expired oauth", selected: "cline", entries: `"cline":{"settings":{"provider":"cline","auth":{"accessToken":"token","expiresAt":1}},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"oauth"}`, want: ports.AgentAuthStatusUnauthorized},
		{name: "expired refreshable oauth", selected: "cline", entries: `"cline":{"settings":{"provider":"cline","auth":{"accessToken":"token","refreshToken":"refresh","expiresAt":1}},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"oauth"}`, want: ports.AgentAuthStatusConfigured},
		{name: "invalid negative expiry", selected: "cline", entries: `"cline":{"settings":{"provider":"cline","auth":{"accessToken":"token","expiresAt":-1}},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"oauth"}`, want: ports.AgentAuthStatusUnknown},
		{name: "selected entry only", selected: "openai", entries: `"openai":{"settings":{"provider":"openai"},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"},"anthropic":{"settings":{"provider":"anthropic","apiKey":"unrelated"},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}`, want: ports.AgentAuthStatusUnknown},
		{name: "cline pass uses cline login", selected: "cline-pass", entries: `"cline":{"settings":{"provider":"cline","auth":{"accessToken":"token","refreshToken":"refresh","expiresAt":1}},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"oauth"},"cline-pass":{"settings":{"provider":"cline-pass"},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}`, want: ports.AgentAuthStatusConfigured},
		{name: "inline AWS", selected: "bedrock", entries: `"bedrock":{"settings":{"provider":"bedrock","aws":{"accessKey":"id","secretKey":"secret","region":"us-east-1"}},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}`, want: ports.AgentAuthStatusConfigured},
		{name: "AWS default chain", selected: "bedrock", entries: `"bedrock":{"settings":{"provider":"bedrock","aws":{"profile":"work","region":"us-east-1"}},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}`, files: map[string]string{".aws/credentials": "[work]\naws_access_key_id=test\naws_secret_access_key=test\n"}, want: ports.AgentAuthStatusConfigured},
		{name: "AWS API key mode ignores chain", selected: "bedrock", entries: `"bedrock":{"settings":{"provider":"bedrock","aws":{"authentication":"api-key","region":"us-east-1"}},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}`, env: map[string]string{"AWS_ACCESS_KEY_ID": "unrelated", "AWS_SECRET_ACCESS_KEY": "unrelated"}, want: ports.AgentAuthStatusUnknown},
		{name: "Google ADC", selected: "vertex", entries: `"vertex":{"settings":{"provider":"vertex","gcp":{"projectId":"project","region":"us-east1"}},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}`, files: map[string]string{".config/gcloud/application_default_credentials.json": `{"type":"authorized_user","client_id":"id","client_secret":"secret","refresh_token":"refresh"}`}, want: ports.AgentAuthStatusConfigured},
		{name: "Azure identity", selected: "azure", entries: `"azure":{"settings":{"provider":"azure","azure":{"useIdentity":true},"baseUrl":"https://example.openai.azure.com"},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}`, env: map[string]string{"AZURE_TENANT_ID": "tenant", "AZURE_CLIENT_ID": "client", "AZURE_CLIENT_SECRET": "secret"}, want: ports.AgentAuthStatusConfigured},
		{name: "Azure identity ignores API key", selected: "azure", entries: `"azure":{"settings":{"provider":"azure","azure":{"useIdentity":true},"baseUrl":"https://example.openai.azure.com"},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}`, env: map[string]string{"AZURE_OPENAI_API_KEY": "unrelated"}, want: ports.AgentAuthStatusUnknown},
		{name: "SAP client credentials", selected: "sapaicore", entries: `"sapaicore":{"settings":{"provider":"sapaicore","sap":{"clientId":"client","clientSecret":"secret","tokenUrl":"https://auth.example.test/oauth/token","resourceGroup":"default"}},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}`, want: ports.AgentAuthStatusConfigured},
		{name: "incomplete SAP", selected: "sapaicore", entries: `"sapaicore":{"settings":{"provider":"sapaicore","sap":{"clientId":"client","tokenUrl":"https://auth.example.test/oauth/token"}},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}`, want: ports.AgentAuthStatusUnknown},
		{name: "Ollama local", selected: "ollama", entries: `"ollama":{"settings":{"provider":"ollama","baseUrl":"http://127.0.0.1:11434"},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}`, want: ports.AgentAuthStatusNotApplicable},
		{name: "LM Studio local", selected: "lmstudio", entries: `"lmstudio":{"settings":{"provider":"lmstudio","baseUrl":"http://localhost:1234/v1"},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}`, want: ports.AgentAuthStatusNotApplicable},
		{name: "unrelated nested token", selected: "openai", entries: `"openai":{"settings":{"provider":"openai","metadata":{"apiKey":"unrelated"}},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}`, want: ports.AgentAuthStatusUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := isolateClineAuth(t)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			for path, content := range tc.files {
				writeClineAuthFile(t, filepath.Join(home, path), content)
			}
			writeClineAuthFile(t, filepath.Join(home, ".cline", "data", "settings", "providers.json"), clineProviders(tc.selected, tc.entries))
			if got := scopedClineStatus(t, ports.AgentAuthCheck{}); got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClineMissingScopedKeyValueIsNotCredentialEvidence(t *testing.T) {
	home := isolateClineAuth(t)
	writeClineAuthFile(t, filepath.Join(home, ".cline", "data", "settings", "providers.json"), clineProviders("openai", `"openai":{"settings":{"provider":"openai"},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}`))
	if got := scopedClineStatus(t, ports.AgentAuthCheck{Args: []string{"cline", "--key", "--provider", "openai"}}); got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want unknown", got)
	}
}

func TestClineScopedProviderAndClineAPIKey(t *testing.T) {
	home := isolateClineAuth(t)
	t.Setenv("CLINE_API_KEY", "cline-key")
	writeClineAuthFile(t, filepath.Join(home, ".cline", "data", "settings", "providers.json"), clineProviders("openai", `"openai":{"settings":{"provider":"openai"},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}`))
	if got := scopedClineStatus(t, ports.AgentAuthCheck{}); got != ports.AgentAuthStatusUnknown {
		t.Fatalf("unrelated CLINE_API_KEY status = %q, want unknown", got)
	}
	if got := scopedClineStatus(t, ports.AgentAuthCheck{Args: []string{"cline", "-P", "cline"}}); got != ports.AgentAuthStatusConfigured {
		t.Fatalf("selected cline status = %q, want configured", got)
	}
}

func TestClineSelectedProviderEnvironmentKeysAndAliases(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		stored   string
		env      map[string]string
		want     ports.AgentAuthStatus
	}{
		{name: "anthropic apiKeyEnv", provider: "anthropic", stored: "anthropic", env: map[string]string{"ANTHROPIC_API_KEY": "test-key"}, want: ports.AgentAuthStatusConfigured},
		{name: "openai alias", provider: "openai", stored: "openai-compatible", env: map[string]string{"OPENAI_API_KEY": "test-key"}, want: ports.AgentAuthStatusConfigured},
		{name: "together alias", provider: "togetherai", stored: "together", env: map[string]string{"TOGETHER_API_KEY": "test-key"}, want: ports.AgentAuthStatusConfigured},
		{name: "SAP alias", provider: "sap-ai-core", stored: "sapaicore", env: map[string]string{"AICORE_SERVICE_KEY": "test-key"}, want: ports.AgentAuthStatusConfigured},
		{name: "unrelated environment", provider: "openai", stored: "openai-compatible", env: map[string]string{"ANTHROPIC_API_KEY": "unrelated"}, want: ports.AgentAuthStatusUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := isolateClineAuth(t)
			entry := `"` + tt.stored + `":{"settings":{"provider":"` + tt.stored + `"},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}`
			writeClineAuthFile(t, filepath.Join(home, ".cline", "data", "settings", "providers.json"), clineProviders(tt.stored, entry))
			if got := scopedClineStatus(t, ports.AgentAuthCheck{Args: []string{"cline", "--provider", tt.provider}, Env: tt.env}); got != tt.want {
				t.Fatalf("status = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClineOAuthRequiresSupportedProviderAndAccessToken(t *testing.T) {
	future := strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10)
	tests := []struct {
		name, selected, entries string
		want                    ports.AgentAuthStatus
	}{
		{name: "refresh token alone", selected: "cline", entries: `"cline":{"settings":{"provider":"cline","auth":{"refreshToken":"refresh","expiresAt":` + future + `}},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"oauth"}`, want: ports.AgentAuthStatusUnknown},
		{name: "supported access token", selected: "cline", entries: `"cline":{"settings":{"provider":"cline","auth":{"accessToken":"access","expiresAt":` + future + `}},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"oauth"}`, want: ports.AgentAuthStatusConfigured},
		{name: "unsupported refresh token", selected: "anthropic", entries: `"anthropic":{"settings":{"provider":"anthropic","auth":{"refreshToken":"refresh","expiresAt":` + future + `}},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"oauth"}`, want: ports.AgentAuthStatusUnknown},
		{name: "unsupported OAuth fields", selected: "anthropic", entries: `"anthropic":{"settings":{"provider":"anthropic","auth":{"accessToken":"access","refreshToken":"refresh","expiresAt":1}},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"oauth"}`, want: ports.AgentAuthStatusUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := isolateClineAuth(t)
			writeClineAuthFile(t, filepath.Join(home, ".cline", "data", "settings", "providers.json"), clineProviders(tt.selected, tt.entries))
			if got := scopedClineStatus(t, ports.AgentAuthCheck{}); got != tt.want {
				t.Fatalf("status = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClineRejectsInvalidNativeProviderFileSchema(t *testing.T) {
	tests := []struct {
		name, file string
		env        map[string]string
	}{
		{name: "invalid provider id", file: clineProviders("bad/provider", `"bad/provider":{"settings":{"provider":"bad/provider","apiKey":"test-key"},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}`)},
		{name: "empty token source", file: clineProviders("openai-compatible", `"openai-compatible":{"settings":{"provider":"openai-compatible","apiKey":"test-key"},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":""}`)},
		{name: "null token source", file: clineProviders("openai-compatible", `"openai-compatible":{"settings":{"provider":"openai-compatible","apiKey":"test-key"},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":null}`)},
		{name: "invalid modes", file: `{"version":1,"lastUsedProvider":"openai-compatible","modes":{"voiceInput":{"providerId":"","modelId":"model"}},"providers":{"openai-compatible":{"settings":{"provider":"openai-compatible","apiKey":"test-key"},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}}}`},
		{name: "invalid AWS authentication", file: clineProviders("bedrock", `"bedrock":{"settings":{"provider":"bedrock","aws":{"authentication":"magic"}},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}`), env: map[string]string{"AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test"}},
		{name: "invalid SAP API", file: clineProviders("sapaicore", `"sapaicore":{"settings":{"provider":"sapaicore","sap":{"clientId":"client","clientSecret":"secret","tokenUrl":"https://auth.example.test/token","api":"magic"}},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}`)},
		{name: "invalid protocol", file: clineProviders("openai-compatible", `"openai-compatible":{"settings":{"provider":"openai-compatible","apiKey":"test-key","protocol":"magic"},"updatedAt":"2026-01-01T00:00:00Z","tokenSource":"manual"}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := isolateClineAuth(t)
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			writeClineAuthFile(t, filepath.Join(home, ".cline", "data", "settings", "providers.json"), tt.file)
			if got := scopedClineStatus(t, ports.AgentAuthCheck{}); got != ports.AgentAuthStatusUnknown {
				t.Fatalf("status = %q, want unknown", got)
			}
		})
	}
}
