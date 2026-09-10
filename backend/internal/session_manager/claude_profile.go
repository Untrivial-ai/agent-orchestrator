package sessionmanager

import (
	"maps"
	"os"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Resolve once when the session is created, including legacy project/daemon
// environment selection. Empty explicitly selects Claude's default directory.
func resolvedClaudeProfile(config domain.AgentConfig, env map[string]string) *string {
	value := os.Getenv("CLAUDE_CONFIG_DIR")
	if selected, ok := env["CLAUDE_CONFIG_DIR"]; ok {
		value = selected
	}
	if config.ClaudeConfigDir != nil {
		value = *config.ClaudeConfigDir
	}
	return &value
}

func sessionProfileEnv(rec domain.SessionRecord, config domain.ProjectConfig) map[string]string {
	env := maps.Clone(config.Env)
	if env == nil {
		env = make(map[string]string)
	}
	selected := rec.Metadata.ClaudeConfigDir
	if selected == nil {
		selected = resolvedClaudeProfile(effectiveAgentConfig(rec.Kind, config), config.Env)
	}
	env["CLAUDE_CONFIG_DIR"] = *selected
	return env
}
