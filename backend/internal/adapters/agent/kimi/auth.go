package kimi

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
	if status, ok, err := kimiLocalAuthStatus(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	} else if ok {
		return status, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

var kimiAPIKeyEnvVars = []string{
	"KIMI_API_KEY",
	"OPENAI_API_KEY",
	// Legacy Kimi Code distributions also accepted these names.
	"KIMI_CODE_API_KEY",
	"MOONSHOT_API_KEY",
}

func kimiLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	for _, name := range kimiAPIKeyEnvVars {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return ports.AgentAuthStatusAuthorized, true, nil
		}
	}
	homes, ok := kimiAuthHomes()
	if !ok {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	for _, home := range homes {
		for _, configName := range []string{"config.toml", "config.json"} {
			status, found, err := kimiConfigAuthStatus(filepath.Join(home, configName))
			if err != nil || found {
				return status, found, err
			}
		}
		// Legacy Kimi Code stored its hosted OAuth token at this fixed path.
		status, found, err := kimiCredentialsAuthStatus(filepath.Join(home, "credentials", "kimi-code.json"))
		if err != nil || found {
			return status, found, err
		}
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

func kimiAuthHomes() ([]string, bool) {
	userHome, err := os.UserHomeDir()
	if err != nil && strings.TrimSpace(os.Getenv("KIMI_SHARE_DIR")) == "" &&
		strings.TrimSpace(os.Getenv(kimiCodeHomeEnv)) == "" {
		return nil, false
	}

	candidates := []string{
		strings.TrimSpace(os.Getenv("KIMI_SHARE_DIR")),
		strings.TrimSpace(os.Getenv(kimiCodeHomeEnv)),
	}
	if candidates[0] == "" && userHome != "" {
		candidates[0] = filepath.Join(userHome, ".kimi")
	}
	if candidates[1] == "" && userHome != "" {
		candidates[1] = filepath.Join(userHome, ".kimi-code")
	}

	homes := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		clean := filepath.Clean(candidate)
		if _, exists := seen[clean]; exists {
			continue
		}
		seen[clean] = struct{}{}
		homes = append(homes, clean)
	}
	return homes, len(homes) > 0
}

// kimiCodeHome retains the legacy KIMI_CODE_HOME lookup used by runtime hook
// isolation. Auth detection additionally checks the current KIMI_SHARE_DIR.
func kimiCodeHome() (string, bool) {
	if home := strings.TrimSpace(os.Getenv(kimiCodeHomeEnv)); home != "" {
		return home, true
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	return filepath.Join(home, ".kimi-code"), true
}

type kimiOAuthRef struct {
	Storage string `json:"storage" toml:"storage"`
	Key     string `json:"key" toml:"key"`
}

type kimiCredentialSource struct {
	APIKey string            `json:"api_key" toml:"api_key"`
	Env    map[string]string `json:"env" toml:"env"`
	OAuth  *kimiOAuthRef     `json:"oauth" toml:"oauth"`
}

type kimiAuthConfig struct {
	Providers map[string]kimiCredentialSource `json:"providers" toml:"providers"`
}

func kimiConfigAuthStatus(path string) (ports.AgentAuthStatus, bool, error) {
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
	var config kimiAuthConfig
	var decodeErr error
	if strings.EqualFold(filepath.Ext(path), ".json") {
		decodeErr = json.Unmarshal(data, &config)
	} else {
		decodeErr = toml.Unmarshal(data, &config)
	}
	if decodeErr != nil {
		return ports.AgentAuthStatusUnknown, false, decodeErr
	}
	for _, provider := range config.Providers {
		if strings.TrimSpace(provider.APIKey) != "" || kimiProviderEnvHasCredential(provider.Env) {
			return ports.AgentAuthStatusAuthorized, true, nil
		}
		if provider.OAuth == nil || strings.TrimSpace(provider.OAuth.Key) == "" {
			continue
		}
		credentialPath := kimiOAuthCredentialPath(filepath.Dir(path), provider.OAuth.Key)
		status, found, err := kimiCredentialsAuthStatus(credentialPath)
		if err != nil || found {
			return status, found, err
		}
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

func kimiProviderEnvHasCredential(env map[string]string) bool {
	for name, value := range env {
		normalized := strings.ToUpper(strings.TrimSpace(name))
		if strings.TrimSpace(value) != "" &&
			(strings.HasSuffix(normalized, "_API_KEY") || strings.HasSuffix(normalized, "_TOKEN")) {
			return true
		}
	}
	return false
}

func kimiOAuthCredentialPath(home, key string) string {
	name := strings.TrimPrefix(strings.TrimSpace(key), "oauth/")
	name = filepath.Base(filepath.FromSlash(name))
	if name == "" || name == "." {
		return ""
	}
	return filepath.Join(home, "credentials", name+".json")
}

func kimiCredentialsAuthStatus(path string) (ports.AgentAuthStatus, bool, error) {
	if strings.TrimSpace(path) == "" {
		return ports.AgentAuthStatusUnknown, false, nil
	}
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

	var credentials struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(data, &credentials); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if strings.TrimSpace(credentials.AccessToken) != "" ||
		strings.TrimSpace(credentials.RefreshToken) != "" {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	return ports.AgentAuthStatusUnknown, false, nil
}
