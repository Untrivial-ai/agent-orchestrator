package qwen

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func isolateQwenAuth(t *testing.T) string {
	t.Helper()
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		t.Setenv(key, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	// Never inspect the host's real system-wide Qwen configuration.
	t.Setenv("QWEN_CODE_SYSTEM_SETTINGS_PATH", filepath.Join(home, "system/settings.json"))
	t.Setenv("QWEN_CODE_SYSTEM_DEFAULTS_PATH", filepath.Join(home, "defaults/settings.json"))
	return home
}

func writeQwenAuthFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestQwenLocalEvidenceIsConfigured(t *testing.T) {
	isolateQwenAuth(t)
	t.Setenv("ANTHROPIC_API_KEY", "test")
	t.Setenv("ANTHROPIC_MODEL", "claude")
	t.Setenv("ANTHROPIC_BASE_URL", "https://api.anthropic.com")
	got, err := (&Plugin{resolvedBinary: "qwen"}).AuthStatus(context.Background())
	if err != nil || got != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, %v; want configured", got, err)
	}
}

func TestQwenScopedAuth(t *testing.T) {
	cases := []struct {
		name, settings, model string
		env                   map[string]string
		args                  []string
		files                 map[string]string
		want                  ports.AgentAuthStatus
	}{
		{name: "missing", want: ports.AgentAuthStatusUnknown},
		{name: "OpenAI environment", env: map[string]string{"OPENAI_API_KEY": "test", "OPENAI_BASE_URL": "https://api.openai.com/v1", "OPENAI_MODEL": "gpt"}, want: ports.AgentAuthStatusConfigured},
		{name: "OpenAI missing endpoint", env: map[string]string{"OPENAI_API_KEY": "test", "OPENAI_MODEL": "gpt"}, want: ports.AgentAuthStatusUnknown},
		{name: "malformed endpoint", env: map[string]string{"OPENAI_API_KEY": "test", "OPENAI_BASE_URL": "://invalid", "OPENAI_MODEL": "gpt"}, want: ports.AgentAuthStatusUnknown},
		{name: "selected Gemini ignores OpenAI", settings: `{"security":{"auth":{"selectedType":"gemini"}},"model":{"name":"gemini"}}`, env: map[string]string{"OPENAI_API_KEY": "test"}, want: ports.AgentAuthStatusUnknown},
		{name: "Gemini environment", env: map[string]string{"GEMINI_API_KEY": "test", "GEMINI_MODEL": "gemini"}, want: ports.AgentAuthStatusConfigured},
		{name: "Vertex key", env: map[string]string{"GOOGLE_API_KEY": "test", "GOOGLE_MODEL": "gemini"}, want: ports.AgentAuthStatusConfigured},
		{name: "Vertex ADC", env: map[string]string{"GOOGLE_CLOUD_PROJECT": "project", "GOOGLE_MODEL": "gemini"}, files: map[string]string{".config/gcloud/application_default_credentials.json": `{"type":"authorized_user","client_id":"id","client_secret":"secret","refresh_token":"refresh"}`}, want: ports.AgentAuthStatusConfigured},
		{name: "Vertex missing project", settings: `{"security":{"auth":{"selectedType":"vertex-ai"}},"model":{"name":"gemini"}}`, files: map[string]string{".config/gcloud/application_default_credentials.json": `{"type":"authorized_user","client_id":"id","client_secret":"secret","refresh_token":"refresh"}`}, want: ports.AgentAuthStatusUnknown},
		{name: "Vertex missing model", env: map[string]string{"GOOGLE_CLOUD_PROJECT": "project"}, files: map[string]string{".config/gcloud/application_default_credentials.json": `{"type":"authorized_user","client_id":"id","client_secret":"secret","refresh_token":"refresh"}`}, want: ports.AgentAuthStatusUnknown},
		{name: "Vertex malformed ADC", env: map[string]string{"GOOGLE_CLOUD_PROJECT": "project", "GOOGLE_MODEL": "gemini"}, files: map[string]string{".config/gcloud/application_default_credentials.json": `{"type":"authorized_user"}`}, want: ports.AgentAuthStatusUnknown},
		{name: "custom envKey", settings: `{"security":{"auth":{"selectedType":"openai"}},"model":{"name":"custom"},"modelProviders":{"openai":[{"id":"custom","baseUrl":"https://custom.example.test/v1","envKey":"CUSTOM_KEY"}]}}`, env: map[string]string{"CUSTOM_KEY": "test"}, want: ports.AgentAuthStatusConfigured},
		{name: "custom envKey does not fall back", settings: `{"security":{"auth":{"selectedType":"openai","apiKey":"ignored"}},"model":{"name":"custom"},"modelProviders":{"openai":[{"id":"custom","baseUrl":"https://custom.example.test/v1","envKey":"CUSTOM_KEY"}]}}`, env: map[string]string{"OPENAI_API_KEY": "ignored"}, want: ports.AgentAuthStatusUnknown},
		{name: "custom provider protocol", settings: `{"providerProtocol":{"company":"openai"},"security":{"auth":{"selectedType":"openai"}},"model":{"name":"custom"},"modelProviders":{"company":[{"id":"custom","baseUrl":"https://custom.example.test/v1","envKey":"CUSTOM_KEY"}]}}`, env: map[string]string{"CUSTOM_KEY": "test"}, want: ports.AgentAuthStatusConfigured},
		{name: "unknown provider ignored", settings: `{"security":{"auth":{"selectedType":"openai"}},"model":{"name":"custom"},"modelProviders":{"typo":[{"id":"custom","baseUrl":"https://custom.example.test/v1","envKey":"CUSTOM_KEY"}]}}`, env: map[string]string{"CUSTOM_KEY": "test"}, want: ports.AgentAuthStatusUnknown},
		{name: "unselected model ignored", settings: `{"security":{"auth":{"selectedType":"openai"}},"model":{"name":"other"},"modelProviders":{"openai":[{"id":"custom","baseUrl":"https://custom.example.test/v1","envKey":"CUSTOM_KEY"}]}}`, env: map[string]string{"CUSTOM_KEY": "test"}, want: ports.AgentAuthStatusUnknown},
		{name: "duplicate model paired endpoint", settings: `{"security":{"auth":{"selectedType":"openai"}},"model":{"name":"same","baseUrl":"https://second.example.test/v1"},"modelProviders":{"openai":[{"id":"same","baseUrl":"https://first.example.test/v1","envKey":"FIRST_KEY"},{"id":"same","baseUrl":"https://second.example.test/v1","envKey":"SECOND_KEY"}]}}`, env: map[string]string{"FIRST_KEY": "ignored"}, want: ports.AgentAuthStatusUnknown},
		{name: "custom bucket declaration order", settings: `{"providerProtocol":{"zcompany":"openai","acompany":"openai"},"security":{"auth":{"selectedType":"openai"}},"model":{"name":"same"},"modelProviders":{"zcompany":[{"id":"same","baseUrl":"https://first.example.test/v1","envKey":"FIRST_KEY"}],"acompany":[{"id":"same","baseUrl":"https://second.example.test/v1","envKey":"SECOND_KEY"}]}}`, env: map[string]string{"SECOND_KEY": "ignored"}, want: ports.AgentAuthStatusUnknown},
		{name: "chat route wins over responses fallback", settings: `{"security":{"auth":{"selectedType":"openai"}},"model":{"name":"same"},"modelProviders":{"openai":[{"id":"same","wireApi":"responses","baseUrl":"https://first.example.test/v1","envKey":"FIRST_KEY"},{"id":"same","wireApi":"chat-completions","baseUrl":"https://second.example.test/v1","envKey":"SECOND_KEY"}]}}`, env: map[string]string{"FIRST_KEY": "ignored"}, want: ports.AgentAuthStatusUnknown},
		{name: "security auth fallback", settings: `{"security":{"auth":{"selectedType":"openai","apiKey":"test","baseUrl":"https://api.openai.com/v1"}},"model":{"name":"gpt"}}`, want: ports.AgentAuthStatusConfigured},
		{name: "unresolved model placeholder", settings: `{"security":{"auth":{"selectedType":"openai","apiKey":"test","baseUrl":"https://api.openai.com/v1"}},"model":{"name":"${MISSING_MODEL}"}}`, want: ports.AgentAuthStatusUnknown},
		{name: "resolved model placeholder", settings: `{"security":{"auth":{"selectedType":"openai"}},"model":{"name":"${SELECTED_MODEL}"},"modelProviders":{"openai":[{"id":"custom","envKey":"CUSTOM_KEY","baseUrl":"https://custom.example.test/v1"}]}}`, env: map[string]string{"SELECTED_MODEL": "custom", "CUSTOM_KEY": "test"}, want: ports.AgentAuthStatusConfigured},
		{name: "unrelated recursive key", settings: `{"mcpServers":{"secret":{"apiKey":"ignored"}},"tools":{"envKey":"OPENAI_API_KEY"}}`, want: ports.AgentAuthStatusUnknown},
		{name: "old provider object ignored", settings: `{"modelProviders":{"openai":{"apiKey":"ignored","baseUrl":"https://api.openai.com/v1"}},"model":{"name":"gpt"}}`, want: ports.AgentAuthStatusUnknown},
		{name: "config env", settings: `{"env":{"GEMINI_API_KEY":"test","GEMINI_MODEL":"gemini"}}`, want: ports.AgentAuthStatusConfigured},
		{name: "malformed settings blocks fallback", settings: `{`, env: map[string]string{"GEMINI_API_KEY": "test", "GEMINI_MODEL": "gemini"}, want: ports.AgentAuthStatusUnknown},
		{name: "CLI scope", args: []string{"qwen", "--auth-type", "openai", "--model", "gpt", "--openai-api-key", "test", "--openai-base-url=https://api.openai.com/v1"}, want: ports.AgentAuthStatusConfigured},
		{name: "AO model scope", model: "gpt", args: []string{"--auth-type=openai"}, env: map[string]string{"OPENAI_API_KEY": "test", "OPENAI_BASE_URL": "https://api.openai.com/v1"}, want: ports.AgentAuthStatusConfigured},
		{name: "CLI endpoint overrides broken environment", args: []string{"--auth-type=openai", "--model=gpt", "--openai-api-key=test", "--openai-base-url=https://api.openai.com/v1"}, env: map[string]string{"OPENAI_BASE_URL": "broken"}, want: ports.AgentAuthStatusConfigured},
		{name: "local selected", settings: `{"security":{"auth":{"selectedType":"openai"}},"model":{"name":"local"},"modelProviders":{"openai":[{"id":"local","baseUrl":"http://localhost:11434/v1"}]}}`, want: ports.AgentAuthStatusNotApplicable},
		{name: "local required key missing", settings: `{"security":{"auth":{"selectedType":"openai"}},"model":{"name":"local"},"modelProviders":{"openai":[{"id":"local","baseUrl":"http://localhost:11434/v1","envKey":"REQUIRED_KEY"}]}}`, want: ports.AgentAuthStatusUnknown},
		{name: "discontinued oauth cache", files: map[string]string{".qwen/oauth_creds.json": `{"access_token":"test","refresh_token":"test"}`}, want: ports.AgentAuthStatusUnknown},
		{name: "discontinued selected OAuth ignores other key", settings: `{"security":{"auth":{"selectedType":"qwen-oauth"}}}`, env: map[string]string{"OPENAI_API_KEY": "test", "OPENAI_MODEL": "gpt", "OPENAI_BASE_URL": "https://api.openai.com/v1"}, want: ports.AgentAuthStatusUnknown},
		{name: "dotenv exported quoted commented", files: map[string]string{".qwen/.env": "export OPENAI_API_KEY='test # literal' # comment\nOPENAI_MODEL=\"gpt\"\nOPENAI_BASE_URL=https://api.openai.com/v1 # comment\n"}, want: ports.AgentAuthStatusConfigured},
		{name: "empty quoted dotenv", files: map[string]string{".qwen/.env": "OPENAI_API_KEY='' # no key\nOPENAI_MODEL=gpt\nOPENAI_BASE_URL=https://api.openai.com/v1\n"}, want: ports.AgentAuthStatusUnknown},
		{name: "home dotenv", files: map[string]string{".env": "GEMINI_API_KEY=test\nGEMINI_MODEL=gemini\n"}, want: ports.AgentAuthStatusConfigured},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := isolateQwenAuth(t)
			if tc.settings != "" {
				writeQwenAuthFile(t, filepath.Join(home, ".qwen/settings.json"), tc.settings)
			}
			for path, content := range tc.files {
				writeQwenAuthFile(t, filepath.Join(home, path), content)
			}
			checker, ok := any(&Plugin{resolvedBinary: "qwen"}).(ports.AgentScopedAuthChecker)
			if !ok {
				t.Fatal("Qwen does not resolve scoped authentication")
			}
			got, err := checker.AuthStatusFor(context.Background(), ports.AgentAuthCheck{Config: ports.AgentConfig{Model: tc.model}, Env: tc.env, Args: tc.args})
			if err != nil || got != tc.want {
				t.Fatalf("status = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestQwenSystemAuthPathsAcrossPlatforms(t *testing.T) {
	for _, tc := range []struct{ goos, system, defaults string }{
		{"darwin", "/Library/Application Support/QwenCode/settings.json", "/Library/Application Support/QwenCode/system-defaults.json"},
		{"linux", "/etc/qwen-code/settings.json", "/etc/qwen-code/system-defaults.json"},
		{"windows", `C:\ProgramData\qwen-code\settings.json`, `C:\ProgramData\qwen-code\system-defaults.json`},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			fixture := filepath.Join(t.TempDir(), "fixture.json")
			writeQwenAuthFile(t, fixture, `{"security":{"auth":{"selectedType":"gemini"}},"model":{"name":"gemini"},"env":{"GEMINI_API_KEY":"test"}}`)
			info, err := os.Stat(fixture)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{tc.system, tc.defaults} {
				d := authutil.Dependencies{GOOS: tc.goos, Getenv: func(string) string { return "" }, Lstat: func(candidate string) (os.FileInfo, error) {
					if candidate == path {
						return info, nil
					}
					return nil, os.ErrNotExist
				}, ReadFile: func(string) ([]byte, error) { return os.ReadFile(fixture) }}
				got, err := qwenAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
				if err != nil || got != ports.AgentAuthStatusConfigured {
					t.Fatalf("system source %s: %q, %v; want configured", path, got, err)
				}
			}
		})
	}
}

func TestQwenHomeOverride(t *testing.T) {
	home := isolateQwenAuth(t)
	t.Setenv("QWEN_HOME", "~/custom-qwen")
	writeQwenAuthFile(t, filepath.Join(home, "custom-qwen/settings.json"), `{"security":{"auth":{"selectedType":"gemini"}},"model":{"name":"gemini"}}`)
	writeQwenAuthFile(t, filepath.Join(home, "custom-qwen/.env"), "GEMINI_API_KEY=test\n")
	got, err := (&Plugin{resolvedBinary: "qwen"}).AuthStatus(context.Background())
	if err != nil || got != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, %v; want configured", got, err)
	}
}

func TestQwenDocumentedProviderVariablesRequireSelectedRoute(t *testing.T) {
	for _, name := range []string{"QWEN_API_KEY", "BAILIAN_CODING_PLAN_API_KEY", "BAILIAN_TOKEN_PLAN_API_KEY", "DASHSCOPE_API_KEY", "OPENROUTER_API_KEY", "REQUESTY_API_KEY", "ZAI_API_KEY", "DEEPSEEK_API_KEY", "XAI_API_KEY", "MINIMAX_API_KEY", "MOONSHOT_API_KEY", "IDEALAB_API_KEY", "MODELSCOPE_API_KEY", "FIREWORKS_API_KEY", "CEREBRAS_API_KEY"} {
		t.Run(name, func(t *testing.T) {
			home := isolateQwenAuth(t)
			t.Setenv(name, "test")
			p := &Plugin{resolvedBinary: "qwen"}
			got, err := p.AuthStatus(context.Background())
			if err != nil || got != ports.AgentAuthStatusUnknown {
				t.Fatalf("unselected key = %q, %v; want unknown", got, err)
			}
			writeQwenAuthFile(t, filepath.Join(home, ".qwen/settings.json"), fmt.Sprintf(`{"security":{"auth":{"selectedType":"openai"}},"model":{"name":"custom"},"modelProviders":{"openai":[{"id":"custom","envKey":%q,"baseUrl":"https://custom.example.test/v1"}]}}`, name))
			got, err = p.AuthStatus(context.Background())
			if err != nil || got != ports.AgentAuthStatusConfigured {
				t.Fatalf("selected key = %q, %v; want configured", got, err)
			}
		})
	}
}

func TestQwenSettingsAndDotenvPrecedence(t *testing.T) {
	home := isolateQwenAuth(t)
	project := filepath.Join(home, "project")
	workingDir := filepath.Join(project, "nested")
	if err := os.MkdirAll(workingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	checker, ok := any(&Plugin{resolvedBinary: "qwen"}).(ports.AgentScopedAuthChecker)
	if !ok {
		t.Fatal("Qwen does not resolve scoped authentication")
	}
	steps := []struct {
		path, content string
		want          ports.AgentAuthStatus
	}{
		{filepath.Join(home, "defaults/settings.json"), `{"security":{"auth":{"selectedType":"anthropic"}},"model":{"name":"claude"},"env":{"ANTHROPIC_API_KEY":"test"}}`, ports.AgentAuthStatusConfigured},
		{filepath.Join(home, ".qwen/settings.json"), `{"security":{"auth":{"selectedType":"gemini"}},"model":{"name":"gemini"}}`, ports.AgentAuthStatusUnknown},
		{filepath.Join(project, ".qwen/settings.json"), `{"security":{"auth":{"selectedType":"openai"}},"model":{"name":"gpt"},"env":{"OPENAI_API_KEY":"test","OPENAI_BASE_URL":"https://api.openai.com/v1"}}`, ports.AgentAuthStatusConfigured},
		{filepath.Join(project, ".env"), "OPENAI_BASE_URL=invalid\n", ports.AgentAuthStatusUnknown},
		{filepath.Join(project, ".qwen/.env"), "OPENAI_BASE_URL=https://api.openai.com/v1\n", ports.AgentAuthStatusConfigured},
		{filepath.Join(home, "system/settings.json"), `{"security":{"auth":{"selectedType":"gemini"}}}`, ports.AgentAuthStatusUnknown},
	}
	for _, step := range steps {
		writeQwenAuthFile(t, step.path, step.content)
		got, err := checker.AuthStatusFor(context.Background(), ports.AgentAuthCheck{WorkingDir: workingDir})
		if err != nil || got != step.want {
			t.Fatalf("after %s: status = %q, %v; want %q", step.path, got, err, step.want)
		}
	}
	got, err := checker.AuthStatusFor(context.Background(), ports.AgentAuthCheck{WorkingDir: workingDir, Env: map[string]string{"GEMINI_API_KEY": "test"}})
	if err != nil || got != ports.AgentAuthStatusConfigured {
		t.Fatalf("scoped environment = %q, %v; want configured", got, err)
	}
}

func TestQwenGlobalCheckIgnoresWorkingDirectory(t *testing.T) {
	isolateQwenAuth(t)
	project := t.TempDir()
	t.Chdir(project)
	writeQwenAuthFile(t, filepath.Join(project, ".env"), "GEMINI_API_KEY=test\nGEMINI_MODEL=gemini\n")
	got, err := (&Plugin{resolvedBinary: "qwen"}).AuthStatus(context.Background())
	if err != nil || got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, %v; want unknown", got, err)
	}
}

func TestQwenMalformedConfigDoesNotExposeLowerPriorityCredentials(t *testing.T) {
	for _, tc := range []struct{ name, path, content string }{
		{"project syntax", "workspace/.qwen/settings.json", `{"security":`},
		{"project selection schema", "workspace/.qwen/settings.json", `{"security":{"auth":{"selectedType":42}}}`},
		{"project model schema", "workspace/.qwen/settings.json", `{"model":{"name":42}}`},
		{"project provider schema", "workspace/.qwen/settings.json", `{"modelProviders":{"gemini":[{"id":42}]}}`},
		{"project protocol schema", "workspace/.qwen/settings.json", `{"providerProtocol":{"company":42}}`},
		{"system override syntax", "system/settings.json", `{`},
		{"system defaults override syntax", "defaults/settings.json", `{`},
		{"explicit QWEN_HOME syntax", "custom-qwen/settings.json", `{`},
		{"project dotenv", "workspace/.qwen/.env", "GOOGLE_MODEL='unfinished\n"},
		{"null root", "workspace/.qwen/settings.json", `null`},
		{"oversized project", "workspace/.qwen/settings.json", strings.Repeat(" ", (1<<20)+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := isolateQwenAuth(t)
			t.Setenv("GEMINI_API_KEY", "lower-key")
			t.Setenv("GEMINI_MODEL", "gemini")
			if strings.HasPrefix(tc.path, "custom-qwen/") {
				t.Setenv("QWEN_HOME", filepath.Join(home, "custom-qwen"))
			}
			writeQwenAuthFile(t, filepath.Join(home, ".qwen/settings.json"), `{"security":{"auth":{"selectedType":"gemini"}},"model":{"name":"gemini"}}`)
			writeQwenAuthFile(t, filepath.Join(home, tc.path), tc.content)
			got, err := (&Plugin{resolvedBinary: "qwen"}).AuthStatusFor(context.Background(), ports.AgentAuthCheck{WorkingDir: filepath.Join(home, "workspace")})
			if err != nil || got != ports.AgentAuthStatusUnknown {
				t.Fatalf("status = %q, %v; want unknown", got, err)
			}
		})
	}
}

func TestQwenMalformedADCCanUseIndependentCredentialSource(t *testing.T) {
	home := isolateQwenAuth(t)
	path := filepath.Join(home, "malformed-adc.json")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
	t.Setenv("GOOGLE_CLOUD_PROJECT", "project")
	t.Setenv("GOOGLE_MODEL", "gemini")
	writeQwenAuthFile(t, path, `{`)
	writeQwenAuthFile(t, filepath.Join(home, ".config/gcloud/application_default_credentials.json"), `{"type":"authorized_user","client_id":"id","client_secret":"secret","refresh_token":"refresh"}`)
	got, err := (&Plugin{resolvedBinary: "qwen"}).AuthStatus(context.Background())
	if err != nil || got != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, %v; want configured", got, err)
	}
}
