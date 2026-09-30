package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

type ProjectAgentConfig struct {
	Model       string `json:"model,omitempty"`
	Mode        string `json:"mode,omitempty"`
	Effort      string `json:"effort,omitempty"`
	Permissions string `json:"permissions,omitempty"`
}

type ProjectRoleConfig struct {
	Agent       string             `json:"agent"`
	AgentConfig ProjectAgentConfig `json:"agentConfig"`
}

type ProjectReviewer struct {
	Harness     string             `json:"harness"`
	AgentConfig ProjectAgentConfig `json:"agentConfig"`
}

type ProjectSettingsConfig struct {
	Worker       *ProjectRoleConfig `json:"worker,omitempty"`
	Orchestrator *ProjectRoleConfig `json:"orchestrator,omitempty"`
	Reviewers    []ProjectReviewer  `json:"reviewers,omitempty"`
	AutoReview   *bool              `json:"autoReview,omitempty"`
}

// Config contains a partial object. Arrays replace; nested role objects merge.
type ProjectSettingsPatch struct {
	DisplayName   *string         `json:"displayName,omitempty"`
	DefaultBranch *string         `json:"defaultBranch,omitempty"`
	Config        json.RawMessage `json:"config,omitempty"`
}

// NormalizeProjectConfig reads legacy flat choices, preferring a nested agent
// when present. Callers persist the normalized object on their next write.
func NormalizeProjectConfig(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(raw, &config); err != nil || config == nil {
		return nil, fmt.Errorf("project config must be an object")
	}
	for _, role := range []string{"worker", "orchestrator"} {
		legacy := config[role+"Agent"]
		if len(legacy) > 0 {
			var agent string
			if err := json.Unmarshal(legacy, &agent); err != nil {
				return nil, fmt.Errorf("%sAgent must be a string", role)
			}
			var nested map[string]json.RawMessage
			if existing := config[role]; len(existing) > 0 {
				if err := json.Unmarshal(existing, &nested); err != nil || nested == nil {
					return nil, fmt.Errorf("%s must be an object", role)
				}
			}
			if nested == nil {
				nested = make(map[string]json.RawMessage)
			}
			if _, exists := nested["agent"]; !exists {
				nested["agent"] = legacy
			}
			config[role], _ = json.Marshal(nested)
			delete(config, role+"Agent")
		}
	}
	return json.Marshal(config)
}

func DecodeProjectSettings(raw json.RawMessage) (ProjectSettingsConfig, error) {
	normalized, err := NormalizeProjectConfig(raw)
	if err != nil {
		return ProjectSettingsConfig{}, err
	}
	var settings ProjectSettingsConfig
	if err := json.Unmarshal(normalized, &settings); err != nil {
		return settings, fmt.Errorf("invalid project settings: %w", err)
	}
	for _, role := range []*ProjectRoleConfig{settings.Worker, settings.Orchestrator} {
		if role != nil {
			if err := ValidateProjectAgent(role.Agent, role.AgentConfig); err != nil {
				return settings, err
			}
		}
	}
	if len(settings.Reviewers) > 1 {
		return settings, fmt.Errorf("Cloud supports one project reviewer")
	}
	for _, reviewer := range settings.Reviewers {
		if err := ValidateProjectAgent(reviewer.Harness, reviewer.AgentConfig); err != nil {
			return settings, err
		}
	}
	return settings, nil
}

// SessionAgentConfig applies role defaults only to that role's chosen harness.
// An explicit session model wins over the project's default.
func SessionAgentConfig(raw json.RawMessage, kind, harness, model string) (string, ProjectAgentConfig, error) {
	settings, err := DecodeProjectSettings(raw)
	if err != nil {
		return "", ProjectAgentConfig{}, err
	}
	role := settings.Worker
	if kind == "orchestrator" {
		role = settings.Orchestrator
	}
	config := ProjectAgentConfig{}
	if role != nil {
		if harness == "" {
			harness = role.Agent
		}
		if harness == role.Agent {
			config = role.AgentConfig
		}
	}
	if model != "" {
		config.Model = model
	}
	return harness, config, nil
}

// EffectiveReviewer preserves the session's agent when no reviewer is set.
// Permission defaults follow the session policy, independently of auto-inject.
func EffectiveReviewer(settings ProjectSettingsConfig, harness, model, sessionMode string, workerConfig ProjectAgentConfig) ProjectReviewer {
	reviewer := ProjectReviewer{Harness: harness, AgentConfig: workerConfig}
	reviewer.AgentConfig.Model = model
	if len(settings.Reviewers) > 0 {
		reviewer = settings.Reviewers[0]
	}
	if reviewer.AgentConfig.Permissions == "" {
		reviewer.AgentConfig.Permissions = "auto"
		if sessionMode == "trusted" {
			reviewer.AgentConfig.Permissions = "bypass-permissions"
		}
	}
	return reviewer
}

func ValidateProjectAgent(harness string, config ProjectAgentConfig) error {
	if !slices.Contains([]string{"claude-code", "codex", "cursor", "opencode"}, harness) {
		return fmt.Errorf("unsupported Cloud agent %q", harness)
	}
	if utf8.RuneCountInString(config.Model) > 500 || strings.ContainsFunc(config.Model, unicode.IsControl) {
		return fmt.Errorf("model must be at most 500 characters without control characters")
	}
	if !slices.Contains([]string{"", "default", "auto", "accept-edits", "bypass-permissions"}, config.Permissions) {
		return fmt.Errorf("invalid agent permissions")
	}
	if harness == "opencode" && config.Permissions == "accept-edits" {
		return fmt.Errorf("OpenCode does not support accept-edits permissions")
	}
	if config.Effort != "" {
		levels := []string{"low", "medium", "high", "xhigh", "max"}
		if harness == "claude-code" {
			levels = []string{"low", "medium", "high", "max"}
		}
		if (harness != "codex" && harness != "claude-code") || !slices.Contains(levels, config.Effort) {
			return fmt.Errorf("unsupported effort for %s", harness)
		}
	}
	if config.Mode != "" && !(harness == "cursor" && slices.Contains([]string{"plan", "ask"}, config.Mode)) {
		return fmt.Errorf("unsupported mode for %s", harness)
	}
	return nil
}

func ParseProjectSettingsPatch(raw json.RawMessage) (ProjectSettingsPatch, error) {
	var patch ProjectSettingsPatch
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&patch); err != nil {
		return patch, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return patch, fmt.Errorf("settings must be an object")
	}
	for key, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return patch, fmt.Errorf("%s cannot be null", key)
		}
	}
	if patch.DisplayName != nil {
		*patch.DisplayName = strings.TrimSpace(*patch.DisplayName)
		if n := utf8.RuneCountInString(*patch.DisplayName); n < 1 || n > 120 {
			return patch, fmt.Errorf("project name must contain 1-120 characters")
		}
	}
	if patch.DefaultBranch != nil {
		*patch.DefaultBranch = strings.TrimSpace(*patch.DefaultBranch)
		if n := utf8.RuneCountInString(*patch.DefaultBranch); n < 1 || n > 255 || strings.ContainsFunc(*patch.DefaultBranch, unicode.IsControl) {
			return patch, fmt.Errorf("default branch must contain 1-255 characters without control characters")
		}
	}
	if len(patch.Config) > 0 {
		if err := validateSettingsPatchObject(patch.Config, "config"); err != nil {
			return patch, err
		}
	}
	return patch, nil
}

func validateSettingsPatchObject(raw json.RawMessage, kind string) error {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return fmt.Errorf("%s must be an object", kind)
	}
	allowed := map[string][]string{
		"config":      {"worker", "orchestrator", "reviewers", "autoReview"},
		"role":        {"agent", "agentConfig"},
		"reviewer":    {"harness", "agentConfig"},
		"agentConfig": {"model", "mode", "effort", "permissions"},
	}
	for key, value := range values {
		if !slices.Contains(allowed[kind], key) {
			return fmt.Errorf("unsupported or null %s.%s", kind, key)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			if kind == "config" && (key == "worker" || key == "orchestrator") {
				continue // Remove the role override and use the session selection.
			}
			return fmt.Errorf("unsupported or null %s.%s", kind, key)
		}
		switch key {
		case "worker", "orchestrator":
			if err := validateSettingsPatchObject(value, "role"); err != nil {
				return err
			}
		case "agentConfig":
			if err := validateSettingsPatchObject(value, "agentConfig"); err != nil {
				return err
			}
		case "reviewers":
			var reviewers []json.RawMessage
			if json.Unmarshal(value, &reviewers) != nil || len(reviewers) > 1 {
				return fmt.Errorf("reviewers must be an array containing at most one reviewer")
			}
			for _, reviewer := range reviewers {
				if err := validateSettingsPatchObject(reviewer, "reviewer"); err != nil {
					return err
				}
			}
		case "autoReview":
			var enabled bool
			if json.Unmarshal(value, &enabled) != nil {
				return fmt.Errorf("autoReview must be a boolean")
			}
		default:
			var setting string
			if json.Unmarshal(value, &setting) != nil {
				return fmt.Errorf("%s must be a string", key)
			}
		}
	}
	return nil
}

func MergeProjectSettingsConfig(existing, patch json.RawMessage) (json.RawMessage, error) {
	normalized, err := NormalizeProjectConfig(existing)
	if err != nil {
		return nil, err
	}
	if len(patch) > 0 {
		if err := validateSettingsPatchObject(patch, "config"); err != nil {
			return nil, err
		}
		normalized = mergeSettingsObjects(normalized, patch)
	}
	if _, err := DecodeProjectSettings(normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func mergeSettingsObjects(existing, patch json.RawMessage) json.RawMessage {
	var result, updates map[string]json.RawMessage
	_ = json.Unmarshal(existing, &result)
	_ = json.Unmarshal(patch, &updates)
	if result == nil {
		result = make(map[string]json.RawMessage)
	}
	for key, value := range updates {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			delete(result, key)
		} else if bytes.HasPrefix(bytes.TrimSpace(value), []byte("{")) {
			result[key] = mergeSettingsObjects(result[key], value)
		} else {
			result[key] = value
		}
	}
	raw, _ := json.Marshal(result)
	return raw
}
