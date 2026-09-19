package autohand

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentScopedAuthChecker = (*Plugin)(nil)

type autohandAuthConfig struct {
	Provider           string                              `json:"provider" toml:"provider" yaml:"provider"`
	Auth               autohandAuthSettings                `json:"auth" toml:"auth" yaml:"auth"`
	AutohandAI         autohandProviderSettings            `json:"autohandai" toml:"autohandai" yaml:"autohandai"`
	OpenRouter         autohandProviderSettings            `json:"openrouter" toml:"openrouter" yaml:"openrouter"`
	Anthropic          autohandProviderSettings            `json:"anthropic" toml:"anthropic" yaml:"anthropic"`
	Ollama             autohandProviderSettings            `json:"ollama" toml:"ollama" yaml:"ollama"`
	LlamaCpp           autohandProviderSettings            `json:"llamacpp" toml:"llamacpp" yaml:"llamacpp"`
	OpenAI             autohandProviderSettings            `json:"openai" toml:"openai" yaml:"openai"`
	MLX                autohandProviderSettings            `json:"mlx" toml:"mlx" yaml:"mlx"`
	LLMGateway         autohandProviderSettings            `json:"llmgateway" toml:"llmgateway" yaml:"llmgateway"`
	Azure              autohandProviderSettings            `json:"azure" toml:"azure" yaml:"azure"`
	ZAI                autohandProviderSettings            `json:"zai" toml:"zai" yaml:"zai"`
	Sakana             autohandProviderSettings            `json:"sakana" toml:"sakana" yaml:"sakana"`
	VertexAI           autohandProviderSettings            `json:"vertexai" toml:"vertexai" yaml:"vertexai"`
	XAI                autohandProviderSettings            `json:"xai" toml:"xai" yaml:"xai"`
	Cerebras           autohandProviderSettings            `json:"cerebras" toml:"cerebras" yaml:"cerebras"`
	NVIDIA             autohandProviderSettings            `json:"nvidia" toml:"nvidia" yaml:"nvidia"`
	DeepSeek           autohandProviderSettings            `json:"deepseek" toml:"deepseek" yaml:"deepseek"`
	Bedrock            autohandProviderSettings            `json:"bedrock" toml:"bedrock" yaml:"bedrock"`
	BlueprintLocal     autohandProviderSettings            `json:"blueprintLocal" toml:"blueprintLocal" yaml:"blueprintLocal"`
	CustomProviders    map[string]autohandProviderSettings `json:"customProviders" toml:"customProviders" yaml:"customProviders"`
	ExtensionProviders map[string]autohandProviderSettings `json:"extensionProviders" toml:"extensionProviders" yaml:"extensionProviders"`
}

type autohandAuthSettings struct {
	Token        string `json:"token" toml:"token" yaml:"token"`
	ExpiresAt    string `json:"expiresAt" toml:"expiresAt" yaml:"expiresAt"`
	APIKeyHelper string `json:"apiKeyHelper" toml:"apiKeyHelper" yaml:"apiKeyHelper"`
}

type autohandProviderSettings struct {
	ID             string `json:"id" toml:"id" yaml:"id"`
	APIKey         string `json:"apiKey" toml:"apiKey" yaml:"apiKey"`
	AuthToken      string `json:"authToken" toml:"authToken" yaml:"authToken"`
	Model          string `json:"model" toml:"model" yaml:"model"`
	Plan           string `json:"plan" toml:"plan" yaml:"plan"`
	AuthMode       string `json:"authMode" toml:"authMode" yaml:"authMode"`
	AuthMethod     string `json:"authMethod" toml:"authMethod" yaml:"authMethod"`
	BaseURL        string `json:"baseUrl" toml:"baseUrl" yaml:"baseUrl"`
	Endpoint       string `json:"endpoint" toml:"endpoint" yaml:"endpoint"`
	DeploymentName string `json:"deploymentName" toml:"deploymentName" yaml:"deploymentName"`
	ProjectID      string `json:"projectId" toml:"projectId" yaml:"projectId"`
	Region         string `json:"region" toml:"region" yaml:"region"`
	Profile        string `json:"profile" toml:"profile" yaml:"profile"`
	APIKeyRequired *bool  `json:"apiKeyRequired" toml:"apiKeyRequired" yaml:"apiKeyRequired"`
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
	return autohandAuthStatus(ctx, check, d)
}

func autohandAuthStatus(ctx context.Context, check ports.AgentAuthCheck, d authutil.Dependencies) (ports.AgentAuthStatus, error) {
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

	path := autohandAuthConfigPath(check, d)
	config := autohandAuthConfig{}
	if path != "" {
		loaded, valid := readAutohandAuthConfig(ctx, d, path)
		if err := ctx.Err(); err != nil {
			return ports.AgentAuthStatusUnknown, err
		}
		if !valid {
			return ports.AgentAuthStatusUnknown, nil
		}
		config = loaded
	}
	if autohandBareMode(check.Args, d.Getenv("AUTOHAND_CODE_SIMPLE")) {
		if usableSecret(d.Getenv("AUTOHAND_API_KEY")) || usableSecret(config.Auth.APIKeyHelper) {
			return ports.AgentAuthStatusConfigured, nil
		}
		return ports.AgentAuthStatusUnknown, nil
	}

	provider := strings.ToLower(strings.TrimSpace(d.Getenv("AUTOHAND_PROVIDER")))
	if provider == "vertex" {
		provider = "vertexai"
	}
	if provider == "" {
		provider = strings.ToLower(strings.TrimSpace(config.Provider))
	}
	if provider == "" {
		provider = "openrouter"
	}
	if !validAutohandProvider(provider) {
		return ports.AgentAuthStatusUnknown, nil
	}
	return autohandProviderStatus(ctx, d, config, provider), nil
}

func autohandProviderStatus(ctx context.Context, d authutil.Dependencies, config autohandAuthConfig, provider string) ports.AgentAuthStatus {
	if provider == "ollama" || provider == "llamacpp" || provider == "mlx" || provider == "blueprint-local" {
		return ports.AgentAuthStatusNotApplicable
	}
	settings, ok := config.autohandProvider(provider)
	if strings.HasPrefix(provider, "custom:") {
		if !ok {
			return ports.AgentAuthStatusUnknown
		}
		if settings.APIKeyRequired != nil && !*settings.APIKeyRequired {
			return ports.AgentAuthStatusNotApplicable
		}
		return configuredIfSecret(settings.APIKey)
	}
	if strings.HasPrefix(provider, "extension:") {
		if !ok {
			return ports.AgentAuthStatusUnknown
		}
		return configuredIfSecret(settings.APIKey)
	}
	if provider == "autohandai" {
		if strings.EqualFold(strings.TrimSpace(settings.Plan), "local") {
			return ports.AgentAuthStatusNotApplicable
		}
		switch strings.ToLower(strings.TrimSpace(settings.AuthMode)) {
		case "account":
			return autohandAccountStatus(config.Auth, d)
		case "api-key":
			if usableSecret(settings.APIKey) || usableSecret(d.Getenv("AUTOHAND_AI_API_KEY")) {
				return ports.AgentAuthStatusConfigured
			}
			return ports.AgentAuthStatusUnknown
		case "":
			return configuredIfSecret(d.Getenv("AUTOHAND_AI_API_KEY"))
		default:
			return ports.AgentAuthStatusUnknown
		}
	}
	if !ok {
		return ports.AgentAuthStatusUnknown
	}
	if provider == "bedrock" {
		if strings.TrimSpace(settings.Model) == "" {
			return ports.AgentAuthStatusUnknown
		}
		if strings.EqualFold(strings.TrimSpace(settings.AuthMode), "bedrock-api-key") {
			return configuredIfSecret(settings.APIKey)
		}
		if profile := strings.TrimSpace(settings.Profile); profile != "" {
			getenv := d.Getenv
			d.Getenv = func(name string) string {
				if name == "AWS_PROFILE" {
					return profile
				}
				return getenv(name)
			}
		}
		return authutil.AWSEvidence(ctx, d).Status
	}
	if provider == "vertexai" {
		if strings.TrimSpace(settings.ProjectID) == "" || strings.TrimSpace(settings.Model) == "" {
			return ports.AgentAuthStatusUnknown
		}
		if usableSecret(settings.AuthToken) {
			return ports.AgentAuthStatusConfigured
		}
		return authutil.GoogleADCEvidence(ctx, d).Status
	}
	if provider == "azure" {
		endpoint := firstNonempty(settings.Endpoint, settings.BaseURL, d.Getenv("AZURE_OPENAI_ENDPOINT"))
		deployment := firstNonempty(settings.DeploymentName, d.Getenv("AZURE_OPENAI_DEPLOYMENT"))
		if endpoint == "" || deployment == "" {
			return ports.AgentAuthStatusUnknown
		}
		if usableSecret(settings.APIKey) || usableSecret(d.Getenv("AZURE_OPENAI_KEY")) {
			return ports.AgentAuthStatusConfigured
		}
		return authutil.AzureEvidence(ctx, d).Status
	}
	if usableSecret(settings.APIKey) {
		return ports.AgentAuthStatusConfigured
	}
	if envName := autohandProviderEnv[provider]; envName != "" && usableSecret(d.Getenv(envName)) {
		return ports.AgentAuthStatusConfigured
	}
	return ports.AgentAuthStatusUnknown
}

func (config autohandAuthConfig) autohandProvider(provider string) (autohandProviderSettings, bool) {
	if strings.HasPrefix(provider, "custom:") {
		entry, ok := config.CustomProviders[strings.TrimPrefix(provider, "custom:")]
		return entry, ok
	}
	if strings.HasPrefix(provider, "extension:") {
		entry, ok := config.ExtensionProviders[provider]
		return entry, ok
	}
	entries := map[string]autohandProviderSettings{
		"autohandai": config.AutohandAI, "openrouter": config.OpenRouter, "anthropic": config.Anthropic,
		"ollama": config.Ollama, "llamacpp": config.LlamaCpp, "openai": config.OpenAI, "mlx": config.MLX,
		"llmgateway": config.LLMGateway, "azure": config.Azure, "zai": config.ZAI, "sakana": config.Sakana,
		"vertexai": config.VertexAI, "xai": config.XAI, "cerebras": config.Cerebras, "nvidia": config.NVIDIA,
		"deepseek": config.DeepSeek, "bedrock": config.Bedrock, "blueprint-local": config.BlueprintLocal,
	}
	entry, ok := entries[provider]
	return entry, ok
}

var autohandProviderEnv = map[string]string{
	"autohandai": "AUTOHAND_AI_API_KEY", "openrouter": "OPENROUTER_API_KEY", "openai": "OPENAI_API_KEY",
	"llmgateway": "LLM_GATEWAY_API_KEY", "azure": "AZURE_OPENAI_KEY", "zai": "ZAI_API_KEY",
	"sakana": "SAKANA_API_KEY", "deepseek": "DEEPSEEK_API_KEY",
}

func validAutohandProvider(provider string) bool {
	if strings.HasPrefix(provider, "custom:") || strings.HasPrefix(provider, "extension:") {
		return len(strings.SplitN(provider, ":", 2)[1]) > 0
	}
	for _, candidate := range []string{
		"autohandai", "openrouter", "anthropic", "ollama", "llamacpp", "openai", "mlx", "llmgateway",
		"azure", "zai", "sakana", "vertexai", "xai", "cerebras", "nvidia", "deepseek", "bedrock", "blueprint-local",
	} {
		if provider == candidate {
			return true
		}
	}
	return false
}

func autohandAccountStatus(auth autohandAuthSettings, d authutil.Dependencies) ports.AgentAuthStatus {
	if !usableSecret(auth.Token) {
		return ports.AgentAuthStatusUnknown
	}
	if strings.TrimSpace(auth.ExpiresAt) == "" {
		return ports.AgentAuthStatusConfigured
	}
	expires, ok := authutil.ParseExpiry(auth.ExpiresAt)
	if !ok {
		return ports.AgentAuthStatusUnknown
	}
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return authutil.ExpiryEvidence(expires, false, now()).Status
}

func configuredIfSecret(value string) ports.AgentAuthStatus {
	if usableSecret(value) {
		return ports.AgentAuthStatusConfigured
	}
	return ports.AgentAuthStatusUnknown
}

func autohandAuthConfigPath(check ports.AgentAuthCheck, d authutil.Dependencies) string {
	if path := autohandConfigArg(check.Args); path != "" {
		return resolveAutohandPath(path, check.WorkingDir, d.Getenv("HOME"))
	}
	if path := strings.TrimSpace(d.Getenv("AUTOHAND_CONFIG")); path != "" {
		return resolveAutohandPath(path, check.WorkingDir, d.Getenv("HOME"))
	}
	home := strings.TrimSpace(d.Getenv("AUTOHAND_HOME"))
	if home == "" {
		if userHome := strings.TrimSpace(d.Getenv("HOME")); userHome != "" {
			home = filepath.Join(userHome, ".autohand")
		}
	}
	for _, name := range []string{"config.toml", "config.yaml", "config.yml", "config.json"} {
		path := filepath.Join(home, name)
		if autohandFileExists(d, path) {
			return path
		}
	}
	return ""
}

func autohandConfigArg(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--config" && i+1 < len(args) {
			return strings.TrimSpace(args[i+1])
		}
		if value, ok := strings.CutPrefix(args[i], "--config="); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func resolveAutohandPath(path, workingDir, home string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if filepath.IsAbs(workingDir) {
		return filepath.Join(workingDir, path)
	}
	if home != "" {
		return filepath.Join(home, path)
	}
	return ""
}

func readAutohandAuthConfig(ctx context.Context, d authutil.Dependencies, path string) (autohandAuthConfig, bool) {
	if !autohandFileExists(d, path) {
		return autohandAuthConfig{}, false
	}
	var config autohandAuthConfig
	var err error
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		err = authutil.ReadTOML(ctx, d, path, &config)
	case ".yaml", ".yml":
		err = authutil.ReadYAML(ctx, d, path, &config)
	case ".json":
		err = authutil.ReadJSON(ctx, d, path, &config)
	default:
		return autohandAuthConfig{}, false
	}
	if err != nil {
		return autohandAuthConfig{}, false
	}
	return config, true
}

func autohandFileExists(d authutil.Dependencies, path string) bool {
	if path == "" {
		return false
	}
	lstat := d.Lstat
	if lstat == nil {
		lstat = os.Lstat
	}
	info, err := lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func autohandBareMode(args []string, env string) bool {
	for _, arg := range args {
		if arg == "--bare" {
			return true
		}
	}
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// autohandConfigAuthStatus is retained for compatibility with legacy direct tests.
func autohandConfigAuthStatus(configPath string) (ports.AgentAuthStatus, error) {
	data, err := os.ReadFile(configPath) //nolint:gosec // path is the user's own Autohand config
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ports.AgentAuthStatusUnknown, nil
		}
		return ports.AgentAuthStatusUnknown, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(data, &config); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	ready, known := autohandCloudAuthReady(config)
	if ready {
		return ports.AgentAuthStatusAuthorized, nil
	}
	if known {
		return ports.AgentAuthStatusUnknown, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

func autohandCloudAuthReady(config map[string]json.RawMessage) (ready, known bool) {
	authRaw, ok := config["auth"]
	if !ok {
		return false, false
	}
	var auth struct {
		Token        string `json:"token"`
		APIKeyHelper string `json:"apiKeyHelper"`
	}
	if json.Unmarshal(authRaw, &auth) != nil {
		return false, false
	}
	return usableSecret(auth.Token), true
}

func usableSecret(value string) bool {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return false
	}
	switch strings.ToLower(normalized) {
	case "api key", "apikey", "your api key", "your-api-key", "your_api_key", "token", "your token", "your-token", "your_token", "changeme", "change-me", "replace-me", "replace_me":
		return false
	default:
		return true
	}
}
