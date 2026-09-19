package crush

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestCrushLocalAuthStatusConfiguredWithDocumentedEnv(t *testing.T) {
	isolateCrushAuth(t)
	t.Setenv("HYPER_API_KEY", "test-key")
	status, ok, err := crushLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = (%q, %v), want configured", status, ok)
	}
}

func TestCrushLocalAuthStatusDoesNotUseProviderCatalog(t *testing.T) {
	isolateCrushAuth(t)
	status, ok, err := crushLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func isolateCrushAuth(t *testing.T) string {
	t.Helper()
	// Clear every inherited variable, including provider-specific CRUSH_ overrides.
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		t.Setenv(key, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func writeCrushAuthFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCrushAuthSources(t *testing.T) {
	cases := []struct {
		name, config, model string
		env                 map[string]string
		files               map[string]string
		want                ports.AgentAuthStatus
	}{
		{name: "missing", want: ports.AgentAuthStatusUnknown},
		{name: "azure environment isolation", env: map[string]string{"AZURE_OPENAI_API_KEY": "test-key"}, want: ports.AgentAuthStatusConfigured},
		{name: "Hyper OAuth", config: `{"providers":{"hyper":{"oauth":{"access_token":"test","refresh_token":"refresh","expires_at":4102444800}}}}`, want: ports.AgentAuthStatusConfigured},
		{name: "Copilot OAuth", config: `{"providers":{"copilot":{"oauth":{"access_token":"test","expires_at":4102444800}}}}`, want: ports.AgentAuthStatusConfigured},
		{name: "OpenAI OAuth refreshable", config: `{"providers":{"openai":{"oauth":{"access_token":"test","refresh_token":"refresh","expires_at":1}}}}`, want: ports.AgentAuthStatusConfigured},
		{name: "stored key", config: `{"providers":{"openai":{"api_key":"test"}}}`, want: ports.AgentAuthStatusConfigured},
		{name: "top level env overrides process", model: "openai/gpt", config: `{"env":{"OPENAI_API_KEY":""}}`, env: map[string]string{"OPENAI_API_KEY": "ignored"}, want: ports.AgentAuthStatusUnknown},
		{name: "top level env and reference", config: `{"env":{"CUSTOM_KEY":"test"},"providers":{"openai":{"api_key":"$CUSTOM_KEY"}}}`, want: ports.AgentAuthStatusConfigured},
		{name: "unresolved reference", config: `{"providers":{"openai":{"api_key":"$MISSING"}}}`, want: ports.AgentAuthStatusUnknown},
		{name: "explicit key reference blocks default key", config: `{"providers":{"openai":{"api_key":"$MISSING"}}}`, env: map[string]string{"OPENAI_API_KEY": "ignored"}, want: ports.AgentAuthStatusUnknown},
		{name: "custom key requires endpoint", model: "company/model", config: `{"providers":{"company":{"type":"openai-compat","api_key":"test"}}}`, want: ports.AgentAuthStatusUnknown},
		{name: "custom key malformed endpoint", model: "company/model", config: `{"providers":{"company":{"type":"openai-compat","api_key":"test","base_url":"://broken"}}}`, want: ports.AgentAuthStatusUnknown},
		{name: "custom key endpoint", model: "company/model", config: `{"providers":{"company":{"type":"openai-compat","api_key":"test","base_url":"https://custom.example.test/v1"}}}`, want: ports.AgentAuthStatusConfigured},
		{name: "command reference never executed", config: `{"providers":{"openai":{"api_key":"$(echo secret)"}}}`, want: ports.AgentAuthStatusUnknown},
		{name: "unrelated provider", model: "anthropic/claude", env: map[string]string{"OPENAI_API_KEY": "test"}, want: ports.AgentAuthStatusUnknown},
		{name: "selected config provider", config: `{"models":{"large":{"provider":"anthropic","model":"claude"}},"providers":{"openai":{"api_key":"test"}}}`, want: ports.AgentAuthStatusUnknown},
		{name: "disabled provider", config: `{"providers":{"openai":{"api_key":"test","disable":true}}}`, env: map[string]string{"OPENAI_API_KEY": "test"}, want: ports.AgentAuthStatusUnknown},
		{name: "local selected", model: "ollama/llama", config: `{"providers":{"ollama":{"type":"ollama","base_url":"http://localhost:11434/v1"}}}`, want: ports.AgentAuthStatusNotApplicable},
		{name: "local unselected", config: `{"providers":{"ollama":{"type":"ollama","base_url":"http://localhost:11434/v1"}}}`, want: ports.AgentAuthStatusUnknown},
		{name: "custom no auth selected", model: "private/model", config: `{"providers":{"private":{"type":"openai-compat","base_url":"https://inference.example.test/v1"}}}`, want: ports.AgentAuthStatusNotApplicable},
		{name: "AWS profile name insufficient", model: "bedrock/claude", env: map[string]string{"AWS_PROFILE": "work"}, want: ports.AgentAuthStatusUnknown},
		{name: "AWS profile credentials", model: "bedrock/claude", env: map[string]string{"AWS_PROFILE": "work"}, files: map[string]string{".aws/credentials": "[work]\naws_access_key_id=test\naws_secret_access_key=test\n"}, want: ports.AgentAuthStatusConfigured},
		{name: "AWS SSO cache", model: "bedrock/claude", files: map[string]string{".aws/config": "[default]\nsso_session=dev\nsso_account_id=123\nsso_role_name=role\n[sso-session dev]\nsso_start_url=https://example.awsapps.com/start\nsso_region=us-east-1\n", ".aws/sso/cache/34c6fceca75e456f25e7e99531e2425c6c1de443.json": `{"accessToken":"test","expiresAt":"2100-01-01T00:00:00Z"}`}, want: ports.AgentAuthStatusConfigured},
		{name: "Vertex ADC", model: "vertexai/gemini", env: map[string]string{"VERTEXAI_PROJECT": "project", "VERTEXAI_LOCATION": "us-east1"}, files: map[string]string{".config/gcloud/application_default_credentials.json": `{"type":"authorized_user","client_id":"id","client_secret":"secret","refresh_token":"refresh"}`}, want: ports.AgentAuthStatusConfigured},
		{name: "malformed ADC", model: "vertexai/gemini", env: map[string]string{"VERTEXAI_PROJECT": "project", "VERTEXAI_LOCATION": "us-east1"}, files: map[string]string{".config/gcloud/application_default_credentials.json": `{"type":"service_account"}`}, want: ports.AgentAuthStatusUnknown},
		{name: "Azure Entra", model: "azure/gpt", env: map[string]string{"AZURE_TENANT_ID": "tenant", "AZURE_CLIENT_ID": "client", "AZURE_CLIENT_SECRET": "secret"}, want: ports.AgentAuthStatusConfigured},
		{name: "provider catalog ignored", files: map[string]string{".local/share/crush/providers.json": `{"providers":{"openai":{"api_key":"test"}}}`}, want: ports.AgentAuthStatusUnknown},
		{name: "malformed config blocks fallback", config: `{`, env: map[string]string{"OPENAI_API_KEY": "test"}, want: ports.AgentAuthStatusUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := isolateCrushAuth(t)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			for path, content := range tc.files {
				writeCrushAuthFile(t, filepath.Join(home, path), content)
			}
			if tc.config != "" {
				writeCrushAuthFile(t, filepath.Join(home, ".config/crush/crush.json"), tc.config)
			}
			checker, ok := any(&Plugin{resolvedBinary: "crush"}).(ports.AgentScopedAuthChecker)
			if !ok {
				t.Fatal("Crush does not resolve scoped authentication")
			}
			got, err := checker.AuthStatusFor(context.Background(), ports.AgentAuthCheck{Config: ports.AgentConfig{Model: tc.model}})
			if err != nil || got != tc.want {
				t.Fatalf("status = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestCrushConfigPrecedence(t *testing.T) {
	home := isolateCrushAuth(t)
	project, global, data := filepath.Join(home, "project"), filepath.Join(home, "global"), filepath.Join(home, "data")
	t.Setenv("CRUSH_GLOBAL_CONFIG", global)
	t.Setenv("CRUSH_GLOBAL_DATA", data)
	writeCrushAuthFile(t, filepath.Join(global, "crush.json"), `{"models":{"large":{"provider":"anthropic","model":"claude"}},"providers":{"openai":{"api_key":"key"}}}`)
	checker, ok := any(&Plugin{resolvedBinary: "crush"}).(ports.AgentScopedAuthChecker)
	if !ok {
		t.Fatal("Crush does not resolve scoped authentication")
	}
	steps := []struct {
		path, content string
		want          ports.AgentAuthStatus
	}{
		{filepath.Join(global, "crushrc"), "model large openai/gpt\n", ports.AgentAuthStatusConfigured},
		{filepath.Join(data, "crush.json"), `{"models":{"large":{"provider":"anthropic","model":"claude"}}}`, ports.AgentAuthStatusUnknown},
		{filepath.Join(project, "crush.json"), `{"models":{"large":{"provider":"openai","model":"gpt"}}}`, ports.AgentAuthStatusConfigured},
		{filepath.Join(project, ".crush.json"), `{"models":{"large":{"provider":"anthropic","model":"claude"}}}`, ports.AgentAuthStatusUnknown},
		{filepath.Join(project, "crushrc"), "model large openai/gpt\n", ports.AgentAuthStatusConfigured},
		{filepath.Join(project, ".crushrc"), "provider add ollama --type ollama --base-url 'http://localhost:11434/v1'\nmodel large ollama/llama\n", ports.AgentAuthStatusNotApplicable},
	}
	for _, step := range steps {
		writeCrushAuthFile(t, step.path, step.content)
		got, err := checker.AuthStatusFor(context.Background(), ports.AgentAuthCheck{WorkingDir: project})
		if err != nil || got != step.want {
			t.Fatalf("after %s: status = %q, %v; want %q", filepath.Base(step.path), got, err, step.want)
		}
	}
	got, err := checker.AuthStatusFor(context.Background(), ports.AgentAuthCheck{})
	if err != nil || got != ports.AgentAuthStatusUnknown {
		t.Fatalf("global check used project: %q, %v", got, err)
	}
}

func TestCrushDynamicShellConfigStaysUnknown(t *testing.T) {
	home := isolateCrushAuth(t)
	t.Setenv("OPENAI_API_KEY", "test")
	writeCrushAuthFile(t, filepath.Join(home, ".config/crush/crushrc"), "source /does/not/exist\n")
	got, err := (&Plugin{resolvedBinary: "crush"}).AuthStatus(context.Background())
	if err != nil || got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, %v; want unknown", got, err)
	}
}

func TestCrushWorkspaceDataIsIndependentOfAOData(t *testing.T) {
	for _, tc := range []struct {
		name, workspaceConfig, aoConfig string
		want                            ports.AgentAuthStatus
	}{
		{"AO credential is not native evidence", `{}`, `{"providers":{"openai":{"api_key":"ao-only-key"}}}`, ports.AgentAuthStatusUnknown},
		{"native credential survives malformed AO file", `{"providers":{"openai":{"api_key":"native-key"}}}`, `{`, ports.AgentAuthStatusConfigured},
		{"native selection overrides AO credential", `{"models":{"large":{"provider":"anthropic","model":"claude"}}}`, `{"providers":{"openai":{"api_key":"ao-only-key"}}}`, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := isolateCrushAuth(t)
			workspace, aoData := filepath.Join(home, "workspace"), filepath.Join(home, "ao-data")
			writeCrushAuthFile(t, filepath.Join(workspace, ".crush", "crush.json"), tc.workspaceConfig)
			writeCrushAuthFile(t, filepath.Join(aoData, "crush.json"), tc.aoConfig)
			got, err := (&Plugin{resolvedBinary: "crush"}).AuthStatusFor(context.Background(), ports.AgentAuthCheck{WorkingDir: workspace, DataDir: aoData})
			if err != nil || got != tc.want {
				t.Fatalf("status = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestCrushMalformedConfigDoesNotExposeLowerPriorityCredentials(t *testing.T) {
	for _, tc := range []struct{ name, path, content string }{
		{"project syntax", "workspace/.crush.json", `{"models":`},
		{"project selection schema", "workspace/.crush.json", `{"models":{"large":{"provider":42}}}`},
		{"project provider schema", "workspace/.crush.json", `{"providers":{"openai":{"disable":"yes"}}}`},
		{"null selected provider", "workspace/.crush.json", `{"models":{"large":{"provider":null}}}`},
		{"null selected model", "workspace/.crush.json", `{"models":{"large":{"model":null}}}`},
		{"null model slot", "workspace/.crush.json", `{"models":{"large":null}}`},
		{"null models container", "workspace/.crush.json", `{"models":null}`},
		{"null provider key", "workspace/.crush.json", `{"providers":{"openai":{"api_key":null}}}`},
		{"null provider disable", "workspace/.crush.json", `{"providers":{"openai":{"disable":null}}}`},
		{"null OAuth token", "workspace/.crush.json", `{"providers":{"openai":{"oauth":{"access_token":null}}}}`},
		{"null providers container", "workspace/.crush.json", `{"providers":null}`},
		{"null env container", "workspace/.crush.json", `{"env":null}`},
		{"native workspace data", "workspace/.crush/crush.json", `{`},
		{"explicit global config", "explicit-config/crush.json", `{`},
		{"explicit global data", "explicit-data/crush.json", `{`},
		{"null root", "workspace/.crush.json", `null`},
		{"oversized project", "workspace/.crush.json", strings.Repeat(" ", (1<<20)+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := isolateCrushAuth(t)
			t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Join(home, "explicit-config"))
			t.Setenv("CRUSH_GLOBAL_DATA", filepath.Join(home, "explicit-data"))
			t.Setenv("OPENAI_API_KEY", "lower-key")
			writeCrushAuthFile(t, filepath.Join(home, "explicit-config/crush.json"), `{"models":{"large":{"provider":"openai","model":"gpt"}}}`)
			writeCrushAuthFile(t, filepath.Join(home, tc.path), tc.content)
			got, err := (&Plugin{resolvedBinary: "crush"}).AuthStatusFor(context.Background(), ports.AgentAuthCheck{WorkingDir: filepath.Join(home, "workspace")})
			if err != nil || got != ports.AgentAuthStatusUnknown {
				t.Fatalf("status = %q, %v; want unknown", got, err)
			}
		})
	}
}

func TestCrushMalformedADCCanUseIndependentCredentialSource(t *testing.T) {
	home := isolateCrushAuth(t)
	path := filepath.Join(home, "malformed-adc.json")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
	t.Setenv("VERTEXAI_PROJECT", "project")
	t.Setenv("VERTEXAI_LOCATION", "us-east1")
	writeCrushAuthFile(t, path, `{`)
	writeCrushAuthFile(t, filepath.Join(home, ".config/gcloud/application_default_credentials.json"), `{"type":"authorized_user","client_id":"id","client_secret":"secret","refresh_token":"refresh"}`)
	got, err := (&Plugin{resolvedBinary: "crush"}).AuthStatusFor(context.Background(), ports.AgentAuthCheck{Config: ports.AgentConfig{Model: "vertexai/gemini"}})
	if err != nil || got != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, %v; want configured", got, err)
	}
}
