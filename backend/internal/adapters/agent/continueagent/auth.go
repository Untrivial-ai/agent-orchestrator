package continueagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentScopedAuthChecker = (*Plugin)(nil)

// AuthStatus returns the plugin's local authentication status.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.AuthStatusFor(ctx, ports.AgentAuthCheck{})
}

// AuthStatusFor resolves invocation-specific environment and config inputs.
func (p *Plugin) AuthStatusFor(ctx context.Context, check ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return continueAuthStatus(ctx, check, authutil.Dependencies{})
}

func continueAuthStatus(ctx context.Context, check ports.AgentAuthCheck, d authutil.Dependencies) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	getenv := d.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	lookup := func(name string) string {
		if value, ok := check.Env[name]; ok {
			return strings.TrimSpace(value)
		}
		return strings.TrimSpace(getenv(name))
	}
	if lookup("CONTINUE_API_KEY") != "" {
		return ports.AgentAuthStatusConfigured, nil
	}

	home := lookup("HOME")
	goos := d.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos == "windows" {
		home = lookup("USERPROFILE")
	}
	continueHome := lookup("CONTINUE_GLOBAL_DIR")
	if continueHome == "" && filepath.IsAbs(home) {
		continueHome = filepath.Join(home, ".continue")
	}
	if !filepath.IsAbs(continueHome) {
		return ports.AgentAuthStatusUnknown, nil
	}

	var browserLogin struct {
		UserID       string      `json:"userId"`
		UserEmail    string      `json:"userEmail"`
		AccessToken  string      `json:"accessToken"`
		RefreshToken string      `json:"refreshToken"`
		ExpiresAt    json.Number `json:"expiresAt"`
	}
	if authutil.ReadJSON(ctx, d, filepath.Join(continueHome, "auth.json"), &browserLogin) == nil &&
		strings.TrimSpace(browserLogin.UserID) != "" &&
		strings.TrimSpace(browserLogin.UserEmail) != "" &&
		strings.TrimSpace(browserLogin.AccessToken) != "" &&
		strings.TrimSpace(browserLogin.RefreshToken) != "" {
		if expiry, err := browserLogin.ExpiresAt.Int64(); err == nil && expiry > 0 {
			return ports.AgentAuthStatusConfigured, nil
		}
	}

	configPath, explicitConfig := continueSelectedConfigPath(check, continueHome)
	if configPath == "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	lstat := d.Lstat
	if lstat == nil {
		lstat = os.Lstat
	}
	if info, err := lstat(configPath); err != nil || !info.Mode().IsRegular() {
		if err := ctx.Err(); err != nil {
			return ports.AgentAuthStatusUnknown, err
		}
		if !explicitConfig && !continueHasModelFlag(check.Args) && lookup("ANTHROPIC_API_KEY") != "" {
			return ports.AgentAuthStatusConfigured, nil
		}
		return ports.AgentAuthStatusUnknown, nil
	}
	var config struct {
		Models []struct {
			Name     string   `yaml:"name"`
			Provider string   `yaml:"provider"`
			Model    string   `yaml:"model"`
			APIKey   string   `yaml:"apiKey"`
			Roles    []string `yaml:"roles"`
		} `yaml:"models"`
	}
	if authutil.ReadYAML(ctx, d, configPath, &config) != nil {
		return ports.AgentAuthStatusUnknown, ctx.Err()
	}
	var persisted struct {
		Model string `json:"cliSelectedModel"`
	}
	_ = authutil.ReadJSON(ctx, d, filepath.Join(continueHome, "index", "globalContext.json"), &persisted)
	selected := -1
	for i, model := range config.Models {
		name := model.Name
		if name == "" {
			name = model.Model
		}
		if continueChatModel(model.Roles) && name == persisted.Model {
			selected = i
			break
		}
	}
	for i, model := range config.Models {
		if !continueChatModel(model.Roles) || (selected >= 0 && selected != i) {
			continue
		}
		provider, selectedModel := strings.TrimSpace(model.Provider), strings.TrimSpace(model.Model)
		if provider == "" || selectedModel == "" || strings.Contains(provider, "${") || strings.Contains(selectedModel, "${") {
			return ports.AgentAuthStatusUnknown, nil
		}
		key := strings.TrimSpace(model.APIKey)
		if envName, reference := continueSecretEnv(key); reference {
			if envName != "" && continueSecretValue(ctx, d, check, continueHome, envName, lookup) != "" {
				return ports.AgentAuthStatusConfigured, nil
			}
			if err := ctx.Err(); err != nil {
				return ports.AgentAuthStatusUnknown, err
			}
			return ports.AgentAuthStatusUnknown, nil
		}
		if key != "" &&
			!strings.Contains(key, "${") &&
			!strings.EqualFold(key, "null") &&
			!strings.EqualFold(key, "none") &&
			(!strings.HasPrefix(key, "<") || !strings.HasSuffix(key, ">")) {
			return ports.AgentAuthStatusConfigured, nil
		}
		return ports.AgentAuthStatusUnknown, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

func continueSelectedConfigPath(check ports.AgentAuthCheck, continueHome string) (string, bool) {
	for i := 0; i < len(check.Args); i++ {
		arg := check.Args[i]
		if arg == "--" {
			break
		}
		path := ""
		selected := false
		if value, ok := strings.CutPrefix(arg, "--config="); ok {
			path, selected = value, true
		} else if arg == "--config" {
			selected = true
			if i+1 < len(check.Args) && !strings.HasPrefix(check.Args[i+1], "-") {
				i++
				path = check.Args[i]
			}
		}
		if !selected {
			continue
		}
		path = strings.TrimSpace(path)
		if path == "" {
			return "", true
		}
		if filepath.IsAbs(path) {
			return filepath.Clean(path), true
		}
		if filepath.IsAbs(check.WorkingDir) {
			return filepath.Join(check.WorkingDir, path), true
		}
		return "", true
	}
	return filepath.Join(continueHome, "config.yaml"), false
}

func continueHasModelFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "--model" || strings.HasPrefix(arg, "--model=") {
			return true
		}
	}
	return false
}

func continueChatModel(roles []string) bool {
	if roles == nil {
		return true
	}
	for _, role := range roles {
		if role == "chat" {
			return true
		}
	}
	return false
}

func continueSecretEnv(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "${{") || !strings.HasSuffix(value, "}}") {
		return "", false
	}
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "${{"), "}}"))
	name, ok := strings.CutPrefix(inner, "secrets.")
	if !ok || strings.ContainsAny(name, " \t\r\n}") {
		return "", true
	}
	parts := strings.Split(name, "/")
	if len(parts)%2 == 0 {
		return "", true
	}
	name = parts[len(parts)-1]
	if name == "" {
		return "", false
	}
	for i, char := range name {
		if char == '_' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || i > 0 && char >= '0' && char <= '9' {
			continue
		}
		return "", false
	}
	return name, true
}

func continueSecretValue(
	ctx context.Context,
	d authutil.Dependencies,
	check ports.AgentAuthCheck,
	continueHome string,
	name string,
	lookup func(string) string,
) string {
	if value := lookup(name); value != "" {
		return value
	}
	paths := []string{filepath.Join(continueHome, ".env")}
	if filepath.IsAbs(check.WorkingDir) {
		paths = append(paths,
			filepath.Join(check.WorkingDir, ".continue", ".env"),
			filepath.Join(check.WorkingDir, ".env"),
		)
	}
	for _, path := range paths {
		data, err := authutil.ReadFile(ctx, d, path)
		if err != nil {
			continue
		}
		values, err := authutil.ParseDotenv(data)
		if err == nil && strings.TrimSpace(values[name]) != "" {
			return strings.TrimSpace(values[name])
		}
	}
	return ""
}
