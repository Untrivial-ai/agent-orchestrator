package grok

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestGrokLocalAuthEvidence(t *testing.T) {
	for _, tt := range []struct {
		name, config, auth string
		env                map[string]string
		want               ports.AgentAuthStatus
	}{
		{name: "missing", want: ports.AgentAuthStatusUnknown},
		{name: "xai environment", env: map[string]string{"XAI_API_KEY": "xai-test"}, want: ports.AgentAuthStatusConfigured},
		{name: "deployment environment", env: map[string]string{"GROK_DEPLOYMENT_KEY": "deployment-test"}, want: ports.AgentAuthStatusConfigured},
		{name: "stale environment", env: map[string]string{"GROK_API_KEY": "stale"}, want: ports.AgentAuthStatusUnknown},
		{name: "stored key", auth: `{"https://auth.x.ai::account":{"key":"stored-test"}}`, want: ports.AgentAuthStatusConfigured},
		{name: "stale stored tokens", auth: `{"https://auth.x.ai::account":{"access_token":"stale","refresh_token":"stale"}}`, want: ports.AgentAuthStatusUnknown},
		{name: "empty stored key", auth: `{"https://auth.x.ai::account":{"key":" "}}`, want: ports.AgentAuthStatusUnknown},
		{name: "malformed auth", auth: `{"key":`, want: ports.AgentAuthStatusUnknown},
		{name: "inline auth", env: map[string]string{"GROK_AUTH": `{"https://auth.x.ai::account":{"key":"inline-test"}}`}, want: ports.AgentAuthStatusConfigured},
		{name: "malformed inline", env: map[string]string{"GROK_AUTH": "not-json"}, want: ports.AgentAuthStatusUnknown},
		{name: "malformed inline permits valid stored key", env: map[string]string{"GROK_AUTH": "not-json"}, auth: `{"https://auth.x.ai::account":{"key":"stored-test"}}`, want: ports.AgentAuthStatusConfigured},
		{name: "deployment config", config: "[endpoints]\ndeployment_key = 'deployment-test'\n", want: ports.AgentAuthStatusConfigured},
		{name: "default model key", config: "[model.grok-build]\napi_key = 'model-test'\n", want: ports.AgentAuthStatusConfigured},
		{name: "selected model key", config: "[models]\ndefault = 'custom'\n[model.custom]\napi_key = 'model-test'\n", want: ports.AgentAuthStatusConfigured},
		{name: "selected model env", config: "[models]\ndefault = 'custom'\n[model.custom]\nenv_key = 'GROK_TEST_KEY'\n", env: map[string]string{"GROK_TEST_KEY": "env-test"}, want: ports.AgentAuthStatusConfigured},
		{name: "missing selected model env", config: "[models]\ndefault = 'custom'\n[model.custom]\nenv_key = 'GROK_TEST_KEY'\n", want: ports.AgentAuthStatusUnknown},
		{name: "inactive model", config: "[model.inactive]\napi_key = 'unrelated'\n", want: ports.AgentAuthStatusUnknown},
		{name: "unrelated nested key", config: "[mcp_servers.unrelated]\napi_key = 'unrelated'\n", want: ports.AgentAuthStatusUnknown},
		{name: "custom provider excludes xai credential", config: "[models]\ndefault = 'custom'\n[model.custom]\nbase_url = 'https://other.example/v1'\n", env: map[string]string{"XAI_API_KEY": "unrelated"}, want: ports.AgentAuthStatusUnknown},
		{name: "custom provider excludes login", config: "[models]\ndefault = 'custom'\n[model.custom]\nbase_url = 'https://other.example/v1'\n", auth: `{"https://auth.x.ai::account":{"key":"unrelated"}}`, want: ports.AgentAuthStatusUnknown},
		{name: "malformed config permits environment", config: "[model", env: map[string]string{"XAI_API_KEY": "xai-test"}, want: ports.AgentAuthStatusConfigured},
		{name: "malformed config only", config: "[model", want: ports.AgentAuthStatusUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := grokTestHome(t)
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			if tt.config != "" {
				writeGrokFile(t, filepath.Join(home, "config.toml"), tt.config)
			}
			if tt.auth != "" {
				writeGrokFile(t, filepath.Join(home, "auth.json"), tt.auth)
			}
			status, ok, err := grokLocalAuthStatus(context.Background())
			if err != nil || status != tt.want || ok != (tt.want != ports.AgentAuthStatusUnknown) {
				t.Fatalf("status = (%q, %v, %v), want %q", status, ok, err, tt.want)
			}
		})
	}
}

func TestGrokAuthPathOverride(t *testing.T) {
	home := grokTestHome(t)
	writeGrokFile(t, filepath.Join(home, "auth.json"), `{"https://auth.x.ai::account":{"key":"default"}}`)
	path := filepath.Join(t.TempDir(), "auth.json")
	t.Setenv("GROK_AUTH_PATH", path)
	status, _, err := grokLocalAuthStatus(context.Background())
	if err != nil || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("missing override = %q, %v", status, err)
	}
	writeGrokFile(t, path, `{"https://auth.x.ai::account":{"key":"override"}}`)
	status, _, err = grokLocalAuthStatus(context.Background())
	if err != nil || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("override = %q, %v", status, err)
	}
}

func TestGrokScopedModelAndEnvironment(t *testing.T) {
	home := grokTestHome(t)
	writeGrokFile(t, filepath.Join(home, "config.toml"), "[model.grok-build]\napi_key = 'default-key'\n[model.custom]\nbase_url = 'https://other.example/v1'\nenv_key = 'GROK_TEST_KEY'\n")
	p := &Plugin{}
	checker, ok := any(p).(ports.AgentScopedAuthChecker)
	if !ok {
		t.Fatal("Grok must support scoped auth checks")
	}
	p.resolvedBinary = "/test/grok"
	for _, tt := range []struct {
		scope ports.AgentAuthCheck
		want  ports.AgentAuthStatus
	}{
		{ports.AgentAuthCheck{Config: ports.AgentConfig{Model: "custom"}}, ports.AgentAuthStatusUnknown},
		{ports.AgentAuthCheck{Config: ports.AgentConfig{Model: "custom"}, Env: map[string]string{"GROK_TEST_KEY": "scoped"}}, ports.AgentAuthStatusConfigured},
		{ports.AgentAuthCheck{Args: []string{"grok", "--model=custom"}}, ports.AgentAuthStatusUnknown},
		{ports.AgentAuthCheck{Env: map[string]string{"GROK_DEFAULT_MODEL": "custom"}}, ports.AgentAuthStatusUnknown},
		{ports.AgentAuthCheck{}, ports.AgentAuthStatusConfigured},
	} {
		status, err := checker.AuthStatusFor(context.Background(), tt.scope)
		if err != nil || status != tt.want {
			t.Fatalf("scoped status = %q, %v; want %q", status, err, tt.want)
		}
	}
}

func TestGrokAuthCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	status, _, err := grokLocalAuthStatus(ctx)
	if status != ports.AgentAuthStatusUnknown || err != context.Canceled {
		t.Fatalf("canceled status = %q, %v", status, err)
	}
}

func TestGrokScopedRelativeAuthPath(t *testing.T) {
	grokTestHome(t)
	workspace := t.TempDir()
	writeGrokFile(t, filepath.Join(workspace, "auth.json"), `{"https://auth.x.ai::account":{"key":"scoped"}}`)
	status, err := grokAuthStatus(context.Background(), ports.AgentAuthCheck{WorkingDir: workspace, Env: map[string]string{"GROK_AUTH_PATH": "auth.json"}})
	if err != nil || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("relative auth path = %q, %v", status, err)
	}
}

func grokTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, key := range []string{"XAI_API_KEY", "GROK_API_KEY", "GROK_AUTH", "GROK_AUTH_PATH", "GROK_DEPLOYMENT_KEY", "GROK_DEFAULT_MODEL", "GROK_TEST_KEY"} {
		t.Setenv(key, "")
	}
	grokHome := t.TempDir()
	t.Setenv("GROK_HOME", grokHome)
	return grokHome
}

func writeGrokFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
