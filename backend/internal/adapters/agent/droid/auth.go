package droid

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus returns the plugin's local authentication status.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	status, ok, err := droidLocalAuthStatus(ctx)
	if err != nil || ok {
		return status, err
	}
	return ports.AgentAuthStatusUnknown, nil
}

func droidLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if strings.TrimSpace(os.Getenv("FACTORY_API_KEY")) != "" {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if home == "" {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	return droidFactoryAuthStatus(filepath.Join(home, ".factory"))
}

func droidFactoryAuthStatus(factoryDir string) (ports.AgentAuthStatus, bool, error) {
	if factoryDir == "" {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	return droidSettingsAuthStatus(filepath.Join(factoryDir, "settings.json"))
}

func droidSettingsAuthStatus(path string) (ports.AgentAuthStatus, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	var settings struct {
		CustomModels []struct {
			Model   string `json:"model"`
			BaseURL string `json:"baseUrl"`
			APIKey  string `json:"apiKey"`
		} `json:"customModels"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	for _, model := range settings.CustomModels {
		if strings.TrimSpace(model.Model) != "" &&
			strings.TrimSpace(model.BaseURL) != "" &&
			droidConfiguredSecret(model.APIKey) {
			return ports.AgentAuthStatusAuthorized, true, nil
		}
	}
	if len(settings.CustomModels) > 0 {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

// droidConfiguredSecret recognizes literal keys and the documented
// ${ENV_VAR} interpolation used by Droid's custom model settings. An
// unresolved placeholder is not evidence that the user is authenticated.
func droidConfiguredSecret(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if strings.HasPrefix(value, "${") && strings.HasSuffix(value, "}") {
		name := strings.TrimSpace(value[2 : len(value)-1])
		return name != "" && strings.TrimSpace(os.Getenv(name)) != ""
	}
	return !strings.HasPrefix(value, "$")
}
