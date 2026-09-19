package agentcreds

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ClaudeSettings contains the effective inputs used for Claude discovery.
// It can contain credentials: keep it in memory and never log or serialize it.
type ClaudeSettings struct {
	Env   map[string]string `json:"-"`
	Model string            `json:"-"`
}

// Only these environment keys may be read from a Claude settings file. Other
// configuration, including executable helpers, is deliberately not interpreted.
var claudeSettingsEnvKeys = []string{
	"ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_SMALL_FAST_MODEL",
}

// These additional provider inputs are accepted only from the launch/process
// environment, never from settings files. Keeping them here gives credentials,
// model defaults, command context, and catalog fingerprints one set of inputs.
var claudeProviderEnvKeys = []string{
	"CLAUDE_CONFIG_DIR", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX",
	"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_FOUNDRY_BASE_URL", "ANTHROPIC_FOUNDRY_RESOURCE",
	"ANTHROPIC_FOUNDRY_API_KEY", "ANTHROPIC_FOUNDRY_AUTH_TOKEN",
	"AWS_REGION", "AWS_DEFAULT_REGION", "AWS_PROFILE", "AWS_CONFIG_FILE", "AWS_SHARED_CREDENTIALS_FILE",
	"AWS_BEARER_TOKEN_BEDROCK", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
	"ANTHROPIC_VERTEX_PROJECT_ID", "GOOGLE_CLOUD_PROJECT", "CLOUD_ML_REGION", "GOOGLE_CLOUD_REGION",
	"GOOGLE_OAUTH_ACCESS_TOKEN", "GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_CONFIG",
}

// ResolveClaudeSettings merges user < project < project-local settings, then
// explicit project/session environment. Process environment is the fallback.
// Missing, malformed, or oversized files are ignored without exposing contents.
func ResolveClaudeSettings(workingDir string, explicitEnv map[string]string, opts ResolveOptions) ClaudeSettings {
	resolved := ClaudeSettings{Env: make(map[string]string)}
	for _, keys := range [][]string{claudeSettingsEnvKeys, claudeProviderEnvKeys} {
		for _, key := range keys {
			if value := opts.env(key); value != "" {
				resolved.Env[key] = value
			}
		}
	}
	lookup := func(key string) string {
		if value, ok := explicitEnv[key]; ok {
			return strings.TrimSpace(value)
		}
		return opts.env(key)
	}
	pathOptions := opts
	pathOptions.Env = lookup
	pathOptions.WorkingDir = workingDir
	configDir, _ := claudeConfigDir(pathOptions)
	var paths []string
	if configDir != "" {
		paths = append(paths, filepath.Join(configDir, "settings.json"))
	}
	if dir := strings.TrimSpace(workingDir); dir != "" {
		paths = append(paths, filepath.Join(dir, ".claude", "settings.json"), filepath.Join(dir, ".claude", "settings.local.json"))
	}
	for _, path := range paths {
		settings := readClaudeSettings(path)
		if settings.Model != "" {
			resolved.Model = settings.Model
		}
		for key, value := range settings.Env {
			resolved.Env[key] = value
		}
	}
	for _, keys := range [][]string{claudeSettingsEnvKeys, claudeProviderEnvKeys} {
		for _, key := range keys {
			if value, ok := explicitEnv[key]; ok {
				resolved.Env[key] = strings.TrimSpace(value)
			}
		}
	}
	if _, configured := resolved.Env["CLAUDE_CONFIG_DIR"]; configured && configDir != "" {
		resolved.Env["CLAUDE_CONFIG_DIR"] = configDir
	}
	if model := resolved.Env["ANTHROPIC_MODEL"]; model != "" {
		resolved.Model = model
	}
	return resolved
}

func readClaudeSettings(path string) ClaudeSettings {
	file, err := os.Open(path) //nolint:gosec // path is a documented user/project settings location
	if err != nil {
		return ClaudeSettings{}
	}
	defer func() { _ = file.Close() }()
	const limit = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(raw) > limit {
		return ClaudeSettings{}
	}
	var payload struct {
		Model string                     `json:"model"`
		Env   map[string]json.RawMessage `json:"env"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return ClaudeSettings{}
	}
	resolved := ClaudeSettings{Model: strings.TrimSpace(payload.Model), Env: make(map[string]string)}
	for _, key := range claudeSettingsEnvKeys {
		var value string
		if rawValue, ok := payload.Env[key]; ok && json.Unmarshal(rawValue, &value) == nil {
			resolved.Env[key] = strings.TrimSpace(value)
		}
	}
	return resolved
}
