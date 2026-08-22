package copilot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authprobe"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus returns the plugin's local authentication status.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	_, err := p.ResolveBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if status, ok, err := copilotLocalAuthStatus(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	} else if ok {
		return status, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

var copilotTokenEnvVars = []string{
	"COPILOT_GITHUB_TOKEN",
	"GH_TOKEN",
	"GITHUB_TOKEN",
}

func copilotLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	for _, name := range copilotTokenEnvVars {
		if copilotUsableToken(os.Getenv(name)) {
			return ports.AgentAuthStatusAuthorized, true, nil
		}
	}
	if copilotBYOKConfigured() {
		return ports.AgentAuthStatusAuthorized, true, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if home == "" {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	configStatus, configOK, err := copilotConfigAuthStatus(filepath.Join(copilotHomeDir(home), "config.json"))
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if configOK {
		return configStatus, true, nil
	}
	if status, ok, err := copilotGHAuthStatus(ctx); err != nil || ok {
		return status, ok, err
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

// copilotBYOKConfigured recognizes Copilot CLI's documented local BYOK setup.
// An API key is optional because providers such as local Ollama do not require
// one; the endpoint and model identify a usable provider configuration.
func copilotBYOKConfigured() bool {
	return strings.TrimSpace(os.Getenv("COPILOT_PROVIDER_BASE_URL")) != "" &&
		strings.TrimSpace(os.Getenv("COPILOT_MODEL")) != ""
}

func copilotHomeDir(home string) string {
	if path := strings.TrimSpace(os.Getenv("COPILOT_HOME")); path != "" {
		return path
	}
	return filepath.Join(home, ".copilot")
}

func copilotConfigAuthStatus(path string) (ports.AgentAuthStatus, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(data, &config); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	for _, key := range []string{"authToken", "accessToken", "token"} {
		var token string
		if err := json.Unmarshal(config[key], &token); err == nil && copilotUsableToken(token) {
			return ports.AgentAuthStatusAuthorized, true, nil
		}
	}
	if copilotLoggedInUser(config) {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

func copilotGHAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	status, err := authprobe.CLIStatus(ctx, "gh", [][]string{{"auth", "status"}})
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if status == ports.AgentAuthStatusAuthorized {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	// GitHub CLI state is only one Copilot credential source. A negative result
	// cannot rule out Copilot's own stored OAuth or token credentials.
	return ports.AgentAuthStatusUnknown, false, nil
}

func copilotUsableToken(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.HasPrefix(value, "ghp_")
}

func copilotLoggedInUser(config map[string]json.RawMessage) bool {
	var users map[string]json.RawMessage
	if err := json.Unmarshal(config["loggedInUsers"], &users); err != nil {
		return false
	}
	for _, user := range users {
		if len(user) > 0 && string(user) != "null" && string(user) != `""` {
			return true
		}
	}
	return false
}
