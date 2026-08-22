package goose

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"

	yaml "gopkg.in/yaml.v3"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus returns the plugin's local authentication status.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	_, err := p.ResolveBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if status, ok, err := gooseLocalAuthStatus(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	} else if ok {
		return status, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

var gooseAPIKeyEnvVars = []string{
	"GOOSE_API_KEY",
	"GOOSE_PROVIDER__API_KEY",
	"GOOSE_EDITOR_API_KEY",
	"OPENAI_API_KEY",
	"ANTHROPIC_API_KEY",
	"GEMINI_API_KEY",
	"GOOGLE_API_KEY",
	"OPENROUTER_API_KEY",
	"DEEPSEEK_API_KEY",
	"GROQ_API_KEY",
	"XAI_API_KEY",
	"MISTRAL_API_KEY",
	"COHERE_API_KEY",
}

func gooseLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	for _, name := range gooseAPIKeyEnvVars {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return ports.AgentAuthStatusAuthorized, true, nil
		}
	}

	for _, path := range gooseConfigPaths() {
		status, ok, err := gooseAuthStatusFromConfig(path)
		if err != nil {
			return ports.AgentAuthStatusUnknown, false, err
		}
		if ok {
			return status, true, nil
		}
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

func gooseConfigPaths() []string {
	seen := map[string]struct{}{}
	paths := []string{}
	add := func(path string) {
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}

	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		add(filepath.Join(xdg, "goose", "config.yaml"))
		add(filepath.Join(xdg, "goose", "secrets.yaml"))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		// Goose stores config here on macOS as well, rather than under
		// os.UserConfigDir's "Application Support" path.
		add(filepath.Join(home, ".config", "goose", "config.yaml"))
		// Goose falls back to this plaintext secret store when its keyring is
		// disabled or unavailable. Keyring-only credentials remain unknown.
		add(filepath.Join(home, ".config", "goose", "secrets.yaml"))
	}
	return paths
}

func gooseAuthStatusFromConfig(path string) (ports.AgentAuthStatus, bool, error) {
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

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if gooseConfigHasCredential(&root) {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

func gooseConfigHasCredential(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	switch node.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range node.Content {
			if gooseConfigHasCredential(child) {
				return true
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := strings.ToLower(strings.TrimSpace(node.Content[i].Value))
			value := strings.Trim(strings.TrimSpace(node.Content[i+1].Value), `"'`)
			if (strings.Contains(key, "api_key") || strings.Contains(key, "apikey") || strings.Contains(key, "token")) &&
				value != "" &&
				!strings.EqualFold(value, "null") &&
				!strings.EqualFold(value, "none") {
				return true
			}
			if gooseConfigHasCredential(node.Content[i+1]) {
				return true
			}
		}
	}
	return false
}
