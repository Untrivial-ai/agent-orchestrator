package cline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus returns the plugin's local authentication status.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	_, err := p.ResolveBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if strings.TrimSpace(os.Getenv("CLINE_API_KEY")) != "" {
		return ports.AgentAuthStatusAuthorized, nil
	}
	if status, ok, err := clineProviderAuthStatus(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	} else if ok {
		return status, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

type clineProvidersFile struct {
	LastUsedProvider string                   `json:"lastUsedProvider"`
	Providers        map[string]clineProvider `json:"providers"`
}

type clineProvider struct {
	Settings clineProviderSettings `json:"settings"`
}

type clineProviderSettings struct {
	APIKey    string             `json:"apiKey"`
	APIKeyAlt string             `json:"apikey"`
	Auth      *clineProviderAuth `json:"auth"`
	Provider  string             `json:"provider"`
}

type clineProviderAuth struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    int64  `json:"expiresAt"`
}

func clineProviderAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
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
	path := filepath.Join(home, ".cline", "data", "settings", "providers.json")
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

	var file clineProvidersFile
	if err := json.Unmarshal(data, &file); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if len(file.Providers) == 0 {
		return ports.AgentAuthStatusUnknown, false, nil
	}

	if provider, ok := configuredClineProvider(file); ok {
		if providerAuthorized(provider.Settings) {
			return ports.AgentAuthStatusAuthorized, true, nil
		}
		return ports.AgentAuthStatusUnknown, false, nil
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

func configuredClineProvider(file clineProvidersFile) (clineProvider, bool) {
	if file.LastUsedProvider != "" {
		if provider, ok := file.Providers[file.LastUsedProvider]; ok {
			return provider, true
		}
	}
	for _, provider := range file.Providers {
		return provider, true
	}
	return clineProvider{}, false
}

func providerAuthorized(settings clineProviderSettings) bool {
	if strings.TrimSpace(settings.APIKey) != "" || strings.TrimSpace(settings.APIKeyAlt) != "" {
		return true
	}
	if settings.Auth == nil {
		return false
	}
	if strings.TrimSpace(settings.Auth.RefreshToken) != "" {
		return true
	}
	if strings.TrimSpace(settings.Auth.AccessToken) == "" {
		return false
	}
	if settings.Auth.ExpiresAt > 0 && settings.Auth.ExpiresAt <= time.Now().UnixMilli() {
		return false
	}
	return true
}
