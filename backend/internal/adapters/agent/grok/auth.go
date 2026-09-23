package grok

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus returns the plugin's local authentication status.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	_, err := p.ResolveBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if status, ok, err := grokLocalAuthStatus(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	} else if ok {
		return status, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

func grokLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if strings.TrimSpace(os.Getenv("XAI_API_KEY")) != "" {
		return ports.AgentAuthStatusAuthorized, true, nil
	}

	grokHome, err := grokConfigDir()
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if grokHome == "" {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	configStatus, configOK, err := grokConfigAuthStatus(filepath.Join(grokHome, "config.toml"))
	if err != nil || configOK {
		return configStatus, configOK, err
	}
	path := filepath.Join(grokHome, "auth.json")
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

	var entries map[string]json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if len(entries) == 0 {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	for key, value := range entries {
		if strings.TrimSpace(key) == "" {
			continue
		}
		var session struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		}
		if json.Unmarshal(value, &session) == nil &&
			(strings.TrimSpace(session.AccessToken) != "" || strings.TrimSpace(session.RefreshToken) != "") {
			return ports.AgentAuthStatusAuthorized, true, nil
		}
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

// grokConfigDir follows Grok Build's GROK_HOME override for all local state.
func grokConfigDir() (string, error) {
	if home := strings.TrimSpace(os.Getenv("GROK_HOME")); home != "" {
		return home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if home == "" {
		return "", nil
	}
	return filepath.Join(home, ".grok"), nil
}

// grokConfigAuthStatus recognizes the documented per-model api_key and
// env_key settings. Other config values (model IDs, base URLs, etc.) are not
// evidence of authentication.
func grokConfigAuthStatus(path string) (ports.AgentAuthStatus, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	var config map[string]any
	if err := toml.Unmarshal(data, &config); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if hasGrokConfiguredSecret(config) {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

func hasGrokConfiguredSecret(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			switch key {
			case "api_key":
				if s, ok := child.(string); ok && strings.TrimSpace(s) != "" && !strings.HasPrefix(strings.TrimSpace(s), "$") {
					return true
				}
			case "env_key":
				if name, ok := child.(string); ok && strings.TrimSpace(os.Getenv(strings.TrimSpace(name))) != "" {
					return true
				}
			}
			if hasGrokConfiguredSecret(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if hasGrokConfiguredSecret(child) {
				return true
			}
		}
	}
	return false
}
