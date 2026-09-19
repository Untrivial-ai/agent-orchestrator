package grok

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentScopedAuthChecker = (*Plugin)(nil)

// AuthStatus checks device-wide defaults using the same resolver as launches.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.AuthStatusFor(ctx, ports.AgentAuthCheck{})
}

func (p *Plugin) AuthStatusFor(ctx context.Context, check ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return grokAuthStatus(ctx, check)
}

func grokLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	status, err := grokAuthStatus(ctx, ports.AgentAuthCheck{})
	return status, status != ports.AgentAuthStatusUnknown, err
}

type grokAuthConfig struct {
	Models struct {
		Default string `toml:"default"`
	} `toml:"models"`
	Model map[string]struct {
		APIKey  string `toml:"api_key"`
		EnvKey  string `toml:"env_key"`
		BaseURL string `toml:"base_url"`
	} `toml:"model"`
	Endpoints struct {
		DeploymentKey string `toml:"deployment_key"`
	} `toml:"endpoints"`
}

func grokAuthStatus(ctx context.Context, check ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	getenv := func(name string) string {
		if value, exists := check.Env[name]; exists {
			return strings.TrimSpace(value)
		}
		return strings.TrimSpace(os.Getenv(name))
	}
	home := getenv("GROK_HOME")
	if home == "" {
		userHome := getenv("HOME")
		if runtime.GOOS == "windows" {
			userHome = getenv("USERPROFILE")
		}
		if userHome != "" {
			home = filepath.Join(userHome, ".grok")
		}
	}
	if home != "" && !filepath.IsAbs(home) {
		if filepath.IsAbs(check.WorkingDir) {
			home = filepath.Join(check.WorkingDir, home)
		} else {
			home = ""
		}
	}
	d := authutil.Dependencies{Getenv: getenv}
	var config grokAuthConfig
	if home != "" {
		// Discard malformed sources completely, including partially decoded data.
		if authutil.ReadTOML(ctx, d, filepath.Join(home, "config.toml"), &config) != nil {
			config = grokAuthConfig{}
		}
	}
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
	if model == "" {
		model = getenv("GROK_DEFAULT_MODEL")
	}
	if model == "" {
		model = strings.TrimSpace(config.Models.Default)
	}
	if model == "" {
		model = "grok-build"
	}
	selected, exists := config.Model[model]
	if exists {
		if grokSecret(selected.APIKey) || (strings.TrimSpace(selected.EnvKey) != "" && getenv(selected.EnvKey) != "") {
			return ports.AgentAuthStatusConfigured, ctx.Err()
		}
		// BYOK models cannot borrow credentials from the hosted Grok login.
		if strings.TrimSpace(selected.EnvKey) != "" || !grokHostedEndpoint(selected.BaseURL) {
			return ports.AgentAuthStatusUnknown, ctx.Err()
		}
	} else if !strings.HasPrefix(model, "grok-") {
		return ports.AgentAuthStatusUnknown, ctx.Err()
	}
	if getenv("XAI_API_KEY") != "" || getenv("GROK_DEPLOYMENT_KEY") != "" || grokSecret(config.Endpoints.DeploymentKey) {
		return ports.AgentAuthStatusConfigured, ctx.Err()
	}
	if grokStoredKey([]byte(getenv("GROK_AUTH"))) {
		return ports.AgentAuthStatusConfigured, ctx.Err()
	}
	path := getenv("GROK_AUTH_PATH")
	if path == "" && home != "" {
		path = filepath.Join(home, "auth.json")
	}
	if path != "" && !filepath.IsAbs(path) {
		if filepath.IsAbs(check.WorkingDir) {
			path = filepath.Join(check.WorkingDir, path)
		} else {
			path = ""
		}
	}
	if path != "" {
		if data, err := authutil.ReadFile(ctx, d, path); err == nil && grokStoredKey(data) {
			return ports.AgentAuthStatusConfigured, ctx.Err()
		}
	}
	return ports.AgentAuthStatusUnknown, ctx.Err()
}

func grokHostedEndpoint(value string) bool {
	if strings.TrimSpace(value) == "" {
		return true
	}
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "https" && u.Host == "api.x.ai" && u.User == nil
}

func grokSecret(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.HasPrefix(value, "$")
}

// Auth entries are one fixed level under account identifiers; arbitrary nested
// keys and the old access_token/refresh_token fields are not credential evidence.
func grokStoredKey(data []byte) bool {
	if len(data) > authutil.MaxFileSize {
		return false
	}
	var entries map[string]json.RawMessage
	if json.Unmarshal(data, &entries) != nil {
		return false
	}
	for account, raw := range entries {
		if strings.TrimSpace(account) == "" {
			continue
		}
		var entry struct {
			Key string `json:"key"`
		}
		if json.Unmarshal(raw, &entry) == nil && grokSecret(entry.Key) {
			return true
		}
	}
	return false
}
