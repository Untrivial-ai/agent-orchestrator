package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus returns the plugin's local authentication status.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	if status, ok, err := piLocalAuthStatus(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	} else if ok {
		return status, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

func piLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	for _, name := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY",
		"MISTRAL_API_KEY", "GROQ_API_KEY", "XAI_API_KEY", "OPENROUTER_API_KEY",
		"DEEPSEEK_API_KEY", "ZAI_API_KEY", "CEREBRAS_API_KEY", "KIMI_API_KEY",
	} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return ports.AgentAuthStatusAuthorized, true, nil
		}
	}
	configDir, ok := piConfigDir()
	if !ok {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	return piAuthJSONStatus(filepath.Join(configDir, "auth.json"))
}

func piConfigDir() (string, bool) {
	if configDir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); configDir != "" {
		return configDir, true
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	return filepath.Join(home, ".pi", "agent"), true
}

type piAuthEntry struct {
	Type string `json:"type"`
	Key  string `json:"key"`
}

func piAuthJSONStatus(path string) (ports.AgentAuthStatus, bool, error) {
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

	var entries map[string]piAuthEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if len(entries) == 0 {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	for provider, entry := range entries {
		if strings.TrimSpace(provider) == "" {
			continue
		}
		if piAuthKeyIsResolved(entry.Key) {
			return ports.AgentAuthStatusAuthorized, true, nil
		}
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

func piAuthKeyIsResolved(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" || strings.HasPrefix(key, "!") {
		// Avoid executing configured commands during a global auth probe. Their
		// result remains unknown until Pi resolves them while launching.
		return false
	}

	resolved := true
	expanded := os.Expand(key, func(name string) string {
		switch name {
		case "$", "!":
			// Pi documents $$ and $! as escaped literal prefixes.
			return name
		default:
			value, ok := os.LookupEnv(name)
			if !ok || strings.TrimSpace(value) == "" {
				resolved = false
			}
			return value
		}
	})
	return resolved && strings.TrimSpace(expanded) != ""
}
