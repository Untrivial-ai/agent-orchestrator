package autohand

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus returns the plugin's local authentication status.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	for _, name := range []string{
		"AUTOHAND_API_KEY", "AUTOHAND_AUTH_TOKEN", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY",
		"GOOGLE_API_KEY", "OPENROUTER_API_KEY", "MISTRAL_API_KEY", "GROQ_API_KEY",
	} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return ports.AgentAuthStatusAuthorized, nil
		}
	}
	status, err := autohandConfigAuthStatus(autohandConfigPath())
	if err != nil || status != ports.AgentAuthStatusUnknown {
		return status, err
	}
	// The documented CLI flow prompts for browser sign-in on first use; it does
	// not expose a stable non-interactive `auth status` command.
	return ports.AgentAuthStatusUnknown, nil
}

func autohandConfigAuthStatus(configPath string) (ports.AgentAuthStatus, error) {
	data, err := os.ReadFile(configPath) //nolint:gosec // path is the user's own Autohand config
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ports.AgentAuthStatusUnknown, nil
		}
		return ports.AgentAuthStatusUnknown, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return ports.AgentAuthStatusUnknown, nil
	}

	var config map[string]json.RawMessage
	if err := json.Unmarshal(data, &config); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}

	authReady, authKnown := autohandCloudAuthReady(config)
	if authReady {
		return ports.AgentAuthStatusAuthorized, nil
	}
	if authKnown {
		return ports.AgentAuthStatusUnknown, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

func autohandCloudAuthReady(config map[string]json.RawMessage) (ready, known bool) {
	authRaw, ok := config["auth"]
	if !ok {
		return false, false
	}
	var auth struct {
		Token        string `json:"token"`
		APIKeyHelper string `json:"apiKeyHelper"`
	}
	if err := json.Unmarshal(authRaw, &auth); err != nil {
		return false, false
	}
	// apiKeyHelper is a documented command which Autohand invokes to obtain an
	// API key. Its presence alone does not prove the command still exists or
	// yields a usable key, and AO must not execute arbitrary user commands for
	// an advisory probe.
	return usableSecret(auth.Token), true
}

func usableSecret(value string) bool {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return false
	}
	switch strings.ToLower(normalized) {
	case "api key", "apikey", "your api key", "your-api-key", "your_api_key", "token", "your token", "your-token", "your_token", "changeme", "change-me", "replace-me", "replace_me":
		return false
	default:
		return true
	}
}
