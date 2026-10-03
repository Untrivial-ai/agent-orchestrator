package modelcatalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// configuredDefaultSource describes where an agent CLI keeps the model it runs
// when no --model flag is passed. Only user-level sources are read: sessions
// launch from AO worktrees whose repository content is chosen per session, so
// repository config is never trusted (see repoMayHoldConfig).
type configuredDefaultSource struct {
	// paths lists user-level config files, lowest precedence first: a later
	// file that sets a model overrides an earlier one.
	paths func(home string, env map[string]string) []string
	parse func(raw []byte) string
	// repoFiles names repository config files (relative to a directory, slash
	// separated) that outrank every user-level path. If the repository may
	// hold any of them, the default is left unresolved.
	repoFiles []string
	// envOverride names a variable that wins over every file when set.
	envOverride string
	// resolve replaces paths/parse/repoFiles for agents whose layering cannot
	// be expressed as a flat file list. It returns "" when the effective model
	// cannot be determined.
	resolve func(home, workingDir string, env map[string]string) string
}

// configuredDefaultSources covers agents whose model-list command reports
// models but never marks which one the CLI will run. Without this, the picker
// has no default to show and the task form reads "Model not reported" even
// though the user has already chosen a model in the agent's own settings.
var configuredDefaultSources = map[string]configuredDefaultSource{
	"opencode": {resolve: func(home, workingDir string, env map[string]string) string {
		return resolveOpenCodeModel(1, home, workingDir, env)
	}},
	"opencode-v2": {resolve: func(home, workingDir string, env map[string]string) string {
		return resolveOpenCodeModel(2, home, workingDir, env)
	}},
	"aider": {
		paths: func(home string, _ map[string]string) []string {
			if home == "" {
				return nil
			}
			return []string{filepath.Join(home, ".aider.conf.yml"), filepath.Join(home, ".aider.conf.yaml")}
		},
		parse:       parseYAMLModelKey,
		repoFiles:   []string{".aider.conf.yml", ".aider.conf.yaml"},
		envOverride: "AIDER_MODEL",
	},
	"droid": {
		paths: func(home string, _ map[string]string) []string {
			if home == "" {
				return nil
			}
			return []string{filepath.Join(home, ".factory", "settings.json")}
		},
		parse: parseJSONCModelKey,
	},
	// Copilot CLI keeps user-editable settings, including the model /model
	// selects, in settings.json; config.json is legacy managed state and is not
	// read, so a stale value there can never be marked as the default. Model
	// precedence: user settings < repository settings < repository local
	// settings < COPILOT_MODEL.
	// https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-config-dir-reference
	"copilot": {
		paths: func(home string, env map[string]string) []string {
			root := envValue(env, "COPILOT_HOME")
			if root == "" && home != "" {
				root = filepath.Join(home, ".copilot")
			}
			if root == "" {
				return nil
			}
			return []string{filepath.Join(root, "settings.json")}
		},
		parse:       parseJSONCModelKey,
		repoFiles:   []string{".github/copilot/settings.json", ".github/copilot/settings.local.json"},
		envOverride: "COPILOT_MODEL",
	},
	"pi": {
		paths: func(home string, _ map[string]string) []string {
			if home == "" {
				return nil
			}
			return []string{filepath.Join(home, ".pi", "agent", "settings.json")}
		},
		parse: parsePiDefaultModel,
	},
	"crush": {
		paths: func(home string, env map[string]string) []string {
			var paths []string
			if configHome := xdgDir(home, env, "XDG_CONFIG_HOME", ".config"); configHome != "" {
				paths = append(paths, filepath.Join(configHome, "crush", "crush.json"))
			}
			// Crush's own model picker persists the selection to its data dir.
			if dataHome := xdgDir(home, env, "XDG_DATA_HOME", filepath.Join(".local", "share")); dataHome != "" {
				paths = append(paths, filepath.Join(dataHome, "crush", "crush.json"))
			}
			return paths
		},
		parse:     parseCrushDefaultModel,
		repoFiles: []string{"crush.json", ".crush.json"},
	},
}

// readConfiguredModel returns the model set in path, or fallback when the file
// is missing or sets none.
func readConfiguredModel(path string, parse func([]byte) string, fallback string) string {
	raw, err := readModelConfig(path)
	if err != nil {
		return fallback
	}
	if value := strings.TrimSpace(parse(raw)); value != "" {
		return value
	}
	return fallback
}

// configuredDefaultModel returns the model the agent's local configuration
// selects, or "" when the agent has no such source or nothing is configured.
func configuredDefaultModel(agentID, workingDir string, env map[string]string) string {
	source, ok := configuredDefaultSources[agentID]
	if !ok {
		return ""
	}
	if source.envOverride != "" {
		if value := envValue(env, source.envOverride); value != "" {
			return value
		}
	}
	home, _ := os.UserHomeDir()
	if source.resolve != nil {
		return strings.TrimSpace(source.resolve(home, workingDir, env))
	}
	if repoMayHoldConfig(workingDir, source.repoFiles...) {
		return ""
	}
	configured := ""
	for _, path := range source.paths(home, env) {
		configured = readConfiguredModel(path, source.parse, configured)
	}
	return configured
}

// applyConfiguredDefault marks the configured model as the catalog default. A
// catalog that already reports a default is left alone: the CLI's own answer
// is more authoritative than AO's reading of its config files. A configured
// model missing from the list is appended, because it is what the CLI will
// actually run.
func applyConfiguredDefault(models []ports.AgentModelInfo, configured string) []ports.AgentModelInfo {
	configured = strings.TrimSpace(configured)
	if configured == "" || !isConcreteModel(configured) {
		return models
	}
	for _, item := range models {
		if item.IsDefault && isConcreteModel(item.ID) {
			return models
		}
	}
	for i := range models {
		if strings.EqualFold(models[i].ID, configured) {
			models[i].IsDefault = true
			return models
		}
	}
	return append(models, ports.AgentModelInfo{ID: configured, Label: configured, IsDefault: true})
}

// isConcreteModel mirrors the renderer's rule: "default" is a placeholder, not
// a model the picker can resolve to a name.
func isConcreteModel(id string) bool {
	id = strings.TrimSpace(id)
	return id != "" && !strings.EqualFold(id, "default")
}

func parseJSONCModelKey(raw []byte) string {
	var config struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(stripJSONC(raw), &config) != nil {
		return ""
	}
	return config.Model
}

func parseYAMLModelKey(raw []byte) string {
	var config struct {
		Model string `yaml:"model"`
	}
	if yaml.Unmarshal(raw, &config) != nil {
		return ""
	}
	return config.Model
}

func parsePiDefaultModel(raw []byte) string {
	var settings struct {
		DefaultProvider string `json:"defaultProvider"`
		DefaultModel    string `json:"defaultModel"`
	}
	if json.Unmarshal(stripJSONC(raw), &settings) != nil {
		return ""
	}
	return joinProviderModel(settings.DefaultProvider, settings.DefaultModel)
}

func parseCrushDefaultModel(raw []byte) string {
	var config struct {
		Models struct {
			Large struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
			} `json:"large"`
		} `json:"models"`
	}
	if json.Unmarshal(stripJSONC(raw), &config) != nil {
		return ""
	}
	return joinProviderModel(config.Models.Large.Provider, config.Models.Large.Model)
}

// joinProviderModel builds the provider/model id these CLIs list. A model with
// no provider is returned bare; a provider with no model selects nothing.
func joinProviderModel(provider, model string) string {
	provider, model = strings.TrimSpace(provider), strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if provider == "" || strings.HasPrefix(model, provider+"/") {
		return model
	}
	return provider + "/" + model
}

// stripJSONC removes // and /* */ comments and trailing commas so JSONC config
// files (opencode.jsonc) decode with encoding/json. String contents are kept
// verbatim, including sequences that look like comments inside URLs.
func stripJSONC(raw []byte) []byte {
	out := make([]byte, 0, len(raw))
	inString, escaped := false, false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
			out = append(out, c)
		case c == '/' && i+1 < len(raw) && raw[i+1] == '/':
			for i < len(raw) && raw[i] != '\n' {
				i++
			}
			if i < len(raw) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(raw) && raw[i+1] == '*':
			i += 2
			for i+1 < len(raw) && (raw[i] != '*' || raw[i+1] != '/') {
				i++
			}
			i++
		case c == ']' || c == '}':
			// Drop a trailing comma left before this closer.
			j := len(out) - 1
			for j >= 0 && (out[j] == ' ' || out[j] == '\t' || out[j] == '\n' || out[j] == '\r') {
				j--
			}
			if j >= 0 && out[j] == ',' {
				out = append(out[:j], out[j+1:]...)
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}

func xdgDir(home string, env map[string]string, key, fallback string) string {
	if dir := envValue(env, key); dir != "" {
		return dir
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, fallback)
}

// envValue prefers the discovery request's environment and falls back to the
// daemon's own, the same precedence the spawned CLI would see.
func envValue(env map[string]string, key string) string {
	if value, ok := env[key]; ok {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(os.Getenv(key))
}
