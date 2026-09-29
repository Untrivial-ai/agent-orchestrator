package unrealagent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestAuthStatusUsesSelectedProvider(t *testing.T) {
	tests := []struct {
		name, provider, key string
		want                ports.AgentAuthStatus
	}{
		{name: "default openai missing", want: ports.AgentAuthStatusUnauthorized},
		{name: "openai", provider: "openai", key: "OPENAI_API_KEY", want: ports.AgentAuthStatusAuthorized},
		{name: "openrouter", provider: "openrouter", key: "OPENROUTER_API_KEY", want: ports.AgentAuthStatusAuthorized},
		{name: "fireworks", provider: "fireworks", key: "FIREWORKS_API_KEY", want: ports.AgentAuthStatusAuthorized},
		{name: "ollama", provider: "ollama", want: ports.AgentAuthStatusAuthorized},
		{name: "unknown", provider: "custom", want: ports.AgentAuthStatusUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, name := range []string{"UNREAL_HARNESS_LLM_API_KEY", "OPENAI_API_KEY", "OPENROUTER_API_KEY", "FIREWORKS_API_KEY"} {
				t.Setenv(name, "")
			}
			t.Setenv("UNREAL_HARNESS_LLM_PROVIDER", test.provider)
			if test.key != "" {
				t.Setenv(test.key, "configured")
			}
			got, err := (&Plugin{resolvedBinary: "unreal-agent-runner"}).AuthStatus(context.Background())
			if err != nil || got != test.want {
				t.Fatalf("AuthStatus() = (%q, %v), want (%q, nil)", got, err, test.want)
			}
		})
	}
}

func TestAuthStatusFindsCodexAuthFile(t *testing.T) {
	authFile := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authFile, []byte(`{"tokens":{"access_token":"configured"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UNREAL_HARNESS_LLM_PROVIDER", "openai-codex")
	t.Setenv("OPENAI_CODEX_AUTH_FILE", authFile)
	got, err := (&Plugin{resolvedBinary: "unreal-agent-runner"}).AuthStatus(context.Background())
	if err != nil || got != ports.AgentAuthStatusAuthorized {
		t.Fatalf("AuthStatus() = (%q, %v)", got, err)
	}
}

func TestAuthStatusWithEnvUsesProjectProviderCredentials(t *testing.T) {
	for _, name := range []string{
		"UNREAL_HARNESS_LLM_PROVIDER", "UNREAL_HARNESS_LLM_API_KEY",
		"OPENAI_API_KEY", "OPENROUTER_API_KEY", "FIREWORKS_API_KEY",
	} {
		t.Setenv(name, "")
	}
	status, err := (&Plugin{resolvedBinary: "unreal-agent-runner"}).AuthStatusWithEnv(
		context.Background(),
		map[string]string{
			"UNREAL_HARNESS_LLM_PROVIDER": "openrouter",
			"OPENROUTER_API_KEY":          "project-key",
		},
	)
	if err != nil || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("AuthStatusWithEnv() = (%q, %v), want (%q, nil)", status, err, ports.AgentAuthStatusAuthorized)
	}
}
