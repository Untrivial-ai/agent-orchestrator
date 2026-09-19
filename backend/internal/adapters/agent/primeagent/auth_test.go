package primeagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func isolatePrimeAuth(t *testing.T) string {
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

func writePrimeAuth(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func primeStatusForTest(t *testing.T, scope ports.AgentAuthCheck) ports.AgentAuthStatus {
	t.Helper()
	p := &Plugin{resolvedBinary: "prime-agent"}
	var status ports.AgentAuthStatus
	var err error
	if checker, ok := any(p).(ports.AgentScopedAuthChecker); ok {
		status, err = checker.AuthStatusFor(context.Background(), scope)
	} else {
		t.Fatal("Prime must implement scoped authentication")
	}
	if err != nil {
		t.Fatalf("authentication check returned an error: %v", err)
	}
	return status
}

func TestPrimeCredentialEvidence(t *testing.T) {
	tests := []struct {
		name, provider, auth, models string
		env                          map[string]string
		args                         []string
		want                         ports.AgentAuthStatus
	}{
		{name: "absent", want: ports.AgentAuthStatusUnknown},
		{name: "global provider environment", env: map[string]string{"ANTHROPIC_API_KEY": "test-key"}, want: ports.AgentAuthStatusConfigured},
		{name: "selected provider environment", provider: "openai", env: map[string]string{"OPENAI_API_KEY": "test-key"}, want: ports.AgentAuthStatusConfigured},
		{name: "unrelated provider environment", provider: "openai", env: map[string]string{"ANTHROPIC_API_KEY": "test-key"}, want: ports.AgentAuthStatusUnknown},
		{name: "copilot preferred", provider: "github-copilot", env: map[string]string{"COPILOT_GITHUB_TOKEN": "test-key"}, want: ports.AgentAuthStatusConfigured},
		{name: "copilot GH", provider: "github-copilot", env: map[string]string{"GH_TOKEN": "test-key"}, want: ports.AgentAuthStatusConfigured},
		{name: "copilot Github", provider: "github-copilot", env: map[string]string{"GITHUB_TOKEN": "test-key"}, want: ports.AgentAuthStatusConfigured},
		{name: "moonshot", provider: "moonshotai", env: map[string]string{"MOONSHOT_API_KEY": "test-key"}, want: ports.AgentAuthStatusConfigured},
		{name: "moonshot CN", provider: "moonshotai-cn", env: map[string]string{"MOONSHOT_API_KEY": "test-key"}, want: ports.AgentAuthStatusConfigured},
		{name: "unsupported Google key", provider: "google", env: map[string]string{"GOOGLE_API_KEY": "test-key"}, want: ports.AgentAuthStatusUnknown},
		{name: "stored key", provider: "anthropic", auth: `{"anthropic":{"type":"api_key","key":"test-key"}}`, want: ports.AgentAuthStatusConfigured},
		{name: "unrelated stored key", provider: "openai", auth: `{"anthropic":{"type":"api_key","key":"test-key"}}`, want: ports.AgentAuthStatusUnknown},
		{name: "unknown credential type", provider: "anthropic", auth: `{"anthropic":{"type":"other","key":"test-key"}}`, want: ports.AgentAuthStatusUnknown},
		{name: "unrelated recursive key", provider: "anthropic", auth: `{"anthropic":{"metadata":{"apiKey":"test-key"}}}`, want: ports.AgentAuthStatusUnknown},
		{name: "MCP key", auth: `{"mcp:notion":{"type":"api_key","key":"test-key"}}`, want: ports.AgentAuthStatusUnknown},
		{name: "unresolved command", provider: "anthropic", auth: `{"anthropic":{"type":"api_key","key":"!echo test-key"}}`, want: ports.AgentAuthStatusUnknown},
		{name: "unresolved env reference", provider: "anthropic", auth: `{"anthropic":{"type":"api_key","key":"MY_API_KEY"}}`, want: ports.AgentAuthStatusUnknown},
		{name: "resolved env reference", provider: "anthropic", auth: `{"anthropic":{"type":"api_key","key":"MY_API_KEY"}}`, env: map[string]string{"MY_API_KEY": "test-key"}, want: ports.AgentAuthStatusConfigured},
		{name: "fresh OAuth", provider: "openai-codex", auth: `{"openai-codex":{"type":"oauth","access":"test-token","expires":4102444800000}}`, want: ports.AgentAuthStatusConfigured},
		{name: "expired OAuth", provider: "openai-codex", auth: `{"openai-codex":{"type":"oauth","access":"test-token","expires":1}}`, want: ports.AgentAuthStatusUnauthorized},
		{name: "refreshable OAuth", provider: "openai-codex", auth: `{"openai-codex":{"type":"oauth","access":"test-token","refresh":"refresh-token","expires":1}}`, want: ports.AgentAuthStatusConfigured},
		{name: "refresh alone", provider: "openai-codex", auth: `{"openai-codex":{"type":"oauth","refresh":"refresh-token","expires":1}}`, want: ports.AgentAuthStatusUnknown},
		{name: "invalid expiry", provider: "openai-codex", auth: `{"openai-codex":{"type":"oauth","access":"test-token","expires":-1}}`, want: ports.AgentAuthStatusUnknown},
		{name: "unsupported OAuth provider", provider: "openai", auth: `{"openai":{"type":"oauth","access":"test-token","expires":4102444800000}}`, want: ports.AgentAuthStatusUnknown},
		{name: "stored expiry beats unrelated environment", provider: "openai-codex", auth: `{"openai-codex":{"type":"oauth","access":"test-token","expires":1}}`, env: map[string]string{"OPENAI_API_KEY": "test-key"}, want: ports.AgentAuthStatusUnauthorized},
		{name: "runtime key beats expiry", provider: "openai-codex", auth: `{"openai-codex":{"type":"oauth","access":"test-token","expires":1}}`, args: []string{"--api-key", "runtime-key"}, want: ports.AgentAuthStatusConfigured},
		{name: "key flag missing value", provider: "anthropic", args: []string{"--api-key", "--print"}, want: ports.AgentAuthStatusUnknown},
		{name: "prompt cannot supply flags", provider: "anthropic", args: []string{"--", "--api-key", "prompt-key"}, want: ports.AgentAuthStatusUnknown},
		{name: "custom model key", provider: "custom", models: `{"providers":{"custom":{"baseUrl":"https://example.test/v1","apiKey":"test-key","models":[{"id":"test"}]}}}`, want: ports.AgentAuthStatusConfigured},
		{name: "custom model unresolved reference", provider: "custom", models: `{"providers":{"custom":{"apiKey":"CUSTOM_API_KEY"}}}`, want: ports.AgentAuthStatusUnknown},
		{name: "custom model unrelated key", provider: "custom", models: `{"providers":{"unrelated":{"apiKey":"test-key"}}}`, want: ports.AgentAuthStatusUnknown},
		{name: "models recursive metadata", provider: "custom", models: `{"providers":{"custom":{"metadata":{"apiKey":"test-key"}}}}`, want: ports.AgentAuthStatusUnknown},
		{name: "malformed stored JSON falls back to environment", provider: "anthropic", auth: "{", env: map[string]string{"ANTHROPIC_API_KEY": "test-key"}, want: ports.AgentAuthStatusConfigured},
		{name: "bedrock skip auth", provider: "amazon-bedrock", env: map[string]string{"AWS_BEDROCK_SKIP_AUTH": "1"}, want: ports.AgentAuthStatusNotApplicable},
		{name: "bedrock skip auth unrelated", provider: "anthropic", env: map[string]string{"AWS_BEDROCK_SKIP_AUTH": "1"}, want: ports.AgentAuthStatusUnknown},
		{name: "bedrock key pair", provider: "amazon-bedrock", env: map[string]string{"AWS_ACCESS_KEY_ID": "id", "AWS_SECRET_ACCESS_KEY": "secret"}, want: ports.AgentAuthStatusConfigured},
		{name: "bedrock incomplete key pair", provider: "amazon-bedrock", env: map[string]string{"AWS_ACCESS_KEY_ID": "id"}, want: ports.AgentAuthStatusUnknown},
		{name: "Ollama selected", provider: "ollama", models: `{"providers":{"ollama":{"baseUrl":"http://localhost:11434/v1","apiKey":"ollama","models":[{"id":"test"}]}}}`, want: ports.AgentAuthStatusNotApplicable},
		{name: "Ollama placeholder is not a global credential", models: `{"providers":{"ollama":{"baseUrl":"http://localhost:11434/v1","apiKey":"ollama","models":[{"id":"test"}]}}}`, want: ports.AgentAuthStatusUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := isolatePrimeAuth(t)
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			if tt.auth != "" {
				writePrimeAuth(t, filepath.Join(home, ".prime", "agent", "auth.json"), tt.auth)
			}
			if tt.models != "" {
				writePrimeAuth(t, filepath.Join(home, ".prime", "agent", "models.json"), tt.models)
			}
			args := []string{"prime-agent"}
			if tt.provider != "" {
				args = append(args, "--provider", tt.provider)
			}
			args = append(args, tt.args...)
			if got := primeStatusForTest(t, ports.AgentAuthCheck{Args: args}); got != tt.want {
				t.Fatalf("status = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPrimeRuntimeDirectoryAndScopedInputs(t *testing.T) {
	tests := []struct {
		name                                               string
		scopeDir, explicit, legacy, runtimeKey, defaultKey bool
		want                                               ports.AgentAuthStatus
	}{
		{"AO runtime credential", true, false, false, true, false, ports.AgentAuthStatusConfigured},
		{"AO runtime never reads default credential", true, false, false, false, true, ports.AgentAuthStatusUnknown},
		{"explicit native directory", false, true, false, true, false, ports.AgentAuthStatusConfigured},
		{"unsupported PI alias", false, false, true, true, false, ports.AgentAuthStatusUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := isolatePrimeAuth(t)
			data := filepath.Join(home, "ao")
			runtime := filepath.Join(data, "agent-runtime", "prime-agent")
			credential := `{"anthropic":{"type":"api_key","key":"test-key"}}`
			if tt.runtimeKey {
				writePrimeAuth(t, filepath.Join(runtime, "auth.json"), credential)
			}
			if tt.defaultKey {
				writePrimeAuth(t, filepath.Join(home, ".prime", "agent", "auth.json"), credential)
			}
			scope := ports.AgentAuthCheck{Args: []string{"prime-agent", "--provider", "anthropic"}, Env: map[string]string{}}
			if tt.scopeDir {
				scope.DataDir = data
			}
			if tt.explicit {
				scope.Env["PRIME_AGENT_CODING_AGENT_DIR"] = runtime
			}
			if tt.legacy {
				t.Setenv("PI_CODING_AGENT_DIR", runtime)
			}
			if got := primeStatusForTest(t, scope); got != tt.want {
				t.Fatalf("status = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPrimeCloudAndScopedEnvironment(t *testing.T) {
	home := isolatePrimeAuth(t)
	tokenPath := filepath.Join(home, "web-token")
	writePrimeAuth(t, tokenPath, "test-token")
	scope := ports.AgentAuthCheck{Args: []string{"prime-agent", "--provider", "amazon-bedrock"}, Env: map[string]string{"AWS_WEB_IDENTITY_TOKEN_FILE": tokenPath}}
	if got := primeStatusForTest(t, scope); got != ports.AgentAuthStatusConfigured {
		t.Fatalf("web identity without role = %q, want configured", got)
	}
	scope.Env["AWS_WEB_IDENTITY_TOKEN_FILE"] = filepath.Join(home, "missing")
	if got := primeStatusForTest(t, scope); got != ports.AgentAuthStatusUnknown {
		t.Fatalf("missing web identity = %q, want unknown", got)
	}
}

func TestPrimeSelectionFromSettingsAndScopedModel(t *testing.T) {
	home := isolatePrimeAuth(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	writePrimeAuth(t, filepath.Join(home, ".prime", "agent", "settings.json"), `{"defaultProvider":"openai"}`)
	if got := primeStatusForTest(t, ports.AgentAuthCheck{}); got != ports.AgentAuthStatusUnknown {
		t.Fatalf("settings selection = %q, want unknown", got)
	}
	if got := primeStatusForTest(t, ports.AgentAuthCheck{Args: []string{"prime-agent", "--model", "anthropic/test-model"}}); got != ports.AgentAuthStatusConfigured {
		t.Fatalf("model selection = %q, want configured", got)
	}
	t.Setenv("OPENAI_API_KEY", "inherited-key")
	if got := primeStatusForTest(t, ports.AgentAuthCheck{Env: map[string]string{"OPENAI_API_KEY": ""}}); got != ports.AgentAuthStatusUnknown {
		t.Fatalf("cleared environment = %q, want unknown", got)
	}
}

func TestPrimeUnknownModelDoesNotReuseDefaultProvider(t *testing.T) {
	home := isolatePrimeAuth(t)
	t.Setenv("OPENAI_API_KEY", "test-key")
	writePrimeAuth(t, filepath.Join(home, ".prime", "agent", "settings.json"), `{"defaultProvider":"openai"}`)
	if got := primeStatusForTest(t, ports.AgentAuthCheck{Args: []string{"prime-agent", "--model", "unknown/test"}}); got != ports.AgentAuthStatusUnknown {
		t.Fatalf("unknown model status = %q, want unknown", got)
	}
}

func TestPrimeGoogleProjectAliasesAndInjectedEvidence(t *testing.T) {
	for _, key := range []string{"GOOGLE_CLOUD_PROJECT", "GCLOUD_PROJECT", "GOOGLE_CLOUD_PROJECT_ID"} {
		t.Run(key, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, "adc.json")
			writePrimeAuth(t, path, `{"type":"authorized_user","client_id":"id","client_secret":"secret","refresh_token":"refresh"}`)
			env := map[string]string{"HOME": home, key: "project", "GOOGLE_CLOUD_LOCATION": "us-central1", "GOOGLE_APPLICATION_CREDENTIALS": path}
			d := authutil.Dependencies{
				Getenv: func(name string) string { return env[name] },
				Now:    func() time.Time { return time.Unix(2000000000, 0) },
				Run: func(context.Context, string, ...string) ([]byte, error) {
					t.Fatal("unexpected command")
					return nil, nil
				},
				ReadFile: func(path string) ([]byte, error) {
					if !strings.HasPrefix(path, home+string(filepath.Separator)) {
						t.Fatal("read outside temporary root")
					}
					return os.ReadFile(path)
				},
			}
			got, err := primeAuthStatus(context.Background(), ports.AgentAuthCheck{Args: []string{"prime-agent", "--provider", "google-vertex"}}, d)
			want := ports.AgentAuthStatusConfigured
			if key == "GOOGLE_CLOUD_PROJECT_ID" {
				want = ports.AgentAuthStatusUnknown
			}
			if err != nil || got != want {
				t.Fatalf("status = %q, error %v, want %q", got, err, want)
			}
		})
	}
}
