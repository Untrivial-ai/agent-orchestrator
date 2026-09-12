package qwen

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
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if status, ok, err := qwenLocalAuthStatus(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	} else if ok {
		return status, nil
	}
	// Qwen documents /doctor as an in-session command. Do not invoke the CLI
	// with a "doctor" positional argument: it would enter the interactive
	// surface rather than provide a documented, non-interactive auth probe.
	return ports.AgentAuthStatusUnknown, nil
}

var qwenAPIKeyEnvVars = []string{
	"QWEN_API_KEY",
	"BAILIAN_CODING_PLAN_API_KEY",
	"OPENAI_API_KEY",
	"ANTHROPIC_API_KEY",
	"GEMINI_API_KEY",
	"GOOGLE_API_KEY",
	"OPENROUTER_API_KEY",
	"REQUESTY_API_KEY",
	"DASHSCOPE_API_KEY",
	"ZAI_API_KEY",
}

func qwenLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	for _, name := range qwenAPIKeyEnvVars {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return ports.AgentAuthStatusAuthorized, true, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if home == "" {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	qwenHome := qwenHomeDir(home)
	settingsPath := filepath.Join(qwenHome, "settings.json")
	if status, ok, err := qwenAuthStatusFromSettings(settingsPath); err != nil || ok {
		return status, ok, err
	}
	names, err := qwenConfiguredEnvNamesFromSettings(settingsPath)
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	return qwenGlobalEnvAuthStatus(qwenHome, home, names...)
}

// qwenHomeDir mirrors Qwen Code's QWEN_HOME override for global state. Project
// settings and .env files deliberately are not considered here: the catalog's
// auth probe has no session workspace, so the daemon's current directory is not
// a valid substitute for a project directory.
func qwenHomeDir(home string) string {
	if path := strings.TrimSpace(os.Getenv("QWEN_HOME")); path != "" {
		if path == "~" {
			return home
		}
		if strings.HasPrefix(path, "~/") {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
		return path
	}
	return filepath.Join(home, ".qwen")
}

func qwenAuthStatusFromSettings(path string) (ports.AgentAuthStatus, bool, error) {
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

	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if containsQwenAPIKey(root) || qwenConfiguredEnvPresent(root) || qwenSettingsEnvPresent(root, qwenConfiguredEnvNames(root)...) {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

// qwenSettingsEnvPresent recognizes the documented settings.json "env" map,
// whose values are loaded as Qwen's lowest-priority credential source.
func qwenSettingsEnvPresent(value any, extraNames ...string) bool {
	root, ok := value.(map[string]any)
	if !ok {
		return false
	}
	env, ok := root["env"].(map[string]any)
	if !ok {
		return false
	}
	for name, value := range env {
		if qwenKnownAPIKeyEnvVar(name, extraNames...) && stringSetting(value) != "" {
			return true
		}
	}
	return false
}

func qwenConfiguredEnvPresent(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if strings.EqualFold(key, "envKey") && strings.TrimSpace(stringSetting(child)) != "" &&
				strings.TrimSpace(os.Getenv(stringSetting(child))) != "" {
				return true
			}
			if qwenConfiguredEnvPresent(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if qwenConfiguredEnvPresent(child) {
				return true
			}
		}
	}
	return false
}

func qwenConfiguredEnvNamesFromSettings(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	return qwenConfiguredEnvNames(root), nil
}

func qwenConfiguredEnvNames(value any) []string {
	var names []string
	var visit func(any)
	visit = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			for key, child := range v {
				if strings.EqualFold(key, "envKey") {
					if name := stringSetting(child); name != "" && !containsQwenEnvName(names, name) {
						names = append(names, name)
					}
				}
				visit(child)
			}
		case []any:
			for _, child := range v {
				visit(child)
			}
		}
	}
	visit(value)
	return names
}

func qwenGlobalEnvAuthStatus(qwenHome, home string, extraNames ...string) (ports.AgentAuthStatus, bool, error) {
	// Qwen falls back to these user-scoped .env files only after its
	// workspace search. The workspace search cannot be performed safely by a
	// global catalog probe, which has no workspace input.
	for _, path := range []string{filepath.Join(qwenHome, ".env"), filepath.Join(home, ".env")} {
		status, ok, err := qwenEnvFileAuthStatus(path, extraNames...)
		if err != nil || ok {
			return status, ok, err
		}
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

func qwenEnvFileAuthStatus(path string, extraNames ...string) (ports.AgentAuthStatus, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || !qwenKnownAPIKeyEnvVar(strings.TrimSpace(key), extraNames...) {
			continue
		}
		if strings.Trim(strings.TrimSpace(value), `"'`) != "" {
			return ports.AgentAuthStatusAuthorized, true, nil
		}
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

func qwenKnownAPIKeyEnvVar(name string, extraNames ...string) bool {
	for _, candidate := range qwenAPIKeyEnvVars {
		if name == candidate {
			return true
		}
	}
	for _, candidate := range extraNames {
		if name == candidate {
			return true
		}
	}
	return false
}

func containsQwenEnvName(names []string, target string) bool {
	for _, name := range names {
		if name == target {
			return true
		}
	}
	return false
}

func containsQwenAPIKey(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if strings.EqualFold(key, "apiKey") || strings.EqualFold(key, "apikey") {
				if stringSetting(child) != "" {
					return true
				}
				continue
			}
			if containsQwenAPIKey(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if containsQwenAPIKey(child) {
				return true
			}
		}
	}
	return false
}

func stringSetting(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	text = strings.TrimSpace(text)
	if text == "" || strings.EqualFold(text, "null") || strings.EqualFold(text, "none") {
		return ""
	}
	return text
}
