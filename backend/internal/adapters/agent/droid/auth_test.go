package droid

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func isolateDroidAuth(t *testing.T) string {
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

func writeDroidAuthFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func scopedDroidStatus(t *testing.T, scope ports.AgentAuthCheck) ports.AgentAuthStatus {
	t.Helper()
	checker, ok := any(&Plugin{resolvedBinary: "droid"}).(ports.AgentScopedAuthChecker)
	if !ok {
		t.Fatal("Droid does not implement scoped authentication")
	}
	status, err := checker.AuthStatusFor(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func TestDroidBrowserAndStaticKeyEvidenceIsConfigured(t *testing.T) {
	t.Run("browser login", func(t *testing.T) {
		home := isolateDroidAuth(t)
		writeDroidAuthFile(t, filepath.Join(home, ".factory", "auth.v2.file"), "encrypted-login")
		writeDroidAuthFile(t, filepath.Join(home, ".factory", "auth.v2.key"), "wrapped-key")
		if got := scopedDroidStatus(t, ports.AgentAuthCheck{}); got != ports.AgentAuthStatusConfigured {
			t.Fatalf("status = %q, want configured", got)
		}
	})
	t.Run("incomplete browser login", func(t *testing.T) {
		home := isolateDroidAuth(t)
		writeDroidAuthFile(t, filepath.Join(home, ".factory", "auth.v2.file"), "encrypted-login")
		if got := scopedDroidStatus(t, ports.AgentAuthCheck{}); got != ports.AgentAuthStatusUnknown {
			t.Fatalf("status = %q, want unknown", got)
		}
	})
	t.Run("factory api key", func(t *testing.T) {
		isolateDroidAuth(t)
		t.Setenv("FACTORY_API_KEY", "factory-test-key")
		if got := scopedDroidStatus(t, ports.AgentAuthCheck{}); got != ports.AgentAuthStatusConfigured {
			t.Fatalf("status = %q, want configured", got)
		}
	})
}

func TestDroidSettingsSourcesAndPrecedence(t *testing.T) {
	home := isolateDroidAuth(t)
	workspace := filepath.Join(home, "project")
	global := filepath.Join(home, ".factory")
	writeDroidAuthFile(t, filepath.Join(global, "config.json"), `{
		"model":"custom:legacy", "custom_models":[
			{"id":"custom:legacy","model":"legacy-model","base_url":"https://legacy.example.test/v1","api_key":"legacy-key","provider":"openai"}
		]}`)
	if got := scopedDroidStatus(t, ports.AgentAuthCheck{WorkingDir: workspace}); got != ports.AgentAuthStatusConfigured {
		t.Fatalf("legacy status = %q, want configured", got)
	}

	writeDroidAuthFile(t, filepath.Join(global, "settings.json"), `{
		"model":"custom:global", "customModels":[
			{"id":"custom:global","model":"global-model","baseUrl":"https://global.example.test/v1","apiKey":"global-key","provider":"openai"}
		]}`)
	writeDroidAuthFile(t, filepath.Join(global, "settings.local.json"), `{"model":"custom:global"}`)
	if got := scopedDroidStatus(t, ports.AgentAuthCheck{WorkingDir: workspace}); got != ports.AgentAuthStatusConfigured {
		t.Fatalf("global status = %q, want configured", got)
	}

	writeDroidAuthFile(t, filepath.Join(workspace, ".factory", "settings.json"), `{
		"model":"custom:project", "customModels":[
			{"id":"custom:project","model":"project-model","baseUrl":"https://project.example.test/v1","provider":"anthropic"}
		]}`)
	if got := scopedDroidStatus(t, ports.AgentAuthCheck{WorkingDir: workspace}); got != ports.AgentAuthStatusNotApplicable {
		t.Fatalf("project status = %q, want not_applicable", got)
	}

	writeDroidAuthFile(t, filepath.Join(workspace, ".factory", "settings.local.json"), `{
		"customModels":[
			{"id":"custom:project","model":"project-model","baseUrl":"https://project.example.test/v1","apiKey":"project-key","provider":"anthropic"}
		]}`)
	if got := scopedDroidStatus(t, ports.AgentAuthCheck{WorkingDir: workspace}); got != ports.AgentAuthStatusConfigured {
		t.Fatalf("project local status = %q, want configured", got)
	}

	runtimeSettings := filepath.Join(home, "runtime-settings.json")
	writeDroidAuthFile(t, runtimeSettings, `{
		"model":"custom:runtime", "customModels":[
			{"id":"custom:runtime","model":"runtime-model","baseUrl":"http://127.0.0.1:11434/v1","provider":"generic-chat-completion-api"}
		]}`)
	if got := scopedDroidStatus(t, ports.AgentAuthCheck{WorkingDir: workspace, Args: []string{"droid", "--settings", runtimeSettings}}); got != ports.AgentAuthStatusNotApplicable {
		t.Fatalf("runtime status = %q, want not_applicable", got)
	}
}

func TestDroidOnlyActiveCustomModelDeterminesStatus(t *testing.T) {
	home := isolateDroidAuth(t)
	writeDroidAuthFile(t, filepath.Join(home, ".factory", "auth.v2.file"), "encrypted-login")
	writeDroidAuthFile(t, filepath.Join(home, ".factory", "auth.v2.key"), "wrapped-key")
	writeDroidAuthFile(t, filepath.Join(home, ".factory", "settings.json"), `{
		"customModels":[
			{"id":"custom:ready","model":"ready","baseUrl":"https://ready.example.test/v1","apiKey":"ready-key","provider":"openai"},
			{"id":"custom:missing","model":"missing","baseUrl":"https://missing.example.test/v1","apiKey":"${MISSING_KEY}","provider":"anthropic"}
		]}`)
	if got := scopedDroidStatus(t, ports.AgentAuthCheck{Config: ports.AgentConfig{Model: "custom:missing"}}); got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want unknown", got)
	}
}

func TestDroidCustomProviderEvidence(t *testing.T) {
	cases := []struct {
		name, model string
		env         map[string]string
		settings    string
		want        ports.AgentAuthStatus
	}{
		{name: "environment key", model: "custom:gateway", env: map[string]string{"GATEWAY_KEY": "test-key"}, settings: `{"customModels":[{"id":"custom:gateway","model":"gateway","baseUrl":"https://gateway.example.test/v1","apiKey":"${GATEWAY_KEY}","provider":"openai"}]}`, want: ports.AgentAuthStatusConfigured},
		{name: "unresolved environment key", model: "custom:gateway", settings: `{"customModels":[{"id":"custom:gateway","model":"gateway","baseUrl":"https://gateway.example.test/v1","apiKey":"${GATEWAY_KEY}","provider":"openai"}]}`, want: ports.AgentAuthStatusUnknown},
		{name: "untrusted helper ignored", model: "custom:gateway", settings: `{"customModels":[{"id":"custom:gateway","model":"gateway","baseUrl":"https://gateway.example.test/v1","apiKeyHelper":"touch MARKER","provider":"anthropic"}]}`, want: ports.AgentAuthStatusNotApplicable},
		{name: "static headers", model: "custom:gateway", settings: `{"customModels":[{"id":"custom:gateway","model":"gateway","baseUrl":"https://gateway.example.test/v1","extraHeaders":{"Authorization":"Bearer fixed"},"provider":"generic-chat-completion-api"}]}`, want: ports.AgentAuthStatusConfigured},
		{name: "trusted keyless endpoint", model: "custom:gateway", settings: `{"customModels":[{"id":"custom:gateway","model":"gateway","baseUrl":"https://gateway.example.test/v1","provider":"generic-chat-completion-api"}]}`, want: ports.AgentAuthStatusNotApplicable},
		{name: "keyless endpoint not selected", model: "factory/default", settings: `{"customModels":[{"id":"custom:gateway","model":"gateway","baseUrl":"https://gateway.example.test/v1","provider":"generic-chat-completion-api"}]}`, want: ports.AgentAuthStatusUnknown},
		{name: "invalid provider", model: "custom:gateway", settings: `{"customModels":[{"id":"custom:gateway","model":"gateway","baseUrl":"https://gateway.example.test/v1","apiKey":"key","provider":"mystery"}]}`, want: ports.AgentAuthStatusUnknown},
		{name: "invalid base URL", model: "custom:gateway", settings: `{"customModels":[{"id":"custom:gateway","model":"gateway","baseUrl":"://broken","apiKey":"key","provider":"openai"}]}`, want: ports.AgentAuthStatusUnknown},
		{name: "unrelated nested key", model: "custom:gateway", settings: `{"customModels":[{"id":"custom:gateway","model":"gateway","baseUrl":"https://gateway.example.test/v1","provider":"openai","metadata":{"apiKey":"unrelated"}}]}`, want: ports.AgentAuthStatusNotApplicable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := isolateDroidAuth(t)
			marker := filepath.Join(home, "must-not-exist")
			settings := strings.ReplaceAll(tc.settings, "MARKER", marker)
			writeDroidAuthFile(t, filepath.Join(home, ".factory", "settings.json"), settings)
			got := scopedDroidStatus(t, ports.AgentAuthCheck{Config: ports.AgentConfig{Model: tc.model}, Env: tc.env})
			if got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("apiKeyHelper was executed: %v", err)
			}
		})
	}
}

func TestDroidBedrockUsesAWSDefaultChain(t *testing.T) {
	home := isolateDroidAuth(t)
	writeDroidAuthFile(t, filepath.Join(home, ".aws", "credentials"), "[work]\naws_access_key_id=test\naws_secret_access_key=test\n")
	writeDroidAuthFile(t, filepath.Join(home, ".factory", "settings.json"), `{
		"model":"custom:bedrock", "customModels":[
			{"id":"custom:bedrock","model":"anthropic.claude","provider":"anthropic","apiKey":"not-used-for-bedrock","bedrock":{"awsProfile":"work","awsRegion":"us-east-1"}}
		]}`)
	if got := scopedDroidStatus(t, ports.AgentAuthCheck{}); got != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, want configured", got)
	}
}

func TestDroidIgnoresAPIKeyHelperOutsideOrgManagedSettings(t *testing.T) {
	tests := []struct {
		name string
		path func(home, workspace string) string
		args func(path string) []string
	}{
		{name: "user", path: func(home, _ string) string { return filepath.Join(home, ".factory", "settings.json") }},
		{name: "project", path: func(_, workspace string) string { return filepath.Join(workspace, ".factory", "settings.json") }},
		{name: "runtime", path: func(home, _ string) string { return filepath.Join(home, "runtime.json") }, args: func(path string) []string { return []string{"droid", "--settings", path} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := isolateDroidAuth(t)
			workspace := filepath.Join(home, "workspace")
			path := tt.path(home, workspace)
			marker := filepath.Join(home, "must-not-exist")
			writeDroidAuthFile(t, path, `{"model":"custom:helper","customModels":[{"id":"custom:helper","model":"helper","baseUrl":"https://gateway.example.test/v1","apiKeyHelper":"touch `+marker+`","provider":"anthropic"}]}`)
			var args []string
			if tt.args != nil {
				args = tt.args(path)
			}
			if got := scopedDroidStatus(t, ports.AgentAuthCheck{WorkingDir: workspace, Args: args}); got != ports.AgentAuthStatusNotApplicable {
				t.Fatalf("status = %q, want not_applicable", got)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("apiKeyHelper was executed: %v", err)
			}
		})
	}
}

func TestDroidLegacyConfigDoesNotExpandEnvironmentValues(t *testing.T) {
	home := isolateDroidAuth(t)
	t.Setenv("LEGACY_KEY", "resolved-key")
	writeDroidAuthFile(t, filepath.Join(home, ".factory", "config.json"), `{
		"model":"custom:legacy", "custom_models":[
			{"id":"custom:legacy","model":"legacy-model","base_url":"https://legacy.example.test/v1","api_key":"${LEGACY_KEY}","provider":"openai"}
		]}`)
	if got := scopedDroidStatus(t, ports.AgentAuthCheck{}); got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want unknown", got)
	}
}

func TestDroidBedrockConverseAndEndpointValidation(t *testing.T) {
	tests := []struct {
		name, provider, endpoint string
		want                     ports.AgentAuthStatus
	}{
		{name: "bedrock converse", provider: "bedrock-converse", want: ports.AgentAuthStatusConfigured},
		{name: "valid endpoint", provider: "anthropic", endpoint: "https://bedrock.example.test", want: ports.AgentAuthStatusConfigured},
		{name: "invalid endpoint", provider: "anthropic", endpoint: "://broken", want: ports.AgentAuthStatusUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := isolateDroidAuth(t)
			writeDroidAuthFile(t, filepath.Join(home, ".aws", "credentials"), "[work]\naws_access_key_id=test\naws_secret_access_key=test\n")
			writeDroidAuthFile(t, filepath.Join(home, ".factory", "settings.json"), `{
				"model":"custom:bedrock", "customModels":[
					{"id":"custom:bedrock","model":"anthropic.claude","provider":"`+tt.provider+`","bedrock":{"awsProfile":"work","awsRegion":"us-east-1","bedrockBaseUrl":"`+tt.endpoint+`"}}
				]}`)
			if got := scopedDroidStatus(t, ports.AgentAuthCheck{}); got != tt.want {
				t.Fatalf("status = %q, want %q", got, tt.want)
			}
		})
	}
}
