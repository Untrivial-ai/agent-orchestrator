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
	case "qwen", "continue", "goose", "vibe", "cline", "autohand":
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
	}
	return paths
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
