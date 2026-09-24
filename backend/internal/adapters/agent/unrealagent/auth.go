package unrealagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus reports whether the selected provider has credentials available.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.authStatus(ctx, os.Getenv)
}

// AuthStatusWithEnv checks the exact environment that will be supplied to the
// provider, including project-scoped overrides that are not visible to the
// daemon process during the generic readiness probe.
func (p *Plugin) AuthStatusWithEnv(ctx context.Context, env map[string]string) (ports.AgentAuthStatus, error) {
	getenv := func(name string) string {
		if value, ok := env[name]; ok {
			return value
		}
		return os.Getenv(name)
	}
	return p.authStatus(ctx, getenv)
}

func (p *Plugin) authStatus(ctx context.Context, getenv func(string) string) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	provider := strings.TrimSpace(getenv("UNREAL_HARNESS_LLM_PROVIDER"))
	if provider == "" {
		provider = "openai"
	}
	genericKey := envSet(getenv, "UNREAL_HARNESS_LLM_API_KEY")
	switch provider {
	case "ollama":
		return ports.AgentAuthStatusAuthorized, nil
	case "openai":
		return credentialStatus(genericKey || envSet(getenv, "OPENAI_API_KEY")), nil
	case "openrouter":
		return credentialStatus(genericKey || envSet(getenv, "OPENROUTER_API_KEY")), nil
	case "fireworks":
		return credentialStatus(genericKey || envSet(getenv, "FIREWORKS_API_KEY")), nil
	case "openai-codex":
		return credentialStatus(envSet(getenv, "OPENAI_CODEX_ACCESS_TOKEN") || codexAuthFilePresent(getenv)), nil
	default:
		return ports.AgentAuthStatusUnknown, nil
	}
}

func credentialStatus(present bool) ports.AgentAuthStatus {
	if present {
		return ports.AgentAuthStatusAuthorized
	}
	return ports.AgentAuthStatusUnauthorized
}

func envSet(getenv func(string) string, name string) bool {
	return strings.TrimSpace(getenv(name)) != ""
}

func codexAuthFilePresent(getenv func(string) string) bool {
	path := strings.TrimSpace(getenv("OPENAI_CODEX_AUTH_FILE"))
	if path == "" {
		home := strings.TrimSpace(getenv("CODEX_HOME"))
		if home == "" {
			var err error
			home, err = os.UserHomeDir()
			if err != nil || home == "" {
				return false
			}
			home = filepath.Join(home, ".codex")
		}
		path = filepath.Join(home, "auth.json")
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}
