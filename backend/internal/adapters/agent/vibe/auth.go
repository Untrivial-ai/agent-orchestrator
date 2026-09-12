package vibe

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus returns the plugin's local authentication status.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	_, err := p.ResolveBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if status, ok, err := vibeLocalAuthStatus(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	} else if ok {
		return status, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

const (
	// This names the default env var Vibe reads; it is not a credential value.
	vibeDefaultAPIKeyEnvVar = "MISTRAL_API_KEY" //nolint:gosec // env var name, not a credential value
)

func vibeLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if home == "" {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	vibeHome := os.Getenv("VIBE_HOME")
	if strings.TrimSpace(vibeHome) == "" {
		vibeHome = filepath.Join(home, ".vibe")
	}

	// AuthStatus is a catalog-level probe and has no session workspace. Do not
	// inspect a project .vibe/config.toml from the daemon's current directory:
	// it could belong to an unrelated checkout and report its credentials for
	// every session. The global Vibe config remains valid evidence here.
	envVars, err := vibeAPIKeyEnvVars(filepath.Join(vibeHome, "config.toml"))
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	for _, envVar := range envVars {
		if strings.TrimSpace(os.Getenv(envVar)) != "" {
			return ports.AgentAuthStatusAuthorized, true, nil
		}
		if status, ok, err := vibeEnvFileAuthStatus(filepath.Join(vibeHome, ".env"), envVar); err != nil || ok {
			return status, ok, err
		}
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

func vibeAPIKeyEnvVars(configPath string) ([]string, error) {
	vars := []string{vibeDefaultAPIKeyEnvVar, "VIBE_CODE_API_KEY"}
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return vars, nil
	}
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.TrimSpace(key) != "api_key_env_var" {
			continue
		}
		envVar := strings.Trim(strings.TrimSpace(value), `"',`)
		if envVar != "" && !strings.EqualFold(envVar, "null") && !containsString(vars, envVar) {
			vars = append(vars, envVar)
		}
	}
	return vars, nil
}

func vibeEnvFileAuthStatus(path, envVar string) (ports.AgentAuthStatus, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != envVar {
			continue
		}
		if strings.Trim(strings.TrimSpace(value), `"'`) != "" {
			return ports.AgentAuthStatusAuthorized, true, nil
		}
		return ports.AgentAuthStatusUnknown, false, nil
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
