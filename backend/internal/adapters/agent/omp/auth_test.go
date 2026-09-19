package omp

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
)

func isolateOMPAuth(t *testing.T) string {
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
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
func ompStatusForTest(t *testing.T, scope ports.AgentAuthCheck) ports.AgentAuthStatus {
	t.Helper()
	p := &Plugin{resolvedBinary: "omp"}
	var status ports.AgentAuthStatus
	var err error
	if checker, ok := any(p).(ports.AgentScopedAuthChecker); ok {
		status, err = checker.AuthStatusFor(context.Background(), scope)
	} else {
		t.Fatal("OMP must implement scoped authentication")
	}
	if err != nil {
		t.Fatalf("authentication check returned an error: %v", err)
	}
	return status
}

type ompTestRow struct {
	provider, kind, payload string
	disabled                any
}

func writeOMPDatabase(t *testing.T, path string, rows []ompTestRow) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("CREATE TABLE auth_credentials(id INTEGER PRIMARY KEY,provider TEXT NOT NULL,credential_type TEXT NOT NULL,data TEXT NOT NULL,disabled_cause TEXT,identity_key TEXT,created_at INTEGER NOT NULL DEFAULT 1,updated_at INTEGER NOT NULL DEFAULT 1)"); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if _, err = db.Exec("INSERT INTO auth_credentials(provider,credential_type,data,disabled_cause) VALUES(?,?,?,?)", row.provider, row.kind, row.payload, row.disabled); err != nil {
			t.Fatal(err)
		}
	}
}
func TestOMPDatabaseCredentialEvidence(t *testing.T) {
	tests := []struct {
		name string
		rows []ompTestRow
		want ports.AgentAuthStatus
	}{
		{"enabled API key", []ompTestRow{{"anthropic", "api_key", `{"key":"test-key"}`, nil}}, ports.AgentAuthStatusConfigured},
		{"disabled API key", []ompTestRow{{"anthropic", "api_key", `{"key":"test-key"}`, "revoked"}}, ports.AgentAuthStatusUnknown},
		{"empty disabled cause still disabled", []ompTestRow{{"anthropic", "api_key", `{"key":"test-key"}`, ""}}, ports.AgentAuthStatusUnknown},
		{"unsupported type", []ompTestRow{{"anthropic", "other", `{"key":"test-key"}`, nil}}, ports.AgentAuthStatusUnknown},
		{"malformed payload", []ompTestRow{{"anthropic", "api_key", "not-json", nil}}, ports.AgentAuthStatusUnknown},
		{"empty payload", []ompTestRow{{"anthropic", "api_key", `{}`, nil}}, ports.AgentAuthStatusUnknown},
		{"nested unrelated key", []ompTestRow{{"anthropic", "api_key", `{"metadata":{"key":"test-key"}}`, nil}}, ports.AgentAuthStatusUnknown},
		{"nonstring key", []ompTestRow{{"anthropic", "api_key", `{"key":42}`, nil}}, ports.AgentAuthStatusUnknown},
		{"unresolved command", []ompTestRow{{"anthropic", "api_key", `{"key":"!echo test-key"}`, nil}}, ports.AgentAuthStatusUnknown},
		{"unresolved variable", []ompTestRow{{"anthropic", "api_key", `{"key":"MY_API_KEY"}`, nil}}, ports.AgentAuthStatusUnknown},
		{"unrelated provider", []ompTestRow{{"openai", "api_key", `{"key":"test-key"}`, nil}}, ports.AgentAuthStatusUnknown},
		{"fresh OAuth", []ompTestRow{{"anthropic", "oauth", `{"access":"test-token","refresh":"","expires":4102444800000}`, nil}}, ports.AgentAuthStatusConfigured},
		{"expired OAuth", []ompTestRow{{"anthropic", "oauth", `{"access":"test-token","refresh":"","expires":1}`, nil}}, ports.AgentAuthStatusUnauthorized},
		{"refreshable OAuth", []ompTestRow{{"anthropic", "oauth", `{"access":"test-token","refresh":"refresh-token","expires":1}`, nil}}, ports.AgentAuthStatusConfigured},
		{"refresh alone", []ompTestRow{{"anthropic", "oauth", `{"refresh":"refresh-token","expires":1}`, nil}}, ports.AgentAuthStatusUnknown},
		{"invalid expiry", []ompTestRow{{"anthropic", "oauth", `{"access":"test-token","refresh":"","expires":-1}`, nil}}, ports.AgentAuthStatusUnknown},
		{"valid sibling after malformed", []ompTestRow{{"anthropic", "api_key", "{", nil}, {"anthropic", "api_key", `{"key":"test-key"}`, nil}}, ports.AgentAuthStatusConfigured},
		{"usable sibling after expired", []ompTestRow{{"anthropic", "oauth", `{"access":"test-token","refresh":"","expires":1}`, nil}, {"anthropic", "api_key", `{"key":"test-key"}`, nil}}, ports.AgentAuthStatusConfigured},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := isolateOMPAuth(t)
			path := filepath.Join(home, ".omp", "agent", "agent.db")
			writeOMPDatabase(t, path, tt.rows)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := ompStatusForTest(t, ports.AgentAuthCheck{Args: []string{"omp", "--provider", "anthropic"}}); got != tt.want {
				t.Fatalf("status = %q, want %q", got, tt.want)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("auth check changed credential database")
			}
		})
	}
}
func TestOMPFileAndEnvironmentEvidence(t *testing.T) {
	tests := []struct {
		name, provider, config, models, auth string
		env                                  map[string]string
		args                                 []string
		files                                map[string]string
		want                                 ports.AgentAuthStatus
	}{
		{name: "absent", want: ports.AgentAuthStatusUnknown},
		{name: "provider environment", provider: "anthropic", env: map[string]string{"ANTHROPIC_API_KEY": "test-key"}, want: ports.AgentAuthStatusConfigured},
		{name: "scoped environment", provider: "openai", env: map[string]string{"OPENAI_API_KEY": "test-key"}, want: ports.AgentAuthStatusConfigured},
		{name: "unrelated environment", provider: "anthropic", env: map[string]string{"OPENAI_API_KEY": "test-key"}, want: ports.AgentAuthStatusUnknown},
		{name: "project dotenv", provider: "anthropic", files: map[string]string{"work/.env": "export ANTHROPIC_API_KEY='test-key' # comment\n"}, want: ports.AgentAuthStatusConfigured},
		{name: "agent dotenv", provider: "anthropic", files: map[string]string{".omp/agent/.env": "ANTHROPIC_API_KEY=test-key\n"}, want: ports.AgentAuthStatusConfigured},
		{name: "dotenv wrong provider", provider: "anthropic", files: map[string]string{"work/.env": "OPENAI_API_KEY=test-key\n"}, want: ports.AgentAuthStatusUnknown},
		{name: "models static key", provider: "custom", models: "providers:\n  custom:\n    baseUrl: https://example.test/v1\n    apiKey: test-key\n", want: ports.AgentAuthStatusConfigured},
		{name: "models environment reference", provider: "custom", models: "providers:\n  custom:\n    apiKey: CUSTOM_API_KEY\n", env: map[string]string{"CUSTOM_API_KEY": "test-key"}, want: ports.AgentAuthStatusConfigured},
		{name: "models unresolved reference", provider: "custom", models: "providers:\n  custom:\n    apiKey: CUSTOM_API_KEY\n", want: ports.AgentAuthStatusUnknown},
		{name: "models command unresolved", provider: "custom", models: "providers:\n  custom:\n    apiKey: '!echo test-key'\n", want: ports.AgentAuthStatusUnknown},
		{name: "models unrelated", provider: "anthropic", models: "providers:\n  openai:\n    apiKey: test-key\n", want: ports.AgentAuthStatusUnknown},
		{name: "no auth selected", provider: "local", models: "providers:\n  local:\n    baseUrl: http://localhost:1234/v1\n    auth: none\n", want: ports.AgentAuthStatusNotApplicable},
		{name: "no auth unselected", provider: "anthropic", models: "providers:\n  local:\n    auth: none\n", want: ports.AgentAuthStatusUnknown},
		{name: "no auth placeholder is not a global credential", models: "providers:\n  local:\n    auth: none\n    apiKey: placeholder\n", want: ports.AgentAuthStatusUnknown},
		{name: "disabled selected provider", provider: "anthropic", config: "disabledProviders: [anthropic]\n", env: map[string]string{"ANTHROPIC_API_KEY": "test-key"}, want: ports.AgentAuthStatusUnknown},
		{name: "settings model role", config: "modelRoles:\n  default: openai/gpt-test\n", env: map[string]string{"ANTHROPIC_API_KEY": "test-key"}, want: ports.AgentAuthStatusUnknown},
		{name: "runtime API key", provider: "anthropic", args: []string{"--api-key", "runtime-key"}, want: ports.AgentAuthStatusConfigured},
		{name: "flag-shaped runtime key is a native string value", provider: "anthropic", args: []string{"--api-key", "--print"}, want: ports.AgentAuthStatusConfigured},
		{name: "prompt flags ignored", provider: "anthropic", args: []string{"--", "--api-key", "prompt-key"}, want: ports.AgentAuthStatusUnknown},
		{name: "fallback auth JSON", provider: "anthropic", auth: `{"anthropic":{"type":"api_key","key":"test-key"}}`, want: ports.AgentAuthStatusConfigured},
		{name: "fallback wrong type", provider: "anthropic", auth: `{"anthropic":{"type":"other","key":"test-key"}}`, want: ports.AgentAuthStatusUnknown},
		{name: "fallback unrelated provider", provider: "anthropic", auth: `{"openai":{"type":"api_key","key":"test-key"}}`, want: ports.AgentAuthStatusUnknown},
		{name: "malformed YAML unknown", provider: "custom", models: "providers: [", want: ports.AgentAuthStatusUnknown},
		{name: "malformed fallback unknown", provider: "anthropic", auth: "{", want: ports.AgentAuthStatusUnknown},
		{name: "broker global chain", env: map[string]string{"OMP_AUTH_BROKER_URL": "https://broker.example.test", "OMP_AUTH_BROKER_TOKEN": "broker-token"}, want: ports.AgentAuthStatusConfigured},
		{name: "broker URL alone", env: map[string]string{"OMP_AUTH_BROKER_URL": "https://broker.example.test"}, want: ports.AgentAuthStatusUnknown},
		{name: "selected broker requires provider evidence", provider: "anthropic", env: map[string]string{"OMP_AUTH_BROKER_URL": "https://broker.example.test", "OMP_AUTH_BROKER_TOKEN": "broker-token"}, want: ports.AgentAuthStatusUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := isolateOMPAuth(t)
			for name, content := range tt.files {
				writeFile(t, filepath.Join(home, name), content)
			}
			for name, content := range map[string]string{"config.yml": tt.config, "models.yml": tt.models, "auth.json": tt.auth} {
				if content != "" {
					writeFile(t, filepath.Join(home, ".omp", "agent", name), content)
				}
			}
			args := []string{"omp"}
			if tt.provider != "" {
				args = append(args, "--provider", tt.provider)
			}
			args = append(args, tt.args...)
			if got := ompStatusForTest(t, ports.AgentAuthCheck{WorkingDir: filepath.Join(home, "work"), Args: args, Env: tt.env}); got != tt.want {
				t.Fatalf("status = %q, want %q", got, tt.want)
			}
		})
	}
}
func TestOMPBrokerEncryptedCache(t *testing.T) {
	tests := []struct {
		name, provider, token, url string
		age                        time.Duration
		want                       ports.AgentAuthStatus
	}{
		{"matching", "anthropic", "broker-token", "https://broker.example.test", 0, ports.AgentAuthStatusConfigured},
		{"unrelated provider", "openai", "broker-token", "https://broker.example.test", 0, ports.AgentAuthStatusUnknown},
		{"wrong token", "anthropic", "wrong-token", "https://broker.example.test", 0, ports.AgentAuthStatusUnknown},
		{"wrong URL", "anthropic", "broker-token", "https://other.example.test", 0, ports.AgentAuthStatusUnknown},
		{"expired cache", "anthropic", "broker-token", "https://broker.example.test", 2 * time.Hour, ports.AgentAuthStatusUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := isolateOMPAuth(t)
			now := time.Now().UnixMilli()
			payload, err := json.Marshal(map[string]any{"generation": 1, "generatedAt": now - tt.age.Milliseconds(), "serverNowMs": now, "refresher": map[string]any{"enabled": true, "intervalMs": 60000, "skewMs": 300000, "nextSweepInMs": 0}, "credentials": []any{map[string]any{"id": 1, "provider": "anthropic", "identityKey": nil, "rotatesInMs": nil, "credential": map[string]any{"type": "api_key", "key": "test-key"}}}})
			if err != nil {
				t.Fatal(err)
			}
			key := sha256.Sum256([]byte("broker-token"))
			block, err := aes.NewCipher(key[:])
			if err != nil {
				t.Fatal(err)
			}
			gcm, err := cipher.NewGCM(block)
			if err != nil {
				t.Fatal(err)
			}
			header := []byte{'O', 'M', 'P', 'S', 2}
			nonce := make([]byte, 12)
			additional := append(append([]byte{}, header...), []byte("https://broker.example.test")...)
			data := append(append(header, nonce...), gcm.Seal(nil, nonce, payload, additional)...)
			path := filepath.Join(home, ".omp", "cache", "auth-broker-snapshot.enc")
			writeFile(t, path, string(data))
			env := map[string]string{"OMP_AUTH_BROKER_URL": tt.url, "OMP_AUTH_BROKER_TOKEN": tt.token}
			if got := ompStatusForTest(t, ports.AgentAuthCheck{Args: []string{"omp", "--provider", tt.provider}, Env: env}); got != tt.want {
				t.Fatalf("status = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOMPCatalogEnvironmentEvidence(t *testing.T) {
	tests := []struct {
		provider, key string
		want          ports.AgentAuthStatus
	}{
		{"moonshot", "MOONSHOT_API_KEY", ports.AgentAuthStatusConfigured},
		{"moonshot", "KIMI_API_KEY", ports.AgentAuthStatusConfigured},
		{"huggingface", "HUGGINGFACE_HUB_TOKEN", ports.AgentAuthStatusConfigured},
		{"coreweave", "WANDB_API_KEY", ports.AgentAuthStatusConfigured},
		{"fireworks", "FIREWORKS_API_KEY", ports.AgentAuthStatusConfigured},
		{"minimax-code", "MINIMAX_CODE_API_KEY", ports.AgentAuthStatusConfigured},
		{"cloudflare-ai-gateway", "CLOUDFLARE_AI_GATEWAY_API_KEY", ports.AgentAuthStatusConfigured},
		{"openai-codex", "OPENAI_CODEX_OAUTH_TOKEN", ports.AgentAuthStatusConfigured},
		{"github-copilot", "GH_TOKEN", ports.AgentAuthStatusUnknown},
		{"cloudflare-ai-gateway", "CLOUDFLARE_ACCOUNT_ID", ports.AgentAuthStatusUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.provider+"/"+tt.key, func(t *testing.T) {
			isolateOMPAuth(t)
			if got := ompStatusForTest(t, ports.AgentAuthCheck{Args: []string{"omp", "--provider", tt.provider}, Env: map[string]string{tt.key: "test-key"}}); got != tt.want {
				t.Fatalf("status = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOMPPrecedenceAndTypedConfiguration(t *testing.T) {
	tests := []struct {
		name, models, config string
		env                  map[string]string
		want                 ports.AgentAuthStatus
	}{
		{"unresolved model key blocks database", "providers:\n  anthropic:\n    apiKey: MISSING_KEY\n", "", nil, ports.AgentAuthStatusUnknown},
		{"disabled provider blocks database", "", "disabledProviders: [anthropic]\n", nil, ports.AgentAuthStatusUnknown},
		{"invalid auth mode blocks key", "providers:\n  anthropic:\n    auth: invalid\n    apiKey: test-key\n", "", nil, ports.AgentAuthStatusUnknown},
		{"broker replaces local database", "", "", map[string]string{"OMP_AUTH_BROKER_URL": "https://broker.example.test", "OMP_AUTH_BROKER_TOKEN": "broker-token"}, ports.AgentAuthStatusUnknown},
		{"malformed model key is ignored", "providers:\n  anthropic:\n    apiKey: 42\n", "", nil, ports.AgentAuthStatusConfigured},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := isolateOMPAuth(t)
			dir := filepath.Join(home, "custom-agent")
			writeOMPDatabase(t, filepath.Join(dir, "agent.db"), []ompTestRow{{"anthropic", "api_key", `{"key":"test-key"}`, nil}})
			if tt.models != "" {
				writeFile(t, filepath.Join(dir, "models.yml"), tt.models)
			}
			if tt.config != "" {
				writeFile(t, filepath.Join(dir, "config.yml"), tt.config)
			}
			scope := ports.AgentAuthCheck{Args: []string{"omp", "--provider", "anthropic"}, Env: map[string]string{"PI_CODING_AGENT_DIR": dir}}
			for key, value := range tt.env {
				scope.Env[key] = value
			}
			if got := ompStatusForTest(t, scope); got != tt.want {
				t.Fatalf("status = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOMPInjectedReadOnlyDatabaseAndNoCommands(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".omp", "agent", "agent.db")
	writeOMPDatabase(t, path, []ompTestRow{{"anthropic", "api_key", `{"key":"test-key"}`, nil}})
	opened := false
	d := ompAuthDependencies{Dependencies: authutil.Dependencies{
		Getenv: func(name string) string {
			if name == "HOME" {
				return home
			}
			return ""
		},
		Run: func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("unexpected command")
			return nil, nil
		},
		Lstat: func(path string) (os.FileInfo, error) {
			if !strings.HasPrefix(path, home+string(filepath.Separator)) {
				t.Fatal("file probe outside temporary root")
			}
			return os.Lstat(path)
		},
	}, OpenDB: func(dsn string) (*sql.DB, error) {
		opened = true
		if !strings.Contains(dsn, "mode=ro") || !strings.Contains(dsn, "query_only") {
			t.Fatal("database not opened read-only")
		}
		return sql.Open("sqlite", dsn)
	}}
	got, err := ompAuthStatus(context.Background(), ports.AgentAuthCheck{Args: []string{"omp", "--provider", "anthropic"}}, d)
	if err != nil || got != ports.AgentAuthStatusConfigured || !opened {
		t.Fatalf("status = %q, error %v, database opened %v", got, err, opened)
	}
}

func TestOMPCloudCredentialEvidence(t *testing.T) {
	tests := []struct {
		name, provider string
		env            map[string]string
		want           ports.AgentAuthStatus
	}{
		{"bedrock pair", "amazon-bedrock", map[string]string{"AWS_ACCESS_KEY_ID": "id", "AWS_SECRET_ACCESS_KEY": "secret"}, ports.AgentAuthStatusConfigured},
		{"bedrock skip", "amazon-bedrock", map[string]string{"AWS_BEDROCK_SKIP_AUTH": "1"}, ports.AgentAuthStatusNotApplicable},
		{"bedrock incomplete", "amazon-bedrock", map[string]string{"AWS_ACCESS_KEY_ID": "id"}, ports.AgentAuthStatusUnknown},
		{"vertex ADC", "google-vertex", map[string]string{"GCP_PROJECT": "project", "GOOGLE_VERTEX_LOCATION": "us-central1"}, ports.AgentAuthStatusConfigured},
		{"vertex missing project", "google-vertex", map[string]string{"GOOGLE_VERTEX_LOCATION": "us-central1"}, ports.AgentAuthStatusUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, "adc.json")
			writeFile(t, path, `{"type":"authorized_user","client_id":"id","client_secret":"secret","refresh_token":"refresh"}`)
			env := map[string]string{"HOME": home, "GOOGLE_APPLICATION_CREDENTIALS": path}
			for key, value := range tt.env {
				env[key] = value
			}
			d := ompAuthDependencies{Dependencies: authutil.Dependencies{
				Getenv: func(key string) string { return env[key] },
				Run: func(context.Context, string, ...string) ([]byte, error) {
					t.Fatal("unexpected command")
					return nil, nil
				},
			}}
			got, err := ompAuthStatus(context.Background(), ports.AgentAuthCheck{Args: []string{"omp", "--provider", tt.provider}}, d)
			if err != nil || got != tt.want {
				t.Fatalf("status = %q, error %v, want %q", got, err, tt.want)
			}
		})
	}
}

func TestOMPBrokerConfigurationAndScopedEnvironment(t *testing.T) {
	home := isolateOMPAuth(t)
	writeFile(t, filepath.Join(home, ".omp", "agent", "config.yaml"), "auth:\n  broker:\n    url: https://broker.example.test\n    token: BROKER_TOKEN\n")
	t.Setenv("BROKER_TOKEN", "broker-token")
	if got := ompStatusForTest(t, ports.AgentAuthCheck{}); got != ports.AgentAuthStatusConfigured {
		t.Fatalf("configured broker = %q", got)
	}
	writeFile(t, filepath.Join(home, ".omp", "agent", "config.yaml"), "")
	t.Setenv("OPENAI_API_KEY", "inherited-key")
	if got := ompStatusForTest(t, ports.AgentAuthCheck{Args: []string{"omp", "--model", "openai/test"}, Env: map[string]string{"OPENAI_API_KEY": ""}}); got != ports.AgentAuthStatusUnknown {
		t.Fatalf("cleared selected env = %q", got)
	}
}

func TestOMPNativeOAuthProviderRows(t *testing.T) {
	for _, provider := range []string{"kilo", "perplexity", "alibaba-coding-plan", "alibaba-token-plan", "cloudflare-ai-gateway", "xiaomi", "openai-codex-device"} {
		t.Run(provider, func(t *testing.T) {
			home := isolateOMPAuth(t)
			writeOMPDatabase(t, filepath.Join(home, ".omp", "agent", "agent.db"), []ompTestRow{{provider, "oauth", `{"access":"test-token","refresh":"","expires":4102444800000}`, nil}})
			if got := ompStatusForTest(t, ports.AgentAuthCheck{Args: []string{"omp", "--provider", provider}}); got != ports.AgentAuthStatusConfigured {
				t.Fatalf("status = %q, want configured", got)
			}
		})
	}
}

func TestOMPUnresolvedNativeOverridesDoNotReadDefaultCredentials(t *testing.T) {
	for _, key := range []string{"OMP_PROFILE", "PI_PROFILE", "PI_CONFIG_FILES"} {
		t.Run(key, func(t *testing.T) {
			home := isolateOMPAuth(t)
			writeFile(t, filepath.Join(home, ".omp", "agent", "auth.json"), `{"anthropic":{"type":"api_key","key":"default-key"}}`)
			if got := ompStatusForTest(t, ports.AgentAuthCheck{Args: []string{"omp", "--provider", "anthropic"}, Env: map[string]string{key: filepath.Join(home, "override")}}); got != ports.AgentAuthStatusUnknown {
				t.Fatalf("status = %q, want unknown for unresolved override", got)
			}
		})
	}
	for _, flag := range []string{"--config", "--profile"} {
		t.Run(flag, func(t *testing.T) {
			home := isolateOMPAuth(t)
			writeFile(t, filepath.Join(home, ".omp", "agent", "auth.json"), `{"anthropic":{"type":"api_key","key":"default-key"}}`)
			if got := ompStatusForTest(t, ports.AgentAuthCheck{Args: []string{"omp", "--provider", "anthropic", flag, "override"}}); got != ports.AgentAuthStatusUnknown {
				t.Fatalf("status = %q, want unknown for unresolved override", got)
			}
		})
	}
	t.Run("profile after extension-shadowable plan", func(t *testing.T) {
		home := isolateOMPAuth(t)
		writeFile(t, filepath.Join(home, ".omp", "agent", "auth.json"), `{"anthropic":{"type":"api_key","key":"default-key"}}`)
		if got := ompStatusForTest(t, ports.AgentAuthCheck{Args: []string{"--provider", "anthropic", "--plan", "--profile", "work"}}); got != ports.AgentAuthStatusUnknown {
			t.Fatalf("status = %q, want unknown for unresolved plan/profile routing", got)
		}
	})
}

func TestOMPStringOptionValuesCannotBecomeAuthOrRoutingFlags(t *testing.T) {
	for _, option := range []string{"--system-prompt", "--append-system-prompt", "--provider-session-id", "--prompt-cache-key", "--session-dir", "--skills", "--extension", "-e"} {
		for _, tt := range []struct {
			name string
			args []string
			env  map[string]string
			want ports.AgentAuthStatus
		}{
			{"split API key", []string{option, "--api-key", "prompt-key"}, nil, ports.AgentAuthStatusUnknown},
			{"inline API key", []string{option, "--api-key=prompt-key"}, nil, ports.AgentAuthStatusUnknown},
			{"provider text", []string{option, "--provider", "openai"}, map[string]string{"OPENAI_API_KEY": "unrelated-key"}, ports.AgentAuthStatusUnknown},
			{"model text", []string{option, "--model=openai/test"}, map[string]string{"ANTHROPIC_API_KEY": "test-key"}, ports.AgentAuthStatusConfigured},
			{"profile text", []string{option, "--profile", "work"}, map[string]string{"ANTHROPIC_API_KEY": "test-key"}, ports.AgentAuthStatusConfigured},
			{"config text", []string{option, "--config=other.yml"}, map[string]string{"ANTHROPIC_API_KEY": "test-key"}, ports.AgentAuthStatusConfigured},
			{"real flag after consumed value", []string{option, "--api-key", "--api-key", "real-key"}, nil, ports.AgentAuthStatusConfigured},
		} {
			t.Run(option+"/"+tt.name, func(t *testing.T) {
				home := isolateOMPAuth(t)
				writeFile(t, filepath.Join(home, ".omp", "agent", "config.yml"), "modelRoles:\n  default: anthropic/test\n")
				if got := ompStatusForTest(t, ports.AgentAuthCheck{Args: tt.args, Env: tt.env}); got != tt.want {
					t.Fatalf("status = %q, want %q", got, tt.want)
				}
			})
		}
	}
}

func TestOMPProjectModelRolesOverrideGlobalRoles(t *testing.T) {
	for _, tt := range []struct {
		name, global, project, model string
		args                         []string
		want                         ports.AgentAuthStatus
	}{
		{"project rejects unrelated global credential", "modelRoles: {default: anthropic/test}", "modelRoles: {default: openai/test}", "", nil, ports.AgentAuthStatusUnknown},
		{"project selects matching credential", "modelRoles: {default: openai/test}", "modelRoles: {default: anthropic/test}", "", nil, ports.AgentAuthStatusConfigured},
		{"project list rejects unrelated global credential", "modelRoles: {default: anthropic/test}", "modelRoles: {default: [openai/test]}", "", nil, ports.AgentAuthStatusUnknown},
		{"project list selects matching credential", "modelRoles: {default: openai/test}", "modelRoles: {default: [anthropic/test]}", "", nil, ports.AgentAuthStatusConfigured},
		{"unresolved project list does not use global credential", "modelRoles: {default: anthropic/test}", "modelRoles: {default: [unresolved-model]}", "", nil, ports.AgentAuthStatusUnknown},
		{"multiple project selectors remain unknown", "modelRoles: {default: anthropic/test}", "modelRoles: {default: [anthropic/test, openai/test]}", "", nil, ports.AgentAuthStatusUnknown},
		{"global list rejects unrelated credential", "modelRoles: {default: [openai/test]}", "{}", "", nil, ports.AgentAuthStatusUnknown},
		{"other project role retains global default", "modelRoles: {default: openai/test}", "modelRoles: {smol: anthropic/test}", "", nil, ports.AgentAuthStatusUnknown},
		{"null project role retains global default", "modelRoles: {default: openai/test}", "modelRoles: {default: null}", "", nil, ports.AgentAuthStatusUnknown},
		{"global disabled providers retained", "disabledProviders: [anthropic]\nmodelRoles: {default: openai/test}", "modelRoles: {default: anthropic/test}", "", nil, ports.AgentAuthStatusUnknown},
		{"scoped model wins over project", "modelRoles: {default: anthropic/test}", "modelRoles: {default: anthropic/test}", "openai/test", nil, ports.AgentAuthStatusUnknown},
		{"argument model wins over project", "modelRoles: {default: anthropic/test}", "modelRoles: {default: anthropic/test}", "", []string{"--model", "openai/test"}, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := isolateOMPAuth(t)
			work := filepath.Join(home, "project")
			writeFile(t, filepath.Join(home, ".omp", "agent", "config.yml"), tt.global)
			writeFile(t, filepath.Join(work, ".omp", "config.yml"), tt.project)
			writeFile(t, filepath.Join(home, ".omp", "agent", "auth.json"), `{"anthropic":{"type":"api_key","key":"test-key"}}`)
			if got := ompStatusForTest(t, ports.AgentAuthCheck{WorkingDir: work, Config: ports.AgentConfig{Model: tt.model}, Args: tt.args}); got != tt.want {
				t.Fatalf("status = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOMPOAuthRequiresPresentStringRefresh(t *testing.T) {
	for _, source := range []string{"database", "fallback"} {
		for _, tt := range []struct {
			name, refresh string
			want          ports.AgentAuthStatus
		}{
			{"missing", "", ports.AgentAuthStatusUnknown},
			{"null", `,"refresh":null`, ports.AgentAuthStatusUnknown},
			{"number", `,"refresh":42`, ports.AgentAuthStatusUnknown},
			{"object", `,"refresh":{}`, ports.AgentAuthStatusUnknown},
			{"empty string", `,"refresh":""`, ports.AgentAuthStatusConfigured},
		} {
			t.Run(source+"/"+tt.name, func(t *testing.T) {
				home := isolateOMPAuth(t)
				payload := `{"type":"oauth","access":"test-token","expires":4102444800000` + tt.refresh + `}`
				if source == "database" {
					writeOMPDatabase(t, filepath.Join(home, ".omp", "agent", "agent.db"), []ompTestRow{{"anthropic", "oauth", payload, nil}})
				} else {
					writeFile(t, filepath.Join(home, ".omp", "agent", "auth.json"), `{"anthropic":`+payload+`}`)
				}
				if got := ompStatusForTest(t, ports.AgentAuthCheck{Args: []string{"--provider", "anthropic"}}); got != tt.want {
					t.Fatalf("status = %q, want %q", got, tt.want)
				}
			})
		}
	}
}

func TestOMPDefaultAndUnaffectedRoutingStillReadsCredentials(t *testing.T) {
	for _, tt := range []struct {
		name string
		env  map[string]string
		args []string
	}{
		{"canonical default", map[string]string{"OMP_PROFILE": "default"}, nil},
		{"legacy default", map[string]string{"PI_PROFILE": "default"}, nil},
		{"canonical default overrides legacy", map[string]string{"OMP_PROFILE": "default", "PI_PROFILE": "work"}, nil},
		{"canonical empty overrides legacy", map[string]string{"OMP_PROFILE": "", "PI_PROFILE": "work"}, nil},
		{"argument default", nil, []string{"--profile", "default"}},
		{"argument default overrides environment", map[string]string{"OMP_PROFILE": "work"}, []string{"--profile=default"}},
		{"unused config root", map[string]string{"XDG_CONFIG_HOME": "xdg"}, nil},
		{"unmigrated data root", map[string]string{"XDG_DATA_HOME": "xdg"}, nil},
		{"unmigrated cache root", map[string]string{"XDG_CACHE_HOME": "xdg"}, nil},
		{"state does not route credentials", map[string]string{"XDG_STATE_HOME": "xdg"}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := isolateOMPAuth(t)
			env := make(map[string]string)
			for key, value := range tt.env {
				if strings.HasPrefix(key, "XDG_") {
					value = filepath.Join(home, value)
				}
				env[key] = value
			}
			writeFile(t, filepath.Join(home, ".omp", "agent", "auth.json"), `{"anthropic":{"type":"api_key","key":"default-key"}}`)
			args := append([]string{"--provider", "anthropic"}, tt.args...)
			if got := ompStatusForTest(t, ports.AgentAuthCheck{Args: args, Env: env}); got != ports.AgentAuthStatusConfigured {
				t.Fatalf("status = %q, want configured", got)
			}
		})
	}
}

func TestOMPBrokerAccountPoolRequiresResolution(t *testing.T) {
	home := isolateOMPAuth(t)
	env := map[string]string{"OMP_AUTH_BROKER_URL": "https://broker.example.test", "OMP_AUTH_BROKER_TOKEN": "broker-token", "OMP_AUTH_BROKER_ACCOUNT_POOL_FILE": filepath.Join(home, "pool.json")}
	if got := ompStatusForTest(t, ports.AgentAuthCheck{Env: env}); got != ports.AgentAuthStatusUnknown {
		t.Fatalf("unresolved account pool = %q, want unknown", got)
	}
}

func TestOMPXDGDatabaseRouting(t *testing.T) {
	for _, tt := range []struct {
		name, platform string
		custom         bool
		defaultKey     bool
		want           ports.AgentAuthStatus
	}{
		{"migrated data rejects old database", "linux", false, true, ports.AgentAuthStatusUnknown},
		{"migrated data uses selected database", "linux", false, false, ports.AgentAuthStatusConfigured},
		{"macOS uses migrated data", "darwin", false, false, ports.AgentAuthStatusConfigured},
		{"Windows ignores XDG", "windows", false, true, ports.AgentAuthStatusConfigured},
		{"custom agent directory ignores XDG", "linux", true, true, ports.AgentAuthStatusConfigured},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			dir := filepath.Join(home, ".omp", "agent")
			if tt.custom {
				dir = filepath.Join(home, "custom")
			}
			defaultRows, routedRows := []ompTestRow{}, []ompTestRow{}
			key := ompTestRow{"anthropic", "api_key", `{"key":"test-key"}`, nil}
			if tt.defaultKey {
				defaultRows = append(defaultRows, key)
			} else {
				routedRows = append(routedRows, key)
			}
			writeOMPDatabase(t, filepath.Join(dir, "agent.db"), defaultRows)
			writeOMPDatabase(t, filepath.Join(home, "xdg", "omp", "agent.db"), routedRows)
			env := map[string]string{"HOME": home, "XDG_DATA_HOME": filepath.Join(home, "xdg")}
			if tt.custom {
				env["PI_CODING_AGENT_DIR"] = dir
			}
			d := ompAuthDependencies{Dependencies: authutil.Dependencies{Getenv: func(key string) string { return env[key] }, GOOS: tt.platform}}
			got, err := ompAuthStatus(context.Background(), ports.AgentAuthCheck{Args: []string{"--provider", "anthropic"}}, d)
			if err != nil || got != tt.want {
				t.Fatalf("status = %q, error %v, want %q", got, err, tt.want)
			}
		})
	}
}
