package goose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestGooseLocalAuthStatusReadsSecretsFile(t *testing.T) {
	gooseTestHome(t)
	t.Setenv("GOOSE_PROVIDER", "openai")
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := os.MkdirAll(filepath.Join(configHome, "goose"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "goose", "secrets.yaml"), []byte("OPENAI_API_KEY: test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	status, ok, err := gooseLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusConfigured)
	}
}

func gooseDeps(t *testing.T, env map[string]string) authutil.Dependencies {
	t.Helper()
	if env == nil {
		env = map[string]string{}
	}
	if env["HOME"] == "" {
		env["HOME"] = t.TempDir()
	}
	return authutil.Dependencies{Getenv: func(k string) string { return env[k] }, GOOS: "linux", Now: func() time.Time { return time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC) }, Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("unexpected credential command")
		return nil, nil
	}}
}

func TestGooseProviderRootsAndMetadata(t *testing.T) {
	for _, tc := range []struct{ name, provider, key string }{
		{"Google metadata", "google", "GOOGLE_API_KEY"},
		{"non API key name", "iflytek", "SPARK_API_PASSWORD"},
		{"missing old catalog coverage", "cerebras", "CEREBRAS_API_KEY"},
		{"Fireworks metadata", "fireworks-ai", "FIREWORKS_API_KEY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := gooseDeps(t, map[string]string{"GOOSE_PROVIDER": tc.provider, tc.key: "fixture-key"})
			got, err := gooseAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
			if err != nil || got != ports.AgentAuthStatusConfigured {
				t.Fatalf("got %q, %v; want configured", got, err)
			}
		})
	}
	for _, mode := range []string{"override", "windows"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			env := map[string]string{"GOOSE_PROVIDER": "openai", "GOOSE_DISABLE_KEYRING": "1"}
			dir := filepath.Join(root, "config")
			if mode == "override" {
				env["GOOSE_PATH_ROOT"] = root
			} else {
				env["APPDATA"] = root
				dir = filepath.Join(root, "Block", "goose", "config")
			}
			d := gooseDeps(t, env)
			if mode == "windows" {
				d.GOOS = "windows"
			}
			gooseWrite(t, filepath.Join(dir, "secrets.yaml"), "OPENAI_API_KEY: fixture-key\n")
			got, err := gooseAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
			if err != nil || got != ports.AgentAuthStatusConfigured {
				t.Fatalf("got %q, %v; want configured", got, err)
			}
		})
	}
}

func TestGooseScopedProviderPrecedence(t *testing.T) {
	d := gooseDeps(t, map[string]string{"GOOSE_PROVIDER": "openai", "OPENAI_API_KEY": "fixture-key"})
	for _, tc := range []struct {
		name  string
		check ports.AgentAuthCheck
		want  ports.AgentAuthStatus
	}{
		{"scoped env", ports.AgentAuthCheck{Env: map[string]string{"GOOSE_PROVIDER": "anthropic"}}, ports.AgentAuthStatusUnknown},
		{"CLI provider", ports.AgentAuthCheck{Args: []string{"goose", "run", "--provider", "ollama"}}, ports.AgentAuthStatusNotApplicable},
		{"equals provider", ports.AgentAuthCheck{Env: map[string]string{"GOOSE_PROVIDER": "anthropic"}, Args: []string{"goose", "run", "--provider=lmstudio"}}, ports.AgentAuthStatusNotApplicable},
		{"prompt is not flags", ports.AgentAuthCheck{Args: []string{"goose", "run", "--", "--provider", "ollama"}}, ports.AgentAuthStatusConfigured},
		{"text argument is not flags", ports.AgentAuthCheck{Args: []string{"goose", "run", "--text", "--provider=ollama"}}, ports.AgentAuthStatusConfigured},
		{"system argument is not flags", ports.AgentAuthCheck{Args: []string{"goose", "run", "--system", "--provider=ollama"}}, ports.AgentAuthStatusConfigured},
		{"empty scoped key", ports.AgentAuthCheck{Env: map[string]string{"OPENAI_API_KEY": ""}}, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := gooseAuthStatus(context.Background(), tc.check, d)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestGooseCustomProviderMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, metadata string
		env            map[string]string
		want           ports.AgentAuthStatus
	}{
		{"declared env", `{"name":"custom_team","engine":"openai","base_url":"https://models.example/v1","api_key_env":"TEAM_AUTH","models":[]}`, map[string]string{"TEAM_AUTH": "fixture"}, ports.AgentAuthStatusConfigured},
		{"unrelated env", `{"name":"custom_team","engine":"openai","base_url":"https://models.example/v1","api_key_env":"TEAM_AUTH","models":[]}`, map[string]string{"OPENAI_API_KEY": "fixture"}, ports.AgentAuthStatusUnknown},
		{"command config not execution", `{"name":"custom_team","engine":"openai","base_url":"https://models.example/v1","auth":{"command":"credential-helper","args":["--secret"]},"models":[]}`, nil, ports.AgentAuthStatusConfigured},
		{"command and key conflict", `{"name":"custom_team","engine":"openai","base_url":"https://models.example/v1","api_key_env":"TEAM_AUTH","auth":{"command":"helper"},"models":[]}`, map[string]string{"TEAM_AUTH": "fixture"}, ports.AgentAuthStatusUnknown},
		{"explicit no auth", `{"name":"custom_team","engine":"openai","base_url":"http://localhost:9000/v1","requires_auth":false,"models":[]}`, nil, ports.AgentAuthStatusNotApplicable},
		{"required nonsecret metadata", `{"name":"custom_team","engine":"openai","base_url":"https://models.example/v1","api_key_env":"TEAM_AUTH","env_vars":[{"name":"TEAM_PROJECT","required":true}],"models":[]}`, map[string]string{"TEAM_AUTH": "fixture"}, ports.AgentAuthStatusUnknown},
		{"required metadata present", `{"name":"custom_team","engine":"openai","base_url":"https://models.example/v1","api_key_env":"TEAM_AUTH","env_vars":[{"name":"TEAM_PROJECT","required":true}],"models":[]}`, map[string]string{"TEAM_AUTH": "fixture", "TEAM_PROJECT": "test"}, ports.AgentAuthStatusConfigured},
		{"mismatched identity", `{"name":"custom_other","engine":"openai","base_url":"https://models.example/v1","api_key_env":"TEAM_AUTH","models":[]}`, map[string]string{"TEAM_AUTH": "fixture"}, ports.AgentAuthStatusUnknown},
		{"bad metadata", `{"name":"custom_team","api_key_env":42}`, nil, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := tc.env
			if env == nil {
				env = map[string]string{}
			}
			env["GOOSE_PROVIDER"] = "custom_team"
			env["GOOSE_PATH_ROOT"] = t.TempDir()
			env["GOOSE_DISABLE_KEYRING"] = "1"
			gooseWrite(t, filepath.Join(env["GOOSE_PATH_ROOT"], "config", "custom_providers", "custom_team.json"), tc.metadata)
			got, err := gooseAuthStatus(context.Background(), ports.AgentAuthCheck{}, gooseDeps(t, env))
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestGooseKeyring(t *testing.T) {
	for _, tc := range []struct {
		name, out string
		err       error
		want      ports.AgentAuthStatus
	}{
		{"selected", `{"OPENAI_API_KEY":"fixture-secret"}`, nil, ports.AgentAuthStatusConfigured},
		{"unrelated", `{"ANTHROPIC_API_KEY":"fixture-secret"}`, nil, ports.AgentAuthStatusUnknown},
		{"malformed", "fixture-secret", nil, ports.AgentAuthStatusUnknown},
		{"inaccessible", "", errors.New("fixture-secret"), ports.AgentAuthStatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := gooseDeps(t, map[string]string{"GOOSE_PROVIDER": "openai"})
			d.GOOS = "darwin"
			d.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name != "/usr/bin/security" || !reflect.DeepEqual(args, []string{"find-generic-password", "-s", "goose", "-a", "secrets", "-w"}) {
					t.Fatalf("unsafe keyring command %q %v", name, args)
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("unbounded keyring command")
				}
				return []byte(tc.out), tc.err
			}
			got, err := gooseAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestGooseCloudProviderEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, provider string
		env            map[string]string
		want           ports.AgentAuthStatus
	}{
		{"AWS default chain", "bedrock", map[string]string{"AWS_ACCESS_KEY_ID": "fixture", "AWS_SECRET_ACCESS_KEY": "fixture"}, ports.AgentAuthStatusConfigured},
		{"incomplete AWS", "bedrock", map[string]string{"AWS_ACCESS_KEY_ID": "fixture"}, ports.AgentAuthStatusUnknown},
		{"Azure AD token", "azure", map[string]string{"AZURE_OPENAI_ENDPOINT": "https://example.openai.azure.com", "AZURE_OPENAI_DEPLOYMENT_NAME": "model", "AZURE_OPENAI_AD_TOKEN": "fixture"}, ports.AgentAuthStatusConfigured},
		{"Azure identity", "azure", map[string]string{"AZURE_OPENAI_ENDPOINT": "https://example.openai.azure.com", "AZURE_OPENAI_DEPLOYMENT_NAME": "model", "AZURE_TENANT_ID": "tenant", "AZURE_CLIENT_ID": "client", "AZURE_CLIENT_SECRET": "fixture"}, ports.AgentAuthStatusConfigured},
		{"unrelated cloud", "anthropic", map[string]string{"AWS_ACCESS_KEY_ID": "fixture", "AWS_SECRET_ACCESS_KEY": "fixture"}, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.env["GOOSE_PROVIDER"] = tc.provider
			d := gooseDeps(t, tc.env)
			d.Run = nil
			got, err := gooseAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	t.Run("ADC", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "adc.json")
		gooseWrite(t, path, `{"type":"authorized_user","client_id":"id","client_secret":"fixture","refresh_token":"fixture"}`)
		d := gooseDeps(t, map[string]string{"GOOSE_PROVIDER": "gcpvertexai", "GCP_PROJECT_ID": "project", "GOOGLE_APPLICATION_CREDENTIALS": path})
		got, err := gooseAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
		if err != nil || got != ports.AgentAuthStatusConfigured {
			t.Fatalf("got %q, %v; want configured", got, err)
		}
	})
	t.Run("Azure CLI", func(t *testing.T) {
		d := gooseDeps(t, map[string]string{"GOOSE_PROVIDER": "azure", "AZURE_OPENAI_ENDPOINT": "https://example.openai.azure.com", "AZURE_OPENAI_DEPLOYMENT_NAME": "model"})
		d.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name != "az" || !reflect.DeepEqual(args, []string{"account", "get-access-token", "--resource", "https://cognitiveservices.azure.com/", "--output", "json"}) {
				t.Fatalf("unexpected command %q %v", name, args)
			}
			return []byte(`{"accessToken":"fixture","expires_on":2000000000}`), nil
		}
		got, err := gooseAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
		if err != nil || got != ports.AgentAuthStatusConfigured {
			t.Fatalf("got %q, %v; want configured", got, err)
		}
	})
}

func TestGooseOAuthCaches(t *testing.T) {
	for _, tc := range []struct {
		name, provider, path, content string
		want                          ports.AgentAuthStatus
	}{
		{"Gemini", "gemini_oauth", "gemini_oauth/tokens.json", `{"project_id":"project","token":{"access_token":"fixture","refresh_token":"refresh","expires_at":"2020-01-01T00:00:00Z"}}`, ports.AgentAuthStatusConfigured},
		{"Kimi", "kimi_code", "kimicode/token.json", `{"access_token":"fixture","refresh_token":"refresh","expires_at":"2020-01-01T00:00:00Z"}`, ports.AgentAuthStatusConfigured},
		{"Goose ChatGPT provider cache", "chatgpt_codex", "chatgpt_codex/tokens.json", `{"access_token":"fixture","refresh_token":"refresh","expires_at":"2020-01-01T00:00:00Z","account_id":"account"}`, ports.AgentAuthStatusConfigured},
		{"xAI", "xai_oauth", "xai_oauth/tokens.json", `{"access_token":"fixture","refresh_token":"refresh","expires_at":"2020-01-01T00:00:00Z"}`, ports.AgentAuthStatusConfigured},
		{"expired no refresh", "xai_oauth", "xai_oauth/tokens.json", `{"access_token":"fixture","refresh_token":"","expires_at":"2020-01-01T00:00:00Z"}`, ports.AgentAuthStatusUnauthorized},
		{"bad expiry", "xai_oauth", "xai_oauth/tokens.json", `{"access_token":"fixture","expires_at":"tomorrow"}`, ports.AgentAuthStatusUnknown},
		{"Copilot", "github_copilot", "githubcopilot/info.json", `{"expires_at":"2099-01-01T00:00:00Z","info":{"token":"fixture","expires_at":4070908800,"refresh_in":1800,"endpoints":{"api":"https://api.githubcopilot.com"}}}`, ports.AgentAuthStatusConfigured},
		{"unrelated cache", "openai", "xai_oauth/tokens.json", `{"access_token":"fixture","refresh_token":"refresh","expires_at":"2099-01-01T00:00:00Z"}`, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			gooseWrite(t, filepath.Join(root, "config", tc.path), tc.content)
			d := gooseDeps(t, map[string]string{"GOOSE_PROVIDER": tc.provider, "GOOSE_PATH_ROOT": root})
			got, err := gooseAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestGooseDatabricksAndCopilotCredentialSelection(t *testing.T) {
	for _, tc := range []struct {
		name, provider, host, path, content string
		env                                 map[string]string
		want                                ports.AgentAuthStatus
	}{
		{"Databricks selected host", "databricks", "https://workspace.example", "databricks/oauth/6c04922cf768c3dab1c73f114eb710225a6c7c36a4161c343658e9a9b8901cf7.json", `{"access_token":"fixture","refresh_token":"refresh","expires_at":"2020-01-01T00:00:00Z"}`, nil, ports.AgentAuthStatusConfigured},
		{"Databricks other host", "databricks", "https://other.example", "databricks/oauth/6c04922cf768c3dab1c73f114eb710225a6c7c36a4161c343658e9a9b8901cf7.json", `{"access_token":"fixture","refresh_token":"refresh"}`, nil, ports.AgentAuthStatusUnknown},
		{"Databricks no refresh", "databricks", "https://workspace.example", "databricks/oauth/6c04922cf768c3dab1c73f114eb710225a6c7c36a4161c343658e9a9b8901cf7.json", `{"access_token":"fixture","expires_at":"2099-01-01T00:00:00Z"}`, nil, ports.AgentAuthStatusUnknown},
		{"Databricks v2 static token", "databricks_v2", "https://workspace.example", "", ``, map[string]string{"DATABRICKS_TOKEN": "fixture"}, ports.AgentAuthStatusConfigured},
		{"Copilot upstream login", "github_copilot", "", "", ``, map[string]string{"GITHUB_COPILOT_TOKEN": "fixture"}, ports.AgentAuthStatusConfigured},
		{"Copilot custom host", "github_copilot", "", "githubcopilot/git_example_com/info.json", `{"expires_at":"2099-01-01T00:00:00Z","info":{"token":"fixture","expires_at":4070908800}}`, map[string]string{"GITHUB_COPILOT_HOST": "https://git.example.com/"}, ports.AgentAuthStatusConfigured},
		{"Copilot unrelated host", "github_copilot", "", "githubcopilot/info.json", `{"expires_at":"2099-01-01T00:00:00Z","info":{"token":"fixture","expires_at":4070908800}}`, map[string]string{"GITHUB_COPILOT_HOST": "git.example.com"}, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			env := tc.env
			if env == nil {
				env = map[string]string{}
			}
			env["GOOSE_PROVIDER"] = tc.provider
			env["GOOSE_PATH_ROOT"] = root
			env["DATABRICKS_HOST"] = tc.host
			if tc.path != "" {
				gooseWrite(t, filepath.Join(root, "config", tc.path), tc.content)
			}
			got, err := gooseAuthStatus(context.Background(), ports.AgentAuthCheck{}, gooseDeps(t, env))
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func gooseTestHome(t *testing.T) string {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		t.Setenv(key, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GOOSE_DISABLE_KEYRING", "1")
	return home
}

func gooseWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGooseSelectedProviderLocalEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, config, secrets string
		env                   map[string]string
		want                  ports.AgentAuthStatus
	}{
		{"selected secret", "GOOSE_PROVIDER: openai\n", "OPENAI_API_KEY: fixture-key\n", nil, ports.AgentAuthStatusConfigured},
		{"selected environment", "GOOSE_PROVIDER: anthropic\n", "", map[string]string{"ANTHROPIC_API_KEY": "fixture-key"}, ports.AgentAuthStatusConfigured},
		{"unrelated environment", "GOOSE_PROVIDER: anthropic\n", "", map[string]string{"OPENAI_API_KEY": "fixture-key"}, ports.AgentAuthStatusUnknown},
		{"unrelated secret", "GOOSE_PROVIDER: anthropic\n", "OPENAI_API_KEY: fixture-key\n", nil, ports.AgentAuthStatusUnknown},
		{"config is not secret store", "GOOSE_PROVIDER: openai\nOPENAI_API_KEY: fixture-key\n", "", nil, ports.AgentAuthStatusUnknown},
		{"nested secret rejected", "GOOSE_PROVIDER: openai\n", "other:\n  OPENAI_API_KEY: fixture-key\n", nil, ports.AgentAuthStatusUnknown},
		{"no selected provider", "", "", map[string]string{"OPENAI_API_KEY": "fixture-key"}, ports.AgentAuthStatusUnknown},
		{"ollama", "GOOSE_PROVIDER: ollama\n", "", nil, ports.AgentAuthStatusNotApplicable},
		{"lmstudio", "GOOSE_PROVIDER: lmstudio\n", "", nil, ports.AgentAuthStatusNotApplicable},
		{"malformed YAML", "GOOSE_PROVIDER: [", "", nil, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := gooseTestHome(t)
			gooseWrite(t, filepath.Join(home, ".config", "goose", "config.yaml"), tc.config)
			gooseWrite(t, filepath.Join(home, ".config", "goose", "secrets.yaml"), tc.secrets)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, _, err := gooseLocalAuthStatus(context.Background())
			if err != nil || got != tc.want {
				t.Fatalf("status = %q, error = %v; want %q", got, err, tc.want)
			}
		})
	}
}
