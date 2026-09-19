package vibe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentScopedAuthChecker = (*Plugin)(nil)

const (
	vibeDefaultAPIKeyEnvVar = "MISTRAL_API_KEY" //nolint:gosec // env var name, not a credential value
	vibeDefaultModelAlias   = "mistral-medium-3.5"
)

type vibeConfigLayer struct {
	ActiveModel *string             `toml:"active_model"`
	Providers   []vibeProviderLayer `toml:"providers"`
	Models      []vibeModelLayer    `toml:"models"`
}

type vibeProviderLayer struct {
	Name         *string `toml:"name"`
	APIKeyEnvVar *string `toml:"api_key_env_var"`
}

type vibeModelLayer struct {
	Name     *string `toml:"name"`
	Provider *string `toml:"provider"`
	Alias    *string `toml:"alias"`
}

type vibeResolvedProvider struct {
	Name         string
	APIKeyEnvVar string
}

type vibeResolvedModel struct {
	Name     string
	Provider string
	Alias    string
}

type vibeResolvedConfig struct {
	ActiveModel string
	Providers   map[string]vibeResolvedProvider
	Models      map[string]vibeResolvedModel
}

func defaultVibeConfig() vibeResolvedConfig {
	return vibeResolvedConfig{
		ActiveModel: vibeDefaultModelAlias,
		Providers: map[string]vibeResolvedProvider{
			"mistral":  {Name: "mistral", APIKeyEnvVar: vibeDefaultAPIKeyEnvVar},
			"llamacpp": {Name: "llamacpp"},
		},
		Models: map[string]vibeResolvedModel{
			vibeDefaultModelAlias: {Name: "mistral-vibe-cli-latest", Provider: "mistral", Alias: vibeDefaultModelAlias},
			"local":               {Name: "devstral", Provider: "llamacpp", Alias: "local"},
		},
	}
}

func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.AuthStatusFor(ctx, ports.AgentAuthCheck{})
}

func (p *Plugin) AuthStatusFor(ctx context.Context, check ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return p.authStatusFor(ctx, check, authutil.Dependencies{})
}

func (p *Plugin) authStatusFor(ctx context.Context, check ports.AgentAuthCheck, d authutil.Dependencies) (ports.AgentAuthStatus, error) {
	return vibeAuthStatus(ctx, check, d)
}

func vibeAuthStatus(ctx context.Context, check ports.AgentAuthCheck, d authutil.Dependencies) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	baseGetenv := d.Getenv
	if baseGetenv == nil {
		baseGetenv = os.Getenv
	}
	d.Getenv = func(name string) string {
		if value, ok := check.Env[name]; ok {
			return value
		}
		return baseGetenv(name)
	}

	home := strings.TrimSpace(d.Getenv("VIBE_HOME"))
	if home == "" {
		userHome := strings.TrimSpace(d.Getenv("HOME"))
		if userHome == "" {
			return ports.AgentAuthStatusUnknown, nil
		}
		home = filepath.Join(userHome, ".vibe")
	}

	resolved := defaultVibeConfig()
	if ok := applyVibeConfigFile(ctx, d, filepath.Join(home, "config.toml"), &resolved); !ok {
		return ports.AgentAuthStatusUnknown, ctx.Err()
	}
	if check.WorkingDir != "" {
		if !filepath.IsAbs(check.WorkingDir) {
			return ports.AgentAuthStatusUnknown, nil
		}
		paths, err := authutil.FindUpward(ctx, d, check.WorkingDir, filepath.Join(".vibe", "config.toml"))
		if err != nil {
			return ports.AgentAuthStatusUnknown, err
		}
		if len(paths) > 0 && filepath.Clean(paths[0]) != filepath.Clean(filepath.Join(home, "config.toml")) {
			if ok := applyVibeConfigFile(ctx, d, paths[0], &resolved); !ok {
				return ports.AgentAuthStatusUnknown, ctx.Err()
			}
		}
	}

	model, ok := resolved.Models[resolved.ActiveModel]
	if !ok || strings.TrimSpace(model.Provider) == "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	provider, ok := resolved.Providers[model.Provider]
	if !ok {
		return ports.AgentAuthStatusUnknown, nil
	}
	if provider.Name == "llamacpp" {
		return ports.AgentAuthStatusNotApplicable, nil
	}
	envName := strings.TrimSpace(provider.APIKeyEnvVar)
	if envName == "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	if usableSecret(d.Getenv(envName)) {
		return ports.AgentAuthStatusConfigured, nil
	}
	data, exists, err := vibeOptionalFile(ctx, d, filepath.Join(home, ".env"))
	if err != nil || !exists {
		return ports.AgentAuthStatusUnknown, err
	}
	values, err := authutil.ParseDotenv(data)
	if err != nil {
		return ports.AgentAuthStatusUnknown, nil
	}
	if usableSecret(values[envName]) {
		return ports.AgentAuthStatusConfigured, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

func applyVibeConfigFile(ctx context.Context, d authutil.Dependencies, path string, resolved *vibeResolvedConfig) bool {
	_, exists, err := vibeOptionalFile(ctx, d, path)
	if err != nil {
		return false
	}
	if !exists {
		return true
	}
	var layer vibeConfigLayer
	if authutil.ReadTOML(ctx, d, path, &layer) != nil {
		return false
	}
	if layer.ActiveModel != nil {
		resolved.ActiveModel = strings.TrimSpace(*layer.ActiveModel)
		if resolved.ActiveModel == "" {
			resolved.ActiveModel = vibeDefaultModelAlias
		}
	}
	seenProviders := make(map[string]bool)
	for _, patch := range layer.Providers {
		if patch.Name == nil || strings.TrimSpace(*patch.Name) == "" {
			return false
		}
		name := strings.TrimSpace(*patch.Name)
		if seenProviders[name] {
			return false
		}
		seenProviders[name] = true
		provider := resolved.Providers[name]
		provider.Name = name
		if patch.APIKeyEnvVar != nil {
			provider.APIKeyEnvVar = strings.TrimSpace(*patch.APIKeyEnvVar)
		}
		resolved.Providers[name] = provider
	}
	seenModels := make(map[string]bool)
	for _, patch := range layer.Models {
		key := ""
		if patch.Alias != nil {
			key = strings.TrimSpace(*patch.Alias)
		} else if patch.Name != nil {
			key = strings.TrimSpace(*patch.Name)
		}
		if key == "" || seenModels[key] {
			return false
		}
		seenModels[key] = true
		model, exists := resolved.Models[key]
		if patch.Name != nil {
			model.Name = strings.TrimSpace(*patch.Name)
		}
		if patch.Provider != nil {
			model.Provider = strings.TrimSpace(*patch.Provider)
		}
		if patch.Alias != nil {
			model.Alias = strings.TrimSpace(*patch.Alias)
		} else if model.Alias == "" {
			model.Alias = key
		}
		if !exists && (model.Name == "" || model.Provider == "") {
			return false
		}
		resolved.Models[key] = model
	}
	return true
}

func vibeOptionalFile(ctx context.Context, d authutil.Dependencies, path string) ([]byte, bool, error) {
	lstat := d.Lstat
	if lstat == nil {
		lstat = os.Lstat
	}
	if _, err := lstat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, errors.New("credential file unavailable")
	}
	data, err := authutil.ReadFile(ctx, d, path)
	return data, err == nil, err
}

func vibeLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	status, err := vibeAuthStatus(ctx, ports.AgentAuthCheck{}, authutil.Dependencies{})
	return status, status != ports.AgentAuthStatusUnknown, err
}

func vibeAPIKeyEnvVars(configPath string) ([]string, error) {
	vars := []string{vibeDefaultAPIKeyEnvVar}
	data, err := os.ReadFile(configPath) //nolint:gosec // compatibility helper for the user's config
	if errors.Is(err, os.ErrNotExist) {
		return vars, nil
	}
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.TrimSpace(key) == "api_key_env_var" {
			value = strings.Trim(strings.TrimSpace(value), `"',`)
			if value != "" && !containsString(vars, value) {
				vars = append(vars, value)
			}
		}
	}
	return vars, nil
}

func vibeEnvFileAuthStatus(path, envVar string) (ports.AgentAuthStatus, bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // compatibility helper for the user's config
	if errors.Is(err, os.ErrNotExist) {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	values, err := authutil.ParseDotenv(data)
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if usableSecret(values[envVar]) {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

func usableSecret(value string) bool { return strings.TrimSpace(value) != "" }

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
