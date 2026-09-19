package kimi

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

// AuthStatus checks device-wide defaults using the scoped resolver.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.AuthStatusFor(ctx, ports.AgentAuthCheck{})
}

// AuthStatusFor checks credentials for the effective Kimi invocation.
func (p *Plugin) AuthStatusFor(ctx context.Context, check ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return kimiAuthStatus(ctx, check)
}

func kimiLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	status, err := kimiAuthStatus(ctx, ports.AgentAuthCheck{})
	return status, status != ports.AgentAuthStatusUnknown, err
}

type kimiOAuthRef struct {
	Storage string `json:"storage" toml:"storage"`
	Key     string `json:"key" toml:"key"`
}

type kimiCredentialSource struct {
	Type          string            `json:"type" toml:"type"`
	APIKey        string            `json:"api_key" toml:"api_key"`
	APIKeyEnv     string            `json:"api_key_env" toml:"api_key_env"`
	Env           map[string]string `json:"env" toml:"env"`
	CustomHeaders map[string]string `json:"custom_headers" toml:"custom_headers"`
	OAuth         *kimiOAuthRef     `json:"oauth" toml:"oauth"`
}

type kimiAuthConfig struct {
	DefaultModel string `json:"default_model" toml:"default_model"`
	Models       map[string]struct {
		Provider string `json:"provider" toml:"provider"`
		Model    string `json:"model" toml:"model"`
	} `json:"models" toml:"models"`
	Providers map[string]kimiCredentialSource `json:"providers" toml:"providers"`
}

func kimiAuthStatus(ctx context.Context, check ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	getenv := func(name string) string {
		if value, exists := check.Env[name]; exists {
			return strings.TrimSpace(value)
		}
		return strings.TrimSpace(os.Getenv(name))
	}
	d := authutil.Dependencies{Getenv: getenv}
	model := strings.TrimSpace(check.Config.Model)
	for i, arg := range check.Args {
		if arg == "--" {
			break
		}
		if (arg == "--model" || arg == "-m") && i+1 < len(check.Args) {
			model = strings.TrimSpace(check.Args[i+1])
		}
		if value, ok := strings.CutPrefix(arg, "--model="); ok {
			model = strings.TrimSpace(value)
		}
	}
	if model == "" && getenv("KIMI_MODEL_NAME") != "" {
		switch getenv("KIMI_MODEL_PROVIDER_TYPE") {
		case "", "kimi", "anthropic", "openai":
			if getenv("KIMI_MODEL_API_KEY") != "" {
				return ports.AgentAuthStatusConfigured, nil
			}
		}
		return ports.AgentAuthStatusUnknown, nil
	}
	userHome := getenv("HOME")
	if runtime.GOOS == "windows" {
		userHome = getenv("USERPROFILE")
	}
	current, legacy := getenv(kimiCodeHomeEnv), getenv("KIMI_SHARE_DIR")
	if current == "" && userHome != "" {
		current = filepath.Join(userHome, ".kimi-code")
	}
	if legacy == "" && userHome != "" {
		legacy = filepath.Join(userHome, ".kimi")
	}
	// Current Kimi Code takes precedence. Legacy JSON migration and shell key
	// fallback are used only when a legacy config is actually selected.
	for i, home := range []string{current, legacy} {
		if home == "" {
			continue
		}
		if !filepath.IsAbs(home) {
			if !filepath.IsAbs(check.WorkingDir) {
				continue
			}
			home = filepath.Join(check.WorkingDir, home)
		}
		for _, filename := range []string{"config.toml", "config.json"} {
			if i == 0 && filename == "config.json" {
				continue
			}
			path := filepath.Join(home, filename)
			config, err := readKimiAuthConfig(ctx, d, path)
			if err != nil {
				continue
			}
			selected := model
			if selected == "" {
				selected = strings.TrimSpace(config.DefaultModel)
			}
			entry, exists := config.Models[selected]
			if !exists || strings.TrimSpace(entry.Model) == "" {
				return ports.AgentAuthStatusUnknown, ctx.Err()
			}
			provider, exists := config.Providers[entry.Provider]
			if !exists {
				return ports.AgentAuthStatusUnknown, ctx.Err()
			}
			return kimiProviderAuthStatus(ctx, d, home, check.WorkingDir, provider, i == 1), ctx.Err()
		}
	}
	return ports.AgentAuthStatusUnknown, ctx.Err()
}

func kimiProviderAuthStatus(ctx context.Context, d authutil.Dependencies, home, workingDir string, provider kimiCredentialSource, legacy bool) ports.AgentAuthStatus {
	var envKeys []string
	switch provider.Type {
	case "kimi":
		envKeys = []string{"KIMI_API_KEY"}
	case "openai", "openai_responses":
		envKeys = []string{"OPENAI_API_KEY"}
	case "anthropic":
		envKeys = []string{"ANTHROPIC_API_KEY"}
	case "google-genai":
		envKeys = []string{"GOOGLE_API_KEY"}
	case "vertexai":
		envKeys = []string{"VERTEXAI_API_KEY", "GOOGLE_API_KEY"}
	case "openai_legacy", "openai-compatible":
		if !legacy {
			return ports.AgentAuthStatusUnknown
		}
		envKeys = []string{"OPENAI_API_KEY"}
	case "gemini", "google_genai":
		if !legacy {
			return ports.AgentAuthStatusUnknown
		}
		envKeys = []string{"GOOGLE_API_KEY"}
	case "_echo", "_scripted_echo":
		if legacy {
			return ports.AgentAuthStatusNotApplicable
		}
		return ports.AgentAuthStatusUnknown
	default:
		return ports.AgentAuthStatusUnknown
	}
	apiKey, envKey := strings.TrimSpace(provider.APIKey), strings.TrimSpace(provider.APIKeyEnv)
	if envKey != "" {
		if apiKey != "" || provider.OAuth != nil {
			return ports.AgentAuthStatusUnknown
		}
		apiKey = strings.TrimSpace(d.Getenv(envKey))
		if apiKey == "" {
			return ports.AgentAuthStatusUnknown
		}
	}
	if apiKey != "" && provider.OAuth != nil {
		return ports.AgentAuthStatusUnknown
	}
	// Only these protocols replace their generated bearer token with the exact
	// Authorization header; a case variant has different native semantics.
	if !legacy && (provider.Type == "kimi" || provider.Type == "openai" || provider.Type == "openai_responses") {
		value, found := provider.CustomHeaders["Authorization"]
		if !found {
			headers := d.Getenv("KIMI_CODE_CUSTOM_HEADERS")
			if len(headers) <= authutil.MaxFileSize {
				for _, line := range strings.Split(headers, "\n") {
					name, header, ok := strings.Cut(line, ":")
					if ok && strings.TrimSpace(name) == "Authorization" {
						value, found = header, true
					}
				}
			}
		}
		if found {
			if strings.TrimSpace(value) != "" {
				return ports.AgentAuthStatusConfigured
			}
			return ports.AgentAuthStatusUnknown
		}
	}
	if legacy && (provider.Type == "kimi" || provider.Type == "openai_legacy" || provider.Type == "openai_responses") {
		if strings.TrimSpace(d.Getenv(envKeys[0])) != "" {
			return ports.AgentAuthStatusConfigured
		}
	}
	if apiKey != "" {
		return ports.AgentAuthStatusConfigured
	}
	for _, key := range envKeys {
		if strings.TrimSpace(provider.Env[key]) != "" {
			if provider.OAuth != nil {
				return ports.AgentAuthStatusUnknown
			}
			return ports.AgentAuthStatusConfigured
		}
	}
	if provider.Type == "vertexai" && strings.TrimSpace(provider.Env["GOOGLE_CLOUD_PROJECT"]) != "" && strings.TrimSpace(provider.Env["GOOGLE_CLOUD_LOCATION"]) != "" {
		cloud := d
		cloud.Getenv = func(name string) string {
			value := d.Getenv(name)
			// Native Google credential paths are relative to the session's cwd,
			// which can differ from the daemon performing this scoped check.
			if (name == "GOOGLE_APPLICATION_CREDENTIALS" || name == "CLOUDSDK_CONFIG") && value != "" && !filepath.IsAbs(value) && filepath.IsAbs(workingDir) {
				return filepath.Join(workingDir, value)
			}
			return value
		}
		if evidence := authutil.GoogleADCEvidence(ctx, cloud); evidence.Status == ports.AgentAuthStatusConfigured {
			return evidence.Status
		}
	}
	if provider.OAuth != nil && (provider.OAuth.Storage == "file" || provider.OAuth.Storage == "keyring" || provider.OAuth.Storage == "") {
		path := kimiOAuthCredentialPath(home, provider.OAuth.Key)
		if path != "" {
			if data, err := authutil.ReadFile(ctx, d, path); err == nil && kimiStoredToken(data) {
				return ports.AgentAuthStatusConfigured
			}
		}
		// Legacy Python Kimi migrates this fixed keyring account to its file
		// store. Never query arbitrary account names supplied by a config.
		if legacy && provider.OAuth.Storage == "keyring" && provider.OAuth.Key == "oauth/kimi-code" {
			if data, err := authutil.GenericPassword(ctx, d, "kimi-code", "oauth/kimi-code"); err == nil && kimiStoredToken(data) {
				return ports.AgentAuthStatusConfigured
			}
		}
	}
	return ports.AgentAuthStatusUnknown
}

func readKimiAuthConfig(ctx context.Context, d authutil.Dependencies, path string) (kimiAuthConfig, error) {
	var config kimiAuthConfig
	var err error
	if strings.EqualFold(filepath.Ext(path), ".json") {
		err = authutil.ReadJSON(ctx, d, path, &config)
	} else {
		err = authutil.ReadTOML(ctx, d, path, &config)
	}
	return config, err
}

// kimiCodeHome is also used by runtime hooks to locate the user's source config.
func kimiCodeHome() (string, bool) {
	if home := strings.TrimSpace(os.Getenv(kimiCodeHomeEnv)); home != "" {
		return home, true
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	return filepath.Join(home, ".kimi-code"), true
}

// Hook seeding needs every explicit file reference so switching models still
// works after copying a profile. AuthStatus itself resolves only the active one.
func kimiConfigOAuthCredentialPaths(path string) ([]string, error) {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, nil
	}
	config, err := readKimiAuthConfig(context.Background(), authutil.Dependencies{}, path)
	if err != nil {
		return nil, err
	}
	var paths []string
	seen := make(map[string]bool)
	for _, provider := range config.Providers {
		if provider.OAuth == nil || (provider.OAuth.Storage != "file" && provider.OAuth.Storage != "keyring" && provider.OAuth.Storage != "") {
			continue
		}
		credentialPath := kimiOAuthCredentialPath(filepath.Dir(path), provider.OAuth.Key)
		if credentialPath != "" && !seen[credentialPath] {
			paths = append(paths, credentialPath)
			seen[credentialPath] = true
		}
	}
	return paths, nil
}

func kimiOAuthCredentialPath(home, key string) string {
	name := strings.TrimPrefix(strings.TrimSpace(key), "oauth/")
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return ""
	}
	return filepath.Join(home, "credentials", name+".json")
}

func kimiCredentialsAuthStatus(path string) (ports.AgentAuthStatus, bool) {
	data, err := authutil.ReadFile(context.Background(), authutil.Dependencies{}, path)
	if err == nil && kimiStoredToken(data) {
		return ports.AgentAuthStatusConfigured, true
	}
	return ports.AgentAuthStatusUnknown, false
}

func kimiStoredToken(data []byte) bool {
	var token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	return len(data) <= authutil.MaxFileSize && json.Unmarshal(data, &token) == nil && (strings.TrimSpace(token.AccessToken) != "" || strings.TrimSpace(token.RefreshToken) != "")
}
