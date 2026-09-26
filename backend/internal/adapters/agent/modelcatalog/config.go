package modelcatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const modelConfigReadLimit = 2 << 20

type configParser func([]byte) ([]ports.AgentModelInfo, error)

func hasConfigDiscoverySource(agentID string) bool {
	switch agentID {
	case "qwen", "continue", "goose", "vibe", "cline", "autohand", "opencode", "kilocode":
		return true
	default:
		return false
	}
}

func discoverConfigCatalog(agentID, workingDir string, env map[string]string) (ports.AgentModelCatalog, error) {
	base := Base(agentID)
	parser := configModelParser(agentID)
	if parser == nil {
		return base, fmt.Errorf("%s has no configuration model parser", agentID)
	}
	var models []ports.AgentModelInfo
	var found bool
	for _, path := range modelConfigPaths(agentID, workingDir, env) {
		raw, err := readModelConfig(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return base, fmt.Errorf("%s model discovery read %s: %w", agentID, path, err)
		}
		found = true
		parsed, err := parser(raw)
		if err != nil {
			return base, fmt.Errorf("%s model discovery parse %s: %w", agentID, path, err)
		}
		models = append(models, parsed...)
	}
	if !found {
		return base, fmt.Errorf("%s model configuration was not found", agentID)
	}
	models = normalize(models)
	if len(models) == 0 {
		return base, fmt.Errorf("%s model configuration returned no models", agentID)
	}
	base.Models = models
	base.SelectionMode = ports.ModelSelectionCatalog
	base.Source = "config"
	return base, nil
}

func configModelParser(agentID string) configParser {
	switch agentID {
	case "qwen":
		return parseQwenModels
	case "continue":
		return parseContinueModels
	case "goose":
		return parseGooseModels
	case "vibe":
		return parseVibeModels
	case "cline":
		return parseClineModels
	case "autohand":
		return parseAutoHandModels
	case "opencode", "kilocode":
		return parseOpenCodeModels
	default:
		return nil
	}
}

func modelConfigPaths(agentID, workingDir string, env map[string]string) []string {
	home, _ := os.UserHomeDir()
	var paths []string
	switch agentID {
	case "qwen":
		if root := qwenConfigHome(home, env); root != "" {
			paths = append(paths, filepath.Join(root, "settings.json"))
		}
		if workingDir != "" {
			paths = append(paths, filepath.Join(workingDir, ".qwen", "settings.json"))
		}
	case "continue":
		if home != "" {
			paths = append(paths, filepath.Join(home, ".continue", "config.yaml"))
		}
	case "goose":
		if root := strings.TrimSpace(env["GOOSE_PATH_ROOT"]); root != "" {
			paths = append(paths, filepath.Join(root, "config", "config.yaml"))
		} else if home != "" {
			paths = append(paths, filepath.Join(home, ".config", "goose", "config.yaml"))
		}
	case "vibe":
		if root := strings.TrimSpace(env["VIBE_HOME"]); root != "" {
			paths = append(paths, filepath.Join(root, "config.toml"))
		} else if home != "" {
			paths = append(paths, filepath.Join(home, ".vibe", "config.toml"))
		}
		if workingDir != "" {
			paths = append(paths, filepath.Join(workingDir, ".vibe", "config.toml"))
		}
	case "cline":
		if home != "" {
			paths = append(paths, filepath.Join(home, ".cline", "data", "settings", "providers.json"))
		}
	case "autohand":
		if home != "" {
			paths = append(paths, filepath.Join(home, ".autohand", "config.json"))
		}
	case "opencode":
		configRoot := xdgConfigRoot(home, env)
		if configRoot != "" {
			paths = appendConfigFiles(paths, filepath.Join(configRoot, "opencode"),
				"config.json", "opencode.json", "opencode.jsonc")
		}
		if p := resolvedConfigPath("OPENCODE_CONFIG", workingDir, env); p != "" {
			paths = append(paths, p)
		}
		for _, dir := range projectConfigDirs(workingDir) {
			paths = appendConfigFiles(paths, dir, "opencode.json", "opencode.jsonc")
			paths = appendConfigFiles(paths, filepath.Join(dir, ".opencode"), "opencode.json", "opencode.jsonc")
		}
		if p := resolvedEnvValue(env, "OPENCODE_CONFIG_DIR"); p != "" {
			paths = appendConfigFiles(paths, p, "opencode.json", "opencode.jsonc")
		}
	case "kilocode":
		// Kilo Code CLI 1.0 is an opencode fork. Its documented config surface
		// (kilocode.ai/docs/cli): KILO_CONFIG env override; project-level
		// kilo.json[c] (legacy opencode.json[c]) or config inside ./.kilo/;
		// global ~/.config/kilo/kilo.json[c] (legacy opencode.json[c]).
		configRoot := xdgConfigRoot(home, env)
		if configRoot != "" {
			paths = appendConfigFiles(paths, filepath.Join(configRoot, "kilo"),
				"config.json", "kilo.json", "kilo.jsonc", "opencode.json", "opencode.jsonc")
		}
		if p := resolvedConfigPath("KILO_CONFIG", workingDir, env); p != "" {
			paths = append(paths, p)
		}
		for _, dir := range projectConfigDirs(workingDir) {
			paths = appendConfigFiles(paths, dir, "kilo.json", "kilo.jsonc", "opencode.json", "opencode.jsonc")
			for _, configDir := range []string{".kilo", ".kilocode"} {
				paths = appendConfigFiles(paths, filepath.Join(dir, configDir),
					"kilo.jsonc", "kilo.json", "opencode.jsonc", "opencode.json")
			}
		}
		if p := resolvedEnvValue(env, "KILO_CONFIG_DIR"); p != "" {
			paths = appendConfigFiles(paths, p, "kilo.jsonc", "kilo.json", "opencode.jsonc", "opencode.json")
		}
	}
	return uniqueConfigPaths(paths)
}

func resolvedEnvValue(env map[string]string, key string) string {
	if value, ok := env[key]; ok {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(os.Getenv(key))
}

func resolvedConfigPath(key, workingDir string, env map[string]string) string {
	path := resolvedEnvValue(env, key)
	if path != "" && !filepath.IsAbs(path) && workingDir != "" {
		return filepath.Join(workingDir, path)
	}
	return path
}

func xdgConfigRoot(home string, env map[string]string) string {
	if root := resolvedEnvValue(env, "XDG_CONFIG_HOME"); root != "" {
		return root
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".config")
}

func projectConfigDirs(workingDir string) []string {
	if strings.TrimSpace(workingDir) == "" {
		return nil
	}
	start := filepath.Clean(workingDir)
	dirs := []string{start}
	for dir := start; ; {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return []string{start}
		}
		dirs = append(dirs, parent)
		dir = parent
	}
	for left, right := 0, len(dirs)-1; left < right; left, right = left+1, right-1 {
		dirs[left], dirs[right] = dirs[right], dirs[left]
	}
	return dirs
}

func appendConfigFiles(paths []string, dir string, names ...string) []string {
	for _, name := range names {
		paths = append(paths, filepath.Join(dir, name))
	}
	return paths
}

func uniqueConfigPaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	unique := make([]string, 0, len(paths))
	for _, path := range paths {
		clean := filepath.Clean(path)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		unique = append(unique, clean)
	}
	return unique
}

func qwenConfigHome(home string, env map[string]string) string {
	root := strings.TrimSpace(env["QWEN_HOME"])
	if root == "" {
		root = strings.TrimSpace(os.Getenv("QWEN_HOME"))
	}
	if root == "" {
		if home == "" {
			return ""
		}
		return filepath.Join(home, ".qwen")
	}
	if root == "~" {
		return home
	}
	if strings.HasPrefix(root, "~/") {
		return filepath.Join(home, strings.TrimPrefix(root, "~/"))
	}
	return root
}

func readModelConfig(path string) ([]byte, error) {
	file, err := os.Open(path) //nolint:gosec // paths are fixed agent configuration locations
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, modelConfigReadLimit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > modelConfigReadLimit {
		return nil, fmt.Errorf("configuration exceeds %d bytes", modelConfigReadLimit)
	}
	return raw, nil
}

func configDiscoveryFingerprint(agentID, workingDir string, env map[string]string) string {
	if !hasConfigDiscoverySource(agentID) {
		return ""
	}
	hash := sha256.New()
	for _, path := range modelConfigPaths(agentID, workingDir, env) {
		raw, err := readModelConfig(path)
		if err != nil {
			continue
		}
		_, _ = hash.Write([]byte(path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(raw)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)[:8])
}

func parseQwenModels(raw []byte) ([]ports.AgentModelInfo, error) {
	var config struct {
		ModelProviders map[string][]struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"modelProviders"`
		Model struct {
			Name string `json:"name"`
		} `json:"model"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, err
	}
	var models []ports.AgentModelInfo
	for provider, configured := range config.ModelProviders {
		for _, item := range configured {
			id := strings.TrimSpace(item.ID)
			if id == "" {
				continue
			}
			label := strings.TrimSpace(item.Name)
			if label == "" {
				label = id
			}
			models = append(models, ports.AgentModelInfo{
				ID: id, Label: label, Provider: provider,
				IsDefault: strings.EqualFold(id, strings.TrimSpace(config.Model.Name)),
			})
		}
	}
	return normalize(models), nil
}

func parseAutoHandModels(raw []byte) ([]ports.AgentModelInfo, error) {
	var config struct {
		Provider string `json:"provider"`
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	if providerRaw := values["provider"]; providerRaw != nil {
		if err := json.Unmarshal(providerRaw, &config.Provider); err != nil {
			return nil, err
		}
	}
	provider := strings.TrimSpace(config.Provider)
	if provider == "" {
		return nil, nil
	}
	var providerConfig struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(values[provider], &providerConfig); err != nil {
		return nil, err
	}
	modelID := strings.TrimSpace(providerConfig.Model)
	if modelID == "" {
		return nil, nil
	}
	selector := modelID
	if !strings.Contains(modelID, "/") {
		selector = provider + "/" + modelID
	}
	return []ports.AgentModelInfo{{
		ID: selector, Label: modelID, Provider: provider, IsDefault: true,
	}}, nil
}

func parseContinueModels(raw []byte) ([]ports.AgentModelInfo, error) {
	var config struct {
		Models []struct {
			Name     string `yaml:"name"`
			Provider string `yaml:"provider"`
			Model    string `yaml:"model"`
		} `yaml:"models"`
		Defaults map[string]string `yaml:"defaults"`
	}
	if err := yaml.Unmarshal(raw, &config); err != nil {
		return nil, err
	}
	defaults := make(map[string]bool, len(config.Defaults))
	for _, value := range config.Defaults {
		defaults[strings.ToLower(strings.TrimSpace(value))] = true
	}
	models := make([]ports.AgentModelInfo, 0, len(config.Models))
	for _, item := range config.Models {
		id := strings.TrimSpace(item.Model)
		if id == "" {
			continue
		}
		label := strings.TrimSpace(item.Name)
		if label == "" {
			label = id
		}
		models = append(models, ports.AgentModelInfo{
			ID: id, Label: label, Provider: strings.TrimSpace(item.Provider),
			IsDefault: defaults[strings.ToLower(id)] || defaults[strings.ToLower(label)],
		})
	}
	return normalize(models), nil
}

func parseGooseModels(raw []byte) ([]ports.AgentModelInfo, error) {
	var config struct {
		ActiveProvider string `yaml:"active_provider"`
		Providers      map[string]struct {
			Model  string   `yaml:"model"`
			Models []string `yaml:"models"`
		} `yaml:"providers"`
	}
	if err := yaml.Unmarshal(raw, &config); err != nil {
		return nil, err
	}
	// AO can pass Goose a model override but not a provider override. Offering
	// another provider's models would produce a launch Goose cannot reproduce.
	provider := strings.TrimSpace(config.ActiveProvider)
	item, ok := config.Providers[provider]
	if !ok || provider == "" {
		return nil, nil
	}
	ids := append([]string(nil), item.Models...)
	if strings.TrimSpace(item.Model) != "" {
		ids = append(ids, item.Model)
	}
	models := make([]ports.AgentModelInfo, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		models = append(models, ports.AgentModelInfo{
			ID: id, Label: id, Provider: provider,
			IsDefault: id == strings.TrimSpace(item.Model),
		})
	}
	return normalize(models), nil
}

func parseVibeModels(raw []byte) ([]ports.AgentModelInfo, error) {
	var config struct {
		ActiveModel string `toml:"active_model"`
		Models      []struct {
			Name     string `toml:"name"`
			Provider string `toml:"provider"`
			Alias    string `toml:"alias"`
		} `toml:"models"`
	}
	if err := toml.Unmarshal(raw, &config); err != nil {
		return nil, err
	}
	models := make([]ports.AgentModelInfo, 0, len(config.Models))
	for _, item := range config.Models {
		id := strings.TrimSpace(item.Alias)
		if id == "" {
			id = strings.TrimSpace(item.Name)
		}
		if id == "" {
			continue
		}
		label := strings.TrimSpace(item.Name)
		if label == "" {
			label = id
		}
		models = append(models, ports.AgentModelInfo{
			ID: id, Label: label, Provider: strings.TrimSpace(item.Provider),
			IsDefault: id == strings.TrimSpace(config.ActiveModel),
		})
	}
	return normalize(models), nil
}

func parseClineModels(raw []byte) ([]ports.AgentModelInfo, error) {
	var config struct {
		LastUsedProvider string                     `json:"lastUsedProvider"`
		Providers        map[string]json.RawMessage `json:"providers"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, err
	}
	var models []ports.AgentModelInfo
	for provider, rawProvider := range config.Providers {
		var value any
		if err := json.Unmarshal(rawProvider, &value); err != nil {
			return nil, err
		}
		ids := configuredModelIDs(value)
		for _, id := range ids {
			models = append(models, ports.AgentModelInfo{
				ID: id, Label: id, Provider: provider,
				IsDefault: provider == config.LastUsedProvider,
			})
		}
	}
	return normalize(models), nil
}

func configuredModelIDs(value any) []string {
	var ids []string
	var walk func(any)
	walk = func(current any) {
		switch node := current.(type) {
		case []any:
			for _, child := range node {
				walk(child)
			}
		case map[string]any:
			for key, child := range node {
				lower := strings.ToLower(key)
				if text, ok := child.(string); ok && (lower == "model" || lower == "modelid" || lower == "apimodelid") {
					if id := strings.TrimSpace(text); looksLikeModelID(id) {
						ids = append(ids, id)
					}
					continue
				}
				walk(child)
			}
		}
	}
	walk(value)
	sort.Strings(ids)
	return ids
}

// parseOpenCodeModels reads the opencode/kilocode configuration format
// (opencode.json / kilo.json, JSONC allowed): providers are configured under
// "provider", and each provider may pin a "models" map whose keys are the
// selectable model IDs. A provider with no "models" map resolves registry
// defaults itself, so it contributes nothing here. The top-level "model"
// ("provider/model") marks the default; when it names a model not declared
// under any provider it is appended so the effective selection stays visible.
func parseOpenCodeModels(raw []byte) ([]ports.AgentModelInfo, error) {
	var config struct {
		Model    string                     `json:"model"`
		Provider map[string]json.RawMessage `json:"provider"`
	}
	if err := json.Unmarshal(stripJSONComments(raw), &config); err != nil {
		return nil, err
	}
	var models []ports.AgentModelInfo
	for provider, rawProvider := range config.Provider {
		var entry struct {
			Models map[string]json.RawMessage `json:"models"`
		}
		if err := json.Unmarshal(rawProvider, &entry); err != nil {
			// A non-object provider entry carries no selectable models.
			continue
		}
		for id := range entry.Models {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			models = append(models, ports.AgentModelInfo{
				ID: provider + "/" + id, Label: id, Provider: provider,
			})
		}
	}
	models = normalize(models)
	defaultModel := strings.TrimSpace(config.Model)
	if defaultModel == "" {
		return models, nil
	}
	for i := range models {
		if strings.EqualFold(models[i].ID, defaultModel) {
			models[i].IsDefault = true
			return normalize(models), nil
		}
	}
	provider, _, _ := strings.Cut(defaultModel, "/")
	models = append(models, ports.AgentModelInfo{
		ID: defaultModel, Label: defaultModel, Provider: provider, IsDefault: true,
	})
	return normalize(models), nil
}

// stripJSONComments removes // and /* */ comments from JSONC content while
// preserving comment-like sequences inside string literals (URLs, base URLs).
func stripJSONComments(raw []byte) []byte {
	out := make([]byte, 0, len(raw))
	inString := false
	escaped := false
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
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == '/' && i+1 < len(raw) {
			switch raw[i+1] {
			case '/':
				for i < len(raw) && raw[i] != '\n' {
					i++
				}
				if i < len(raw) {
					out = append(out, raw[i])
				}
				continue
			case '*':
				i += 2
				for i+1 < len(raw) && (raw[i] != '*' || raw[i+1] != '/') {
					i++
				}
				i++ // skip the closing '/'
				continue
			}
		}
		out = append(out, c)
	}
	return out
}
