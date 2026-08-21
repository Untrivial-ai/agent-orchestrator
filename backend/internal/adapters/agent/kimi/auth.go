package kimi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authprobe"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus returns the plugin's local authentication status.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	binary, err := p.ResolveBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if status, ok, err := kimiLocalAuthStatus(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	} else if ok {
		return status, nil
	}
	return authprobe.CLIStatus(ctx, binary, nil)
}

var kimiAPIKeyEnvVars = []string{
	"KIMI_API_KEY",
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
	home, ok := kimiCodeHome()
	if !ok {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	configStatus, configOK, err := kimiConfigAuthStatus(filepath.Join(home, "config.toml"))
	if err != nil || configStatus == ports.AgentAuthStatusAuthorized {
		return configStatus, configOK, err
	}
	credentialsStatus, credentialsOK, err := kimiCredentialsAuthStatus(kimiCredentialsPath(home))
	if err != nil || credentialsOK {
		return credentialsStatus, credentialsOK, err
	}
	if configOK {
		return configStatus, configOK, nil
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

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

// kimiCredentialsPath returns the device-code OAuth credential file inside a
// Kimi Code home. OAuth-authenticated profiles keep their tokens here and leave
// every config.toml api_key empty.
func kimiCredentialsPath(home string) string {
	return filepath.Join(home, "credentials", "kimi-code.json")
}

var kimiAPIKeyLineRE = regexp.MustCompile(`(?m)^\s*api_key\s*=\s*("([^"]*)"|'([^']*)'|([^\s#]+))`)

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
	matches := kimiAPIKeyLineRE.FindAllStringSubmatch(string(data), -1)
	if len(matches) == 0 {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	for _, match := range matches {
		for _, group := range match[2:] {
			if strings.TrimSpace(group) != "" {
				return ports.AgentAuthStatusAuthorized, true, nil
			}
		}
	}
	return ports.AgentAuthStatusUnauthorized, true, nil
}

func kimiCredentialsAuthStatus(path string) (ports.AgentAuthStatus, bool, error) {
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
	return ports.AgentAuthStatusUnauthorized, true, nil
}
