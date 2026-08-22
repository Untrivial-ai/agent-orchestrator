package aider

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
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if status, ok, err := aiderLocalAuthStatus(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	} else if ok {
		return status, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

var aiderAPIKeyEnvVars = []string{
	"AIDER_API_KEY",
	"AIDER_OPENAI_API_KEY",
	"AIDER_ANTHROPIC_API_KEY",
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

func aiderLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	for _, name := range aiderAPIKeyEnvVars {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return ports.AgentAuthStatusAuthorized, true, nil
		}
	}

	for _, path := range aiderConfigPaths() {
		status, ok, err := aiderAuthStatusFromFile(path)
		if err != nil {
			return ports.AgentAuthStatusUnknown, false, err
		}
		if ok {
			return status, true, nil
		}
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

func aiderConfigPaths() []string {
	paths := []string{}
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		paths = append(paths,
			filepath.Join(cwd, ".env"),
			filepath.Join(cwd, ".aider.conf.yml"),
			filepath.Join(cwd, ".aider.conf.yaml"),
		)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths,
			filepath.Join(home, ".env"),
			filepath.Join(home, ".aider.conf.yml"),
			filepath.Join(home, ".aider.conf.yaml"),
		)
	}
	return paths
}

func aiderAuthStatusFromFile(path string) (ports.AgentAuthStatus, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	if fileContainsAPIKey(text) {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

func fileContainsAPIKey(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lower := strings.ToLower(line)
		listEntry := strings.HasPrefix(line, "-") && strings.Contains(line, "=")
		if !listEntry && (!strings.Contains(lower, "api") || !strings.Contains(lower, "key")) {
			continue
		}
		if valueAfterAssignment(line) != "" {
			return true
		}
	}
	return false
}

func valueAfterAssignment(line string) string {
	for _, sep := range []string{"=", ":"} {
		before, after, ok := strings.Cut(line, sep)
		if !ok || !strings.Contains(strings.ToLower(before), "key") {
			continue
		}
		value := strings.Trim(strings.TrimSpace(after), `"'`)
		if value != "" && !strings.EqualFold(value, "null") && !strings.EqualFold(value, "none") {
			return value
		}
	}
	// Aider's documented `api-key` YAML option is a list of provider=value
	// entries (for example `- gemini=...`), so it does not have the word
	// "key" on the left side of the assignment.
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "-") {
		if _, after, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(trimmed, "-")), "="); ok {
			value := strings.Trim(strings.TrimSpace(after), `"'`)
			if value != "" && !strings.EqualFold(value, "null") && !strings.EqualFold(value, "none") {
				return value
			}
		}
	}
	return ""
}
