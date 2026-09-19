package qwen

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
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

func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.AuthStatusFor(ctx, ports.AgentAuthCheck{})
}

func (p *Plugin) AuthStatusFor(ctx context.Context, scope ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	// /doctor is an interactive command, not a safe native status probe.
	return qwenAuthStatus(ctx, scope, authutil.Dependencies{})
}

type qwenAuthModel struct {
	ID      string `json:"id"`
	BaseURL string `json:"baseUrl"`
	EnvKey  string `json:"envKey"`
	WireAPI string `json:"wireApi"`
}

type qwenAuthSettings struct {
	Security struct {
		Auth struct {
			SelectedType string `json:"selectedType"`
			APIKey       string `json:"apiKey"`
			BaseURL      string `json:"baseUrl"`
		} `json:"auth"`
	} `json:"security"`
	Model struct {
		Name    string `json:"name"`
		BaseURL string `json:"baseUrl"`
	} `json:"model"`
	ModelProviders   map[string][]qwenAuthModel `json:"modelProviders"`
	ProviderProtocol map[string]string          `json:"providerProtocol"`
	Env              map[string]string          `json:"env"`
	providerOrder    []string
}

func qwenAuthStatus(ctx context.Context, scope ports.AgentAuthCheck, d authutil.Dependencies) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	baseEnv := d.Getenv
	if baseEnv == nil {
		baseEnv = os.Getenv
	}
	env := func(key string) string {
		if value, ok := scope.Env[key]; ok {
			return value
		}
		return baseEnv(key)
	}
	d.Getenv = env
	if d.GOOS == "" {
		d.GOOS = runtime.GOOS
	}
	home := env("HOME")
	if d.GOOS == "windows" {
		home = env("USERPROFILE")
	}
	qwenHome := env("QWEN_HOME")
	if qwenHome == "" {
		if home != "" {
			qwenHome = filepath.Join(home, ".qwen")
		}
	} else if qwenHome == "~" {
		qwenHome = home
	} else if strings.HasPrefix(qwenHome, "~/") {
		qwenHome = filepath.Join(home, qwenHome[2:])
	}
	system, defaults := qwenSystemAuthPaths(d.GOOS, env)
	paths := []string{defaults}
	if qwenHome != "" {
		paths = append(paths, filepath.Join(qwenHome, "settings.json"))
	}
	if filepath.IsAbs(scope.WorkingDir) {
		found, _ := authutil.FindUpward(ctx, d, scope.WorkingDir, filepath.Join(".qwen", "settings.json"))
		// Native discovery selects the closest project settings file.
		if len(found) > 0 {
			paths = append(paths, found[0])
		}
	}
	paths = append(paths, system)
	cfg := qwenAuthSettings{Env: map[string]string{}}
	for _, path := range paths {
		data, err := authutil.ReadFile(ctx, d, path)
		if err != nil {
			stat := d.Lstat
			if stat == nil {
				stat = os.Lstat
			}
			if _, statErr := stat(path); !os.IsNotExist(statErr) {
				return ports.AgentAuthStatusUnknown, ctx.Err()
			}
			continue
		}
		if !cfg.readJSON(data) {
			// An invalid settings layer leaves the effective provider and
			// credentials ambiguous, even if a lower layer contains a key.
			return ports.AgentAuthStatusUnknown, ctx.Err()
		}
	}

	var dotenvPaths []string
	if filepath.IsAbs(scope.WorkingDir) {
		found, _ := authutil.FindUpward(ctx, d, scope.WorkingDir, filepath.Join(".qwen", ".env"), ".env")
		if len(found) > 0 {
			dotenvPaths = append(dotenvPaths, found[0])
		}
	}
	if qwenHome != "" {
		dotenvPaths = append(dotenvPaths, filepath.Join(qwenHome, ".env"))
	}
	if home != "" {
		dotenvPaths = append(dotenvPaths, filepath.Join(home, ".qwen", ".env"), filepath.Join(home, ".env"))
	}
	fileEnv := make(map[string]string)
	for _, path := range dotenvPaths {
		data, err := authutil.ReadFile(ctx, d, path)
		if err != nil {
			stat := d.Lstat
			if stat == nil {
				stat = os.Lstat
			}
			if _, statErr := stat(path); !os.IsNotExist(statErr) {
				return ports.AgentAuthStatusUnknown, ctx.Err()
			}
			continue
		}
		values, err := authutil.ParseDotenv(data)
		if err != nil {
			return ports.AgentAuthStatusUnknown, ctx.Err()
		}
		for key, value := range values {
			if fileEnv[key] == "" {
				fileEnv[key] = value
			}
		}
	}
	// Process/launch env > closest project dotenv > home dotenv > settings.env.
	d.Getenv = func(key string) string {
		if value := env(key); value != "" {
			return value
		}
		if value := fileEnv[key]; value != "" {
			return value
		}
		return cfg.Env[key]
	}
	status := cfg.evidence(ctx, scope, d)
	return status, ctx.Err()
}

func qwenSystemAuthPaths(goos string, env func(string) string) (string, string) {
	system := env("QWEN_CODE_SYSTEM_SETTINGS_PATH")
	if system == "" {
		switch goos {
		case "darwin":
			system = "/Library/Application Support/QwenCode/settings.json"
		case "windows":
			system = `C:\ProgramData\qwen-code\settings.json`
		default:
			system = "/etc/qwen-code/settings.json"
		}
	}
	defaults := env("QWEN_CODE_SYSTEM_DEFAULTS_PATH")
	if defaults == "" {
		if goos == "windows" && strings.Contains(system, `\`) {
			defaults = system[:strings.LastIndex(system, `\`)+1] + "system-defaults.json"
		} else {
			defaults = filepath.Join(filepath.Dir(system), "system-defaults.json")
		}
	}
	return system, defaults
}

func (c *qwenAuthSettings) readJSON(data []byte) bool {
	// Only schema-owned fields are decoded. Model-provider and protocol maps
	// replace their whole lower-priority map, matching Qwen's merge strategy.
	var layer struct {
		Security         json.RawMessage
		Model            json.RawMessage
		ModelProviders   json.RawMessage `json:"modelProviders"`
		ProviderProtocol json.RawMessage `json:"providerProtocol"`
		Env              map[string]string
	}
	if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) || json.Unmarshal(data, &layer) != nil {
		return false
	}
	if len(layer.Security) > 0 {
		value := c.Security
		if json.Unmarshal(layer.Security, &value) != nil {
			return false
		}
		c.Security = value
	}
	if len(layer.Model) > 0 {
		value := c.Model
		if json.Unmarshal(layer.Model, &value) != nil {
			return false
		}
		c.Model = value
	}
	if len(layer.ModelProviders) > 0 {
		var entries map[string]json.RawMessage
		if json.Unmarshal(layer.ModelProviders, &entries) != nil {
			return false
		}
		c.ModelProviders = make(map[string][]qwenAuthModel)
		c.providerOrder = nil
		// Preserve JSON declaration order: Qwen uses the first matching
		// route when no persisted endpoint disambiguates duplicate IDs.
		decoder := json.NewDecoder(bytes.NewReader(layer.ModelProviders))
		if _, err := decoder.Token(); err != nil {
			return false
		}
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return false
			}
			id, ok := token.(string)
			if !ok {
				return false
			}
			var raw json.RawMessage
			if decoder.Decode(&raw) != nil {
				return false
			}
			var models []qwenAuthModel
			if json.Unmarshal(raw, &models) != nil {
				return false
			}
			c.ModelProviders[id] = models
			c.providerOrder = append(c.providerOrder, id)
		}
	}
	if len(layer.ProviderProtocol) > 0 {
		var protocols map[string]string
		if json.Unmarshal(layer.ProviderProtocol, &protocols) != nil {
			return false
		}
		c.ProviderProtocol = protocols
	}
	for key, value := range layer.Env {
		c.Env[key] = value
	}
	return true
}

type qwenProtocolEnv struct {
	Key, BaseURL string
	Models       []string
}

var qwenProtocolVars = map[string]qwenProtocolEnv{
	"openai":           {Key: "OPENAI_API_KEY", BaseURL: "OPENAI_BASE_URL", Models: []string{"OPENAI_MODEL", "QWEN_MODEL"}},
	"openai-responses": {Key: "OPENAI_API_KEY", BaseURL: "OPENAI_BASE_URL", Models: []string{"OPENAI_MODEL"}},
	"anthropic":        {Key: "ANTHROPIC_API_KEY", BaseURL: "ANTHROPIC_BASE_URL", Models: []string{"ANTHROPIC_MODEL"}},
	"gemini":           {Key: "GEMINI_API_KEY", Models: []string{"GEMINI_MODEL"}},
	"vertex-ai":        {Key: "GOOGLE_API_KEY", Models: []string{"GOOGLE_MODEL"}},
}

func (c *qwenAuthSettings) evidence(ctx context.Context, scope ports.AgentAuthCheck, d authutil.Dependencies) ports.AgentAuthStatus {
	authType, model, cliKey, cliURL := c.Security.Auth.SelectedType, scope.Config.Model, "", ""
	for i := 0; i < len(scope.Args); i++ {
		arg := scope.Args[i]
		if arg == "--" {
			break
		}
		name, value, inline := strings.Cut(arg, "=")
		switch name {
		case "--auth-type", "--model", "-m", "--openai-api-key", "--openai-base-url":
		default:
			continue
		}
		if !inline {
			if i+1 >= len(scope.Args) {
				return ports.AgentAuthStatusUnknown
			}
			i++
			value = scope.Args[i]
		}
		switch name {
		case "--auth-type":
			authType = value
		case "--model", "-m":
			model = value
		case "--openai-api-key":
			cliKey = value
		case "--openai-base-url":
			cliURL = value
		}
	}
	if authType == "" {
		authType = qwenAuthTypeFromEnv(d.Getenv)
	}
	vars, known := qwenProtocolVars[authType]
	if !known {
		return ports.AgentAuthStatusUnknown
	}
	pairedURL := ""
	if model == "" {
		model = c.Model.Name
		pairedURL = qwenAuthValue(c.Model.BaseURL, d.Getenv)
	}
	if model == "" {
		for _, key := range vars.Models {
			if model = strings.TrimSpace(d.Getenv(key)); model != "" {
				break
			}
		}
	}
	model = qwenAuthValue(model, d.Getenv)
	if model == "" {
		return ports.AgentAuthStatusUnknown
	}
	entry, found := c.selectedModel(authType, model, pairedURL, d.Getenv)
	baseURL := ""
	if found {
		baseURL = qwenAuthValue(entry.BaseURL, d.Getenv)
	}
	if baseURL == "" {
		baseURL = qwenAuthValue(cliURL, d.Getenv)
	}
	if baseURL == "" && vars.BaseURL != "" {
		baseURL = strings.TrimSpace(d.Getenv(vars.BaseURL))
	}
	if baseURL == "" {
		baseURL = qwenAuthValue(c.Security.Auth.BaseURL, d.Getenv)
	}
	var endpoint *url.URL
	if baseURL != "" {
		var err error
		endpoint, err = url.Parse(baseURL)
		if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" {
			return ports.AgentAuthStatusUnknown
		}
	}
	if (authType == "openai" || authType == "openai-responses") && endpoint == nil {
		return ports.AgentAuthStatusUnknown
	}
	if found && entry.EnvKey != "" {
		if strings.TrimSpace(d.Getenv(entry.EnvKey)) != "" {
			return ports.AgentAuthStatusConfigured
		}
		// An explicit credential slot must not silently choose another principal.
		if strings.TrimSpace(cliKey) != "" && (authType == "openai" || authType == "openai-responses") {
			return ports.AgentAuthStatusConfigured
		}
		return ports.AgentAuthStatusUnknown
	}
	if strings.TrimSpace(cliKey) != "" && (authType == "openai" || authType == "openai-responses") {
		return ports.AgentAuthStatusConfigured
	}
	if strings.TrimSpace(d.Getenv(vars.Key)) != "" || qwenAuthValue(c.Security.Auth.APIKey, d.Getenv) != "" {
		return ports.AgentAuthStatusConfigured
	}
	if authType == "vertex-ai" && strings.TrimSpace(d.Getenv("GOOGLE_CLOUD_PROJECT")) != "" {
		return authutil.GoogleADCEvidence(ctx, d).Status
	}
	if found && endpoint != nil && (authType == "openai" || authType == "openai-responses") {
		host := endpoint.Hostname()
		ip := net.ParseIP(host)
		if host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return authutil.NoAuthEvidence(true).Status
		}
	}
	return ports.AgentAuthStatusUnknown
}

func qwenAuthTypeFromEnv(env func(string) string) string {
	if env("QWEN_OAUTH") != "" {
		return "qwen-oauth"
	}
	if env("OPENAI_API_KEY") != "" && (env("OPENAI_MODEL") != "" || env("QWEN_MODEL") != "") && env("OPENAI_BASE_URL") != "" {
		return "openai"
	}
	if env("GEMINI_API_KEY") != "" && env("GEMINI_MODEL") != "" {
		return "gemini"
	}
	if (env("GOOGLE_API_KEY") != "" || env("GOOGLE_CLOUD_PROJECT") != "") && env("GOOGLE_MODEL") != "" {
		return "vertex-ai"
	}
	if env("ANTHROPIC_API_KEY") != "" && env("ANTHROPIC_MODEL") != "" && env("ANTHROPIC_BASE_URL") != "" {
		return "anthropic"
	}
	return ""
}

func (c *qwenAuthSettings) selectedModel(authType, model, baseURL string, env func(string) string) (qwenAuthModel, bool) {
	// The selected endpoint disambiguates first, then the native first-ID
	// fallback applies. OpenAI may use Responses only when no Chat route matches.
	protocols := []string{authType}
	if authType == "openai" {
		protocols = append(protocols, "openai-responses")
	}
	for _, exactURL := range []bool{true, false} {
		if exactURL && baseURL == "" {
			continue
		}
		for _, requested := range protocols {
			for _, id := range c.providerOrder {
				protocol := id
				if mapped, ok := c.ProviderProtocol[id]; ok {
					protocol = mapped
				}
				for _, entry := range c.ModelProviders[id] {
					effective := protocol
					if protocol == "openai" || protocol == "openai-responses" {
						if entry.WireAPI == "responses" {
							effective = "openai-responses"
						} else if entry.WireAPI == "chat-completions" {
							effective = "openai"
						} else if entry.WireAPI != "" {
							continue
						}
					} else if entry.WireAPI != "" {
						continue
					}
					if entry.ID != model || effective != requested {
						continue
					}
					if !exactURL || qwenAuthValue(entry.BaseURL, env) == baseURL {
						return entry, true
					}
				}
			}
		}
	}
	return qwenAuthModel{}, false
}

func qwenAuthValue(value string, env func(string) string) string {
	// No command expansion; unresolved settings placeholders are not secrets.
	if strings.Contains(value, "$(") || strings.ContainsAny(value, "`\n") {
		return ""
	}
	unresolved := false
	value = os.Expand(value, func(key string) string {
		v := env(key)
		if strings.TrimSpace(v) == "" {
			unresolved = true
		}
		return v
	})
	if unresolved {
		return ""
	}
	return strings.TrimSpace(value)
}
