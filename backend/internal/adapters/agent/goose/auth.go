package goose

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"gopkg.in/yaml.v3"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentScopedAuthChecker = (*Plugin)(nil)

// AuthStatus resolves device-wide credentials without assuming a workspace.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.AuthStatusFor(ctx, ports.AgentAuthCheck{})
}

// AuthStatusFor resolves only the effective Goose provider's credentials.
func (p *Plugin) AuthStatusFor(ctx context.Context, check ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	if _, err := p.gooseBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return gooseAuthStatus(ctx, check, authutil.Dependencies{})
}

func gooseLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	status, err := gooseAuthStatus(ctx, ports.AgentAuthCheck{}, authutil.Dependencies{})
	return status, status != ports.AgentAuthStatusUnknown, err
}

// Goose's declarative provider metadata declares exact secret names. The
// catalog is not a list of interchangeable credentials: only the selected
// provider's entry is consulted.
var gooseProviderKeys = map[string]string{
	"openai": "OPENAI_API_KEY", "anthropic": "ANTHROPIC_API_KEY", "google": "GOOGLE_API_KEY", "openrouter": "OPENROUTER_API_KEY",
	"aimlapi": "AIMLAPI_API_KEY", "alibaba": "DASHSCOPE_API_KEY", "celeris": "CELERIS_API_KEY", "cerebras": "CEREBRAS_API_KEY",
	"custom_deepseek": "DEEPSEEK_API_KEY", "empiriolabs": "EMPIRIOLABS_API_KEY", "eurouter": "EUROUTER_API_KEY", "fireworks-ai": "FIREWORKS_API_KEY",
	"friendli": "FRIENDLI_API_KEY", "futurmix": "FUTURMIX_API_KEY", "groq": "GROQ_API_KEY", "iflytek": "SPARK_API_PASSWORD",
	"iflytek_astron": "ASTRON_API_KEY", "inception": "INCEPTION_API_KEY", "meta": "META_MODEL_API_KEY", "minimax": "MINIMAX_API_KEY",
	"mistral": "MISTRAL_API_KEY", "moonshot": "MOONSHOT_API_KEY", "nearai": "NEARAI_API_KEY", "novita": "NOVITA_API_KEY",
	"nvidia": "NVIDIA_API_KEY", "ollama_cloud": "OLLAMA_CLOUD_API_KEY", "opencode_go": "OPENCODE_API_KEY", "opencode_zen": "OPENCODE_API_KEY",
	"opper": "OPPER_API_KEY", "orcarouter": "ORCAROUTER_API_KEY", "ovhcloud": "OVHCLOUD_API_KEY", "perplexity": "PERPLEXITY_API_KEY",
	"pleumrouter": "PLEUMROUTER_API_KEY", "routstr": "ROUTSTR_API_KEY", "sakana": "SAKANA_API_KEY", "saladcloud": "SALAD_CLOUD_API_KEY",
	"saygm": "SAYGM_API_KEY", "scaleway": "SCW_SECRET_KEY", "tanzu_ai": "TANZU_AI_API_KEY", "custom_tensorix": "TENSORIX_API_KEY",
	"together": "TOGETHER_API_KEY", "trustedrouter": "TRUSTEDROUTER_API_KEY", "venice": "VENICE_API_KEY", "vercel_ai_gateway": "AI_GATEWAY_API_KEY",
	"zai": "ZHIPU_API_KEY", "zhipu": "ZHIPU_API_KEY",
}

type gooseProviderMetadata struct {
	Name         string `json:"name"`
	Engine       string `json:"engine"`
	BaseURL      string `json:"base_url"`
	APIKeyEnv    string `json:"api_key_env"`
	RequiresAuth *bool  `json:"requires_auth"`
	Auth         *struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	} `json:"auth"`
	EnvVars []struct {
		Name     string `json:"name"`
		Required bool   `json:"required"`
		Secret   bool   `json:"secret"`
		Default  string `json:"default"`
	} `json:"env_vars"`
}

var gooseProviderID = regexp.MustCompile(`^[a-z0-9_][a-z0-9_-]*$`)

func gooseAuthStatus(ctx context.Context, check ports.AgentAuthCheck, d authutil.Dependencies) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	getenv := d.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	d.Getenv = func(key string) string {
		if value, ok := check.Env[key]; ok {
			return strings.TrimSpace(value)
		}
		return strings.TrimSpace(getenv(key))
	}
	dir := gooseConfigDir(d)
	config := map[string]yaml.Node{}
	if dir != "" {
		_ = authutil.ReadYAML(ctx, d, filepath.Join(dir, "config.yaml"), &config)
	}
	param := func(key string) string {
		if value, ok := check.Env[key]; ok {
			return strings.TrimSpace(value)
		}
		if value := d.Getenv(key); value != "" {
			return value
		}
		node := config[key]
		if node.Kind == yaml.ScalarNode && node.Tag == "!!str" {
			return strings.TrimSpace(node.Value)
		}
		return ""
	}
	provider := param("GOOSE_PROVIDER")
	for i := 0; i < len(check.Args); i++ {
		arg := check.Args[i]
		if arg == "--" {
			break
		}
		// Skip values carried by the other native launch options. Prompt and
		// system text are not options even when they begin with --provider.
		switch arg {
		case "--text", "-t", "--system", "--instructions", "-i", "--recipe", "--model", "--session-id":
			if i+1 < len(check.Args) {
				i++
			}
			continue
		}
		if strings.HasPrefix(arg, "--provider=") {
			provider = strings.TrimPrefix(arg, "--provider=")
		}
		if arg == "--provider" && i+1 < len(check.Args) {
			i++
			provider = check.Args[i]
		}
	}
	if !gooseProviderID.MatchString(provider) {
		return ports.AgentAuthStatusUnknown, nil
	}

	// Read secrets lazily and at most once. config.yaml is deliberately excluded.
	loaded := false
	stored := map[string]yaml.Node{}
	secret := func(key string) string {
		if key == "" {
			return ""
		}
		if value, ok := check.Env[key]; ok {
			return strings.TrimSpace(value)
		}
		if value := d.Getenv(key); value != "" {
			return value
		}
		if !loaded {
			loaded = true
			disabled := d.Getenv("GOOSE_DISABLE_KEYRING") != ""
			if node, ok := config["GOOSE_DISABLE_KEYRING"]; ok {
				disabled = disabled || node.Value == "true" || node.Value == "1"
			}
			if !disabled {
				if raw, err := authutil.GenericPassword(ctx, d, "goose", "secrets"); err == nil {
					// The keyring stores JSON; the file fallback stores YAML.
					var values map[string]json.RawMessage
					if json.Unmarshal(raw, &values) == nil {
						for key, rawValue := range values {
							var value string
							if json.Unmarshal(rawValue, &value) == nil {
								stored[key] = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
							}
						}
					}
				}
			}
			if len(stored) == 0 && dir != "" {
				_ = authutil.ReadYAML(ctx, d, filepath.Join(dir, "secrets.yaml"), &stored)
			}
		}
		node := stored[key]
		if node.Kind == yaml.ScalarNode && node.Tag == "!!str" {
			return strings.TrimSpace(node.Value)
		}
		return ""
	}

	// A selected custom provider declares its own auth mechanism. Its identity
	// must match the filename, and the id cannot traverse out of custom_providers.
	var metadata gooseProviderMetadata
	if dir != "" && authutil.ReadJSON(ctx, d, filepath.Join(dir, "custom_providers", provider+".json"), &metadata) == nil {
		endpoint, err := url.Parse(metadata.BaseURL)
		if metadata.Name != provider || err != nil || endpoint.Hostname() == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.User != nil {
			return ports.AgentAuthStatusUnknown, nil
		}
		switch metadata.Engine {
		case "openai", "openai_compatible", "anthropic", "anthropic_compatible", "ollama", "ollama_compatible":
		default:
			return ports.AgentAuthStatusUnknown, nil
		}
		if metadata.Auth != nil && strings.TrimSpace(metadata.APIKeyEnv) != "" {
			return ports.AgentAuthStatusUnknown, nil
		}
		for _, field := range metadata.EnvVars {
			if !field.Required {
				continue
			}
			value := param(field.Name)
			if field.Secret {
				value = secret(field.Name)
			}
			if value == "" && strings.TrimSpace(field.Default) == "" {
				return ports.AgentAuthStatusUnknown, nil
			}
		}
		if metadata.RequiresAuth != nil && !*metadata.RequiresAuth {
			return ports.AgentAuthStatusNotApplicable, nil
		}
		if metadata.Auth != nil {
			if strings.TrimSpace(metadata.Auth.Command) != "" {
				return ports.AgentAuthStatusConfigured, nil
			}
			return ports.AgentAuthStatusUnknown, nil
		}
		if secret(metadata.APIKeyEnv) != "" {
			return ports.AgentAuthStatusConfigured, nil
		}
		return ports.AgentAuthStatusUnknown, ctx.Err()
	}
	switch provider {
	case "ollama", "lmstudio", "atomic_chat", "llama_swap", "lynkr", "omlx":
		return ports.AgentAuthStatusNotApplicable, nil
	case "bedrock":
		return authutil.AWSEvidence(ctx, d).Status, ctx.Err()
	case "gcpvertexai":
		if param("GCP_PROJECT_ID") != "" {
			return authutil.GoogleADCEvidence(ctx, d).Status, ctx.Err()
		}
		return ports.AgentAuthStatusUnknown, nil
	case "azure":
		if param("AZURE_OPENAI_ENDPOINT") == "" || param("AZURE_OPENAI_DEPLOYMENT_NAME") == "" {
			return ports.AgentAuthStatusUnknown, nil
		}
		if secret("AZURE_OPENAI_API_KEY") != "" || secret("AZURE_OPENAI_AD_TOKEN") != "" {
			return ports.AgentAuthStatusConfigured, nil
		}
		return authutil.AzureEvidence(ctx, d).Status, ctx.Err()
	case "databricks", "databricks_v2":
		host := param("DATABRICKS_HOST")
		if host == "" {
			host = secret("DATABRICKS_HOST")
		}
		endpoint, err := url.Parse(host)
		if err != nil || endpoint.Hostname() == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.User != nil {
			return ports.AgentAuthStatusUnknown, nil
		}
		if secret("DATABRICKS_TOKEN") != "" {
			return ports.AgentAuthStatusConfigured, nil
		}
		if dir != "" {
			// Goose hashes the exact host, client id and comma-joined scopes;
			// never scan unrelated workspace caches for an arbitrary token.
			hash := sha256.Sum256([]byte(host + "databricks-cli" + "all-apis,offline_access"))
			var token gooseOAuthToken
			path := filepath.Join(dir, "databricks", "oauth", hex.EncodeToString(hash[:])+".json")
			if authutil.ReadJSON(ctx, d, path, &token) == nil && strings.TrimSpace(token.Access) != "" && strings.TrimSpace(token.Refresh) != "" {
				if token.Expires == "" {
					return ports.AgentAuthStatusConfigured, nil
				}
				if _, err := time.Parse(time.RFC3339, token.Expires); err == nil {
					return ports.AgentAuthStatusConfigured, nil
				}
			}
		}
		return ports.AgentAuthStatusUnknown, ctx.Err()
	case "github_copilot":
		if secret("GITHUB_COPILOT_TOKEN") != "" {
			return ports.AgentAuthStatusConfigured, nil
		}
		return gooseOAuthStatus(ctx, d, dir, provider, param("GITHUB_COPILOT_HOST")), ctx.Err()
	case "gemini_oauth", "kimi_code", "xai_oauth", "chatgpt_codex":
		return gooseOAuthStatus(ctx, d, dir, provider, ""), ctx.Err()
	}
	if provider == "tanzu_ai" && param("TANZU_AI_ENDPOINT") == "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	if secret(gooseProviderKeys[provider]) != "" {
		return ports.AgentAuthStatusConfigured, nil
	}
	return ports.AgentAuthStatusUnknown, ctx.Err()
}

func gooseConfigDir(d authutil.Dependencies) string {
	if root := d.Getenv("GOOSE_PATH_ROOT"); filepath.IsAbs(root) {
		return filepath.Join(root, "config")
	}
	goos := d.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos == "windows" {
		root := d.Getenv("APPDATA")
		if !filepath.IsAbs(root) {
			home := d.Getenv("USERPROFILE")
			if !filepath.IsAbs(home) {
				return ""
			}
			root = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(root, "Block", "goose", "config")
	}
	if root := d.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(root) {
		return filepath.Join(root, "goose")
	}
	if home := d.Getenv("HOME"); filepath.IsAbs(home) {
		return filepath.Join(home, ".config", "goose")
	}
	return ""
}

type gooseOAuthToken struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	Expires string `json:"expires_at"`
}

func gooseOAuthStatus(ctx context.Context, d authutil.Dependencies, dir, provider, host string) ports.AgentAuthStatus {
	if dir == "" {
		return ports.AgentAuthStatusUnknown
	}
	now := time.Now()
	if d.Now != nil {
		now = d.Now()
	}
	var token gooseOAuthToken
	switch provider {
	case "gemini_oauth":
		var setup struct {
			ProjectID string          `json:"project_id"`
			Token     gooseOAuthToken `json:"token"`
		}
		if authutil.ReadJSON(ctx, d, filepath.Join(dir, "gemini_oauth", "tokens.json"), &setup) != nil || strings.TrimSpace(setup.ProjectID) == "" {
			return ports.AgentAuthStatusUnknown
		}
		token = setup.Token
	case "github_copilot":
		host = strings.TrimPrefix(strings.TrimRight(host, "/"), "https://")
		path := filepath.Join(dir, "githubcopilot", "info.json")
		if host != "" && host != "github.com" {
			safeHost := strings.NewReplacer("/", "_", ":", "_", ".", "_", "\\", "_").Replace(host)
			path = filepath.Join(dir, "githubcopilot", safeHost, "info.json")
		}
		var cache struct {
			Expires string `json:"expires_at"`
			Info    struct {
				Token   string `json:"token"`
				Expires int64  `json:"expires_at"`
			} `json:"info"`
		}
		if authutil.ReadJSON(ctx, d, path, &cache) != nil || strings.TrimSpace(cache.Info.Token) == "" {
			return ports.AgentAuthStatusUnknown
		}
		expiry, err := time.Parse(time.RFC3339, cache.Expires)
		if err != nil || cache.Info.Expires <= 0 {
			return ports.AgentAuthStatusUnknown
		}
		// Copilot refreshes this short-lived cache through its GitHub login chain;
		// stale cache alone cannot prove that upstream login was rejected.
		if !expiry.After(now) || cache.Info.Expires <= now.Unix() {
			return ports.AgentAuthStatusUnknown
		}
		return ports.AgentAuthStatusConfigured
	default:
		path := filepath.Join(dir, "xai_oauth", "tokens.json")
		if provider == "kimi_code" {
			path = filepath.Join(dir, "kimicode", "token.json")
		} else if provider == "chatgpt_codex" {
			path = filepath.Join(dir, "chatgpt_codex", "tokens.json")
		}
		if authutil.ReadJSON(ctx, d, path, &token) != nil {
			return ports.AgentAuthStatusUnknown
		}
	}
	if strings.TrimSpace(token.Access) == "" {
		return ports.AgentAuthStatusUnknown
	}
	expiry, err := time.Parse(time.RFC3339, token.Expires)
	if err != nil {
		return ports.AgentAuthStatusUnknown
	}
	return authutil.ExpiryEvidence(expiry, strings.TrimSpace(token.Refresh) != "", now).Status
}
