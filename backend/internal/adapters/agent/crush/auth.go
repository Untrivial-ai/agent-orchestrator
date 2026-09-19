package crush

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentScopedAuthChecker = (*Plugin)(nil)

// AuthStatus checks device-wide defaults using the scoped resolver.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.AuthStatusFor(ctx, ports.AgentAuthCheck{})
}

// AuthStatusFor checks credentials for the effective Crush invocation.
func (p *Plugin) AuthStatusFor(ctx context.Context, scope ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return crushAuthStatus(ctx, scope, authutil.Dependencies{})
}

func crushLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	status, err := crushAuthStatus(ctx, ports.AgentAuthCheck{}, authutil.Dependencies{})
	return status, status != ports.AgentAuthStatusUnknown, err
}

type crushAuthProvider struct {
	Type    string `json:"type"`
	APIKey  string `json:"api_key"`
	BaseURL string `json:"base_url"`
	Disable bool   `json:"disable"`
	OAuth   *struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresAt    int64  `json:"expires_at"`
	} `json:"oauth"`
}

type crushAuthModel struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type crushAuthConfig struct {
	Providers map[string]crushAuthProvider
	Models    map[string]crushAuthModel
	Env       map[string]string
}

var crushProviderEnv = map[string]string{
	"hyper": "HYPER_API_KEY", "anthropic": "ANTHROPIC_API_KEY", "openai": "OPENAI_API_KEY",
	"vercel": "VERCEL_API_KEY", "gemini": "GEMINI_API_KEY", "zai": "ZAI_API_KEY",
	"minimax": "MINIMAX_API_KEY", "synthetic": "SYNTHETIC_API_KEY", "huggingface": "HF_TOKEN",
	"cerebras": "CEREBRAS_API_KEY", "openrouter": "OPENROUTER_API_KEY", "ionet": "IONET_API_KEY",
	"alibaba-singapore": "ALIBABA_SINGAPORE_API_KEY", "alibaba-us": "ALIBABA_US_API_KEY",
	"groq": "GROQ_API_KEY", "avian": "AVIAN_API_KEY", "opencode": "OPENCODE_API_KEY",
	"azure": "AZURE_OPENAI_API_KEY", "moonshot": "MOONSHOT_API_KEY",
}

func crushAuthStatus(ctx context.Context, scope ports.AgentAuthCheck, d authutil.Dependencies) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	d.WorkingDir = scope.WorkingDir
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
	cfg := crushAuthConfig{Providers: map[string]crushAuthProvider{}, Models: map[string]crushAuthModel{}, Env: map[string]string{}}
	for _, path := range crushAuthPaths(scope, d) {
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
		if name := filepath.Base(path); name == "crushrc" || name == ".crushrc" {
			// crushrc is executable Bash. Only literal supported builtins can
			// be resolved locally; dynamic scripts must never be executed here.
			if !cfg.readLiteralRC(string(data)) {
				return ports.AgentAuthStatusUnknown, ctx.Err()
			}
		} else if !cfg.readJSON(data) {
			// A present config may change provider selection or credential
			// precedence. Do not expose lower-priority evidence if it is invalid.
			return ports.AgentAuthStatusUnknown, ctx.Err()
		}
	}
	// Crush applies top-level env before configuring providers, overwriting
	// inherited values. Resolve in its sorted-key order without mutating os.Env.
	resolved := make(map[string]string)
	keys := make([]string, 0, len(cfg.Env))
	for key := range cfg.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	effective := func(key string) string {
		if value, ok := resolved[key]; ok {
			return value
		}
		return env(key)
	}
	for _, key := range keys {
		resolved[key] = crushValue(cfg.Env[key], effective)
	}
	d.Getenv = func(key string) string {
		if value := effective("CRUSH_" + key); value != "" {
			return value
		}
		return effective(key)
	}
	selected := cfg.Models["large"].Provider
	if scope.Config.Model != "" {
		provider, model, ok := strings.Cut(scope.Config.Model, "/")
		if !ok || provider == "" || model == "" {
			return ports.AgentAuthStatusUnknown, nil
		}
		selected = provider
	}
	if selected != "" {
		return crushProviderEvidence(ctx, d, selected, cfg.Providers[selected], true).Status, ctx.Err()
	}
	ids := make(map[string]bool)
	for id := range cfg.Providers {
		ids[id] = true
	}
	for id := range crushProviderEnv {
		ids[id] = true
	}
	for _, id := range []string{"bedrock", "vertexai", "azure"} {
		ids[id] = true
	}
	evidence := make([]authutil.Evidence, 0, len(ids))
	for id := range ids {
		evidence = append(evidence, crushProviderEvidence(ctx, d, id, cfg.Providers[id], false))
	}
	return authutil.FirstDefinitive(evidence...).Status, ctx.Err()
}

func crushProviderEvidence(ctx context.Context, d authutil.Dependencies, id string, p crushAuthProvider, selected bool) authutil.Evidence {
	unknown := authutil.Evidence{Status: ports.AgentAuthStatusUnknown}
	configured := authutil.Evidence{Status: ports.AgentAuthStatusConfigured}
	if p.Disable {
		return unknown
	}
	endpoint, err := url.Parse(crushValue(p.BaseURL, d.Getenv))
	validEndpoint := err == nil && (endpoint.Scheme == "http" || endpoint.Scheme == "https") && endpoint.Hostname() != ""
	knownProvider := crushProviderEnv[id] != "" || id == "copilot" || id == "bedrock" || id == "vertexai" || p.Type == "bedrock" || p.Type == "google-vertex" || p.Type == "azure"
	if (p.BaseURL != "" && !validEndpoint) || (!knownProvider && !validEndpoint) {
		return unknown
	}
	if crushValue(p.APIKey, d.Getenv) != "" {
		return configured
	}
	if p.OAuth != nil && strings.TrimSpace(p.OAuth.AccessToken) != "" {
		if strings.TrimSpace(p.OAuth.RefreshToken) != "" {
			return configured
		}
		if p.OAuth.ExpiresAt > 0 {
			now := time.Now()
			if d.Now != nil {
				now = d.Now()
			}
			return authutil.ExpiryEvidence(time.Unix(p.OAuth.ExpiresAt, 0), false, now)
		}
	}
	if p.APIKey != "" {
		return unknown
	}
	if name := crushProviderEnv[id]; name != "" && strings.TrimSpace(d.Getenv(name)) != "" {
		return configured
	}
	switch {
	case id == "bedrock" || p.Type == "bedrock":
		return authutil.AWSEvidence(ctx, d)
	case id == "azure" || p.Type == "azure":
		return authutil.AzureEvidence(ctx, d)
	case id == "vertexai" || p.Type == "google-vertex":
		if strings.TrimSpace(d.Getenv("VERTEXAI_PROJECT")) != "" && strings.TrimSpace(d.Getenv("VERTEXAI_LOCATION")) != "" {
			return authutil.GoogleADCEvidence(ctx, d)
		}
		return unknown
	}
	// No API key declaration is different from an unresolved required key.
	if p.APIKey == "" && p.OAuth == nil && validEndpoint && (p.Type == "ollama" || (crushProviderEnv[id] == "" && p.Type == "openai-compat")) {
		return authutil.NoAuthEvidence(selected)
	}
	return unknown
}

func crushAuthPaths(scope ports.AgentAuthCheck, d authutil.Dependencies) []string {
	home := d.Getenv("HOME")
	if d.GOOS == "windows" {
		home = d.Getenv("USERPROFILE")
	}
	configDir := d.Getenv("CRUSH_GLOBAL_CONFIG")
	if configDir == "" {
		root := d.Getenv("XDG_CONFIG_HOME")
		if root == "" && home != "" {
			root = filepath.Join(home, ".config")
		}
		if root != "" {
			configDir = filepath.Join(root, "crush")
		}
	}
	dataDir := d.Getenv("CRUSH_GLOBAL_DATA")
	if dataDir == "" {
		root := d.Getenv("XDG_DATA_HOME")
		if root == "" && d.GOOS == "windows" {
			root = d.Getenv("LOCALAPPDATA")
			if root == "" && home != "" {
				root = filepath.Join(home, "AppData", "Local")
			}
		}
		if root == "" && home != "" {
			root = filepath.Join(home, ".local", "share")
		}
		if root != "" {
			dataDir = filepath.Join(root, "crush")
		}
	}
	var paths []string
	if configDir != "" {
		paths = append(paths, filepath.Join(configDir, "crush.json"), filepath.Join(configDir, "crushrc"))
	}
	if dataDir != "" {
		paths = append(paths, filepath.Join(dataDir, "crush.json"))
	}
	if filepath.IsAbs(scope.WorkingDir) {
		// Current Crush bounds discovery to the worktree root (or cwd when
		// no repository exists). Recognize both .git directories and files.
		var dirs []string
		for dir := filepath.Clean(scope.WorkingDir); ; dir = filepath.Dir(dir) {
			dirs = append(dirs, dir)
			if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
				break
			}
			if filepath.Dir(dir) == dir {
				dirs = dirs[:1]
				break
			}
		}
		for i := len(dirs) - 1; i >= 0; i-- {
			for _, name := range []string{"crush.json", ".crush.json", "crushrc", ".crushrc"} {
				paths = append(paths, filepath.Join(dirs[i], name))
			}
		}
		// AgentAuthCheck.DataDir belongs to AO. Crush's native workspace
		// configuration lives inside the workspace's .crush directory.
		paths = append(paths, filepath.Join(scope.WorkingDir, ".crush", "crush.json"))
	}
	return paths
}

func (c *crushAuthConfig) readJSON(data []byte) bool {
	var layer struct {
		Providers map[string]json.RawMessage
		Models    map[string]json.RawMessage
		Env       map[string]string
	}
	if !crushAuthObject(data, "providers", "models", "env") || json.Unmarshal(data, &layer) != nil {
		return false
	}
	for id, raw := range layer.Providers {
		var token struct{ OAuth json.RawMessage }
		if !crushAuthObject(raw, "type", "api_key", "base_url", "disable", "oauth") || json.Unmarshal(raw, &token) != nil {
			return false
		}
		if len(token.OAuth) > 0 && !crushAuthObject(token.OAuth, "access_token", "refresh_token", "expires_at") {
			return false
		}
		var p crushAuthProvider
		if json.Unmarshal(raw, &p) != nil {
			return false
		}
		p = c.Providers[id]
		if json.Unmarshal(raw, &p) == nil {
			c.Providers[id] = p
		}
	}
	for id, raw := range layer.Models {
		var m crushAuthModel
		if !crushAuthObject(raw, "provider", "model") || json.Unmarshal(raw, &m) != nil {
			return false
		}
		m = c.Models[id]
		if json.Unmarshal(raw, &m) == nil {
			c.Models[id] = m
		}
	}
	for key, value := range layer.Env {
		c.Env[key] = value
	}
	return true
}

// encoding/json leaves existing scalar and struct values unchanged for null.
// Reject explicit nulls at the named auth-schema fields before merging a layer.
// This checks only this object; unrelated settings are not recursively inspected.
func crushAuthObject(data []byte, nonnullFields ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return false
	}
	for key, value := range fields {
		if strings.TrimSpace(string(value)) != "null" {
			continue
		}
		for _, field := range nonnullFields {
			if strings.EqualFold(key, field) {
				return false
			}
		}
	}
	return true
}

func crushValue(value string, env func(string) string) string {
	if strings.Contains(value, "$(") || strings.ContainsAny(value, "`\n") {
		return ""
	}
	unresolved := false
	value = os.Expand(value, func(key string) string {
		for _, c := range key {
			if c != '_' && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
				unresolved = true
				return ""
			}
		}
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

func (c *crushAuthConfig) readLiteralRC(content string) bool {
	content = strings.ReplaceAll(content, "\\\n", "")
	for _, line := range strings.Split(content, "\n") {
		words, ok := crushLiteralWords(line)
		if !ok {
			return false
		}
		if len(words) == 0 {
			continue
		}
		switch {
		case len(words) == 3 && words[0] == "model" && (words[1] == "large" || words[1] == "small"):
			provider, model, ok := strings.Cut(words[2], "/")
			if !ok || provider == "" || model == "" {
				return false
			}
			c.Models[words[1]] = crushAuthModel{Provider: provider, Model: model}
		case len(words) >= 3 && words[0] == "provider" && words[1] == "add":
			if (len(words)-3)%2 != 0 {
				return false
			}
			p := c.Providers[words[2]]
			for i := 3; i < len(words); i += 2 {
				switch words[i] {
				case "--type":
					p.Type = words[i+1]
				case "--base-url":
					p.BaseURL = words[i+1]
				case "--api-key":
					p.APIKey = words[i+1]
				case "--disable":
					if words[i+1] != "true" && words[i+1] != "false" {
						return false
					}
					p.Disable = words[i+1] == "true"
				default:
					return false
				}
			}
			c.Providers[words[2]] = p
		case words[0] == "export" || (len(words) == 1 && strings.Contains(words[0], "=")):
			vars, err := authutil.ParseDotenv([]byte(line))
			if err != nil {
				return false
			}
			for key, value := range vars {
				c.Env[key] = value
			}
		default:
			return false
		}
	}
	return true
}

// This is a literal tokenizer, not a shell interpreter. Operators, expansion
// commands, escaping, and malformed quoting are deliberately unsupported.
func crushLiteralWords(line string) ([]string, bool) {
	var words []string
	var word strings.Builder
	var quote rune
	started := false
	for _, c := range line {
		if strings.ContainsRune("`\\;|&<>()", c) {
			return nil, false
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				word.WriteRune(c)
			}
			continue
		}
		if c == '#' && !started {
			break
		}
		if c == '\'' || c == '"' {
			quote = c
			started = true
			continue
		}
		if c == ' ' || c == '\t' || c == '\r' {
			if started {
				words = append(words, word.String())
				word.Reset()
				started = false
			}
			continue
		}
		started = true
		word.WriteRune(c)
	}
	if quote != 0 {
		return nil, false
	}
	if started {
		words = append(words, word.String())
	}
	return words, true
}
