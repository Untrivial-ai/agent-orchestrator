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
	Profiles           map[string]autohandAuthConfig       `json:"profiles" toml:"profiles" yaml:"profiles"`
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
	ID             string                `json:"id" toml:"id" yaml:"id"`
	DisplayName    string                `json:"displayName" toml:"displayName" yaml:"displayName"`
	APIFormat      string                `json:"apiFormat" toml:"apiFormat" yaml:"apiFormat"`
	APIKey         string                `json:"apiKey" toml:"apiKey" yaml:"apiKey"`
	AuthToken      string                `json:"authToken" toml:"authToken" yaml:"authToken"`
	Model          string                `json:"model" toml:"model" yaml:"model"`
	Plan           string                `json:"plan" toml:"plan" yaml:"plan"`
	AuthMode       string                `json:"authMode" toml:"authMode" yaml:"authMode"`
	AuthMethod     string                `json:"authMethod" toml:"authMethod" yaml:"authMethod"`
	BaseURL        string                `json:"baseUrl" toml:"baseUrl" yaml:"baseUrl"`
	Endpoint       string                `json:"endpoint" toml:"endpoint" yaml:"endpoint"`
	ResourceName   string                `json:"resourceName" toml:"resourceName" yaml:"resourceName"`
	DeploymentName string                `json:"deploymentName" toml:"deploymentName" yaml:"deploymentName"`
	TenantID       string                `json:"tenantId" toml:"tenantId" yaml:"tenantId"`
	ClientID       string                `json:"clientId" toml:"clientId" yaml:"clientId"`
	ClientSecret   string                `json:"clientSecret" toml:"clientSecret" yaml:"clientSecret"`
	ProjectID      string                `json:"projectId" toml:"projectId" yaml:"projectId"`
	Region         string                `json:"region" toml:"region" yaml:"region"`
	Profile        string                `json:"profile" toml:"profile" yaml:"profile"`
	APIMode        string                `json:"apiMode" toml:"apiMode" yaml:"apiMode"`
	ModelPath      string                `json:"modelPath" toml:"modelPath" yaml:"modelPath"`
	ModelSHA256    string                `json:"modelSha256" toml:"modelSha256" yaml:"modelSha256"`
	ChatGPTAuth    autohandOAuthSettings `json:"chatgptAuth" toml:"chatgptAuth" yaml:"chatgptAuth"`
	OAuthAuth      autohandOAuthSettings `json:"oauthAuth" toml:"oauthAuth" yaml:"oauthAuth"`
	APIKeyRequired *bool                 `json:"apiKeyRequired" toml:"apiKeyRequired" yaml:"apiKeyRequired"`
}

type autohandOAuthSettings struct {
	AccessToken string `json:"accessToken" toml:"accessToken" yaml:"accessToken"`
	AccountID   string `json:"accountId" toml:"accountId" yaml:"accountId"`
}

type autohandWorkspaceSettings struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// AuthStatus reports the effective device-wide Autohand credential state.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.AuthStatusFor(ctx, ports.AgentAuthCheck{})
}

// AuthStatusFor reports Autohand credentials for one effective invocation.
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
	config := defaultAutohandAuthConfig()
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
	if filepath.IsAbs(check.WorkingDir) {
		localPath := filepath.Join(check.WorkingDir, ".autohand", "settings.local.json")
		if autohandFileExists(d, localPath) {
			var local autohandWorkspaceSettings
			if authutil.ReadJSON(ctx, d, localPath, &local) != nil {
				return ports.AgentAuthStatusUnknown, ctx.Err()
			}
			applyAutohandWorkspaceSettings(&config, local)
		}
	}
	applyAutohandEnvironment(&config, d)
	if provider := normalizedAutohandProvider(d.Getenv("AUTOHAND_PROVIDER")); provider != "" {
		config.Provider = provider
	}
	if !applyAutohandRunOverrides(&config, check.Args) {
		return ports.AgentAuthStatusUnknown, nil
	}
	if autohandBareMode(check.Args, d.Getenv("AUTOHAND_CODE_SIMPLE")) {
		if usableSecret(d.Getenv("AUTOHAND_API_KEY")) || usableSecret(config.Auth.APIKeyHelper) {
			return ports.AgentAuthStatusConfigured, nil
		}
		return ports.AgentAuthStatusUnknown, nil
	}

	provider := normalizedAutohandProvider(config.Provider)
	if provider == "" {
		provider = "openrouter"
	}
	if !validAutohandProvider(provider) {
		return ports.AgentAuthStatusUnknown, nil
	}
	return autohandProviderStatus(ctx, d, config, provider), nil
}

func defaultAutohandAuthConfig() autohandAuthConfig {
	return autohandAuthConfig{
		Provider: "openrouter",
		OpenRouter: autohandProviderSettings{
			Model:   "openrouter/auto",
			BaseURL: "https://openrouter.ai/api/v1",
		},
	}
}

func normalizedAutohandProvider(value string) string {
	provider := strings.ToLower(strings.TrimSpace(value))
	if provider == "vertex" {
		return "vertexai"
	}
	return provider
}

func applyAutohandEnvironment(config *autohandAuthConfig, d authutil.Dependencies) {
	aiKey := strings.TrimSpace(d.Getenv("AUTOHAND_AI_API_KEY"))
	aiBaseURL := strings.TrimSpace(d.Getenv("AUTOHAND_AI_BASE_URL"))
	aiPlan := strings.TrimSpace(d.Getenv("AUTOHAND_AI_PLAN"))
	if aiKey != "" || aiBaseURL != "" || aiPlan != "" {
		settings := config.AutohandAI
		if settings.Model == "" {
			settings.Model = firstNonempty(d.Getenv("AUTOHAND_MODEL"), "fantail")
		}
		if strings.EqualFold(aiPlan, "local") {
			settings.Plan = "local"
		} else {
			settings.Plan = "cloud"
		}
		if aiKey != "" {
			settings.APIKey = aiKey
			settings.AuthMode = "api-key"
		}
		if aiBaseURL != "" {
			settings.BaseURL = aiBaseURL
		}
		config.AutohandAI = settings
	}

	azureKey := strings.TrimSpace(d.Getenv("AZURE_OPENAI_KEY"))
	azureBaseURL := strings.TrimSpace(d.Getenv("AZURE_OPENAI_ENDPOINT"))
	azureDeployment := strings.TrimSpace(d.Getenv("AZURE_OPENAI_DEPLOYMENT"))
	if azureKey == "" && azureBaseURL == "" && azureDeployment == "" {
		return
	}
	settings := config.Azure
	if settings.Model == "" {
		settings.Model = firstNonempty(azureDeployment, "gpt-4o")
	}
	if azureKey != "" {
		settings.APIKey = azureKey
	}
	if azureBaseURL != "" {
		settings.BaseURL = azureBaseURL
	}
	if azureDeployment != "" {
		settings.DeploymentName = azureDeployment
	}
	settings.TenantID = firstNonempty(d.Getenv("AZURE_TENANT_ID"), settings.TenantID)
	settings.ClientID = firstNonempty(d.Getenv("AZURE_CLIENT_ID"), settings.ClientID)
	settings.ClientSecret = firstNonempty(d.Getenv("AZURE_CLIENT_SECRET"), settings.ClientSecret)
	config.Azure = settings
}

func applyAutohandWorkspaceSettings(config *autohandAuthConfig, local autohandWorkspaceSettings) {
	if provider := normalizedAutohandProvider(local.Provider); provider != "" {
		config.Provider = provider
	}
	if model := strings.TrimSpace(local.Model); model != "" {
		provider := normalizedAutohandProvider(config.Provider)
		if settings, ok := config.autohandProvider(provider); ok {
			settings.Model = model
			config.setAutohandProvider(provider, settings)
		}
	}
}

func applyAutohandRunOverrides(config *autohandAuthConfig, args []string) bool {
	profile, sets, provider, valid := autohandRunArgs(args)
	if !valid {
		return false
	}
	if profile != "" {
		overlay, ok := config.Profiles[profile]
		if !ok {
			return false
		}
		mergeAutohandConfig(config, overlay)
	}
	for _, entry := range sets {
		if !applyAutohandSet(config, entry) {
			return false
		}
	}
	if provider != "" {
		provider = normalizedAutohandProvider(provider)
		if !validAutohandProvider(provider) {
			return false
		}
		config.Provider = provider
		if provider == "autohandai" && config.AutohandAI == (autohandProviderSettings{}) {
			config.AutohandAI = autohandProviderSettings{Model: "fantail", Plan: "cloud", AuthMode: "account"}
		}
	}
	return true
}

func autohandRunArgs(args []string) (profile string, sets []string, provider string, valid bool) {
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		name, value, inline := strings.Cut(args[i], "=")
		if name != "--profile" && name != "--set" && name != "--provider" {
			continue
		}
		if !inline {
			if i+1 >= len(args) {
				return "", nil, "", false
			}
			i++
			value = args[i]
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return "", nil, "", false
		}
		switch name {
		case "--profile":
			profile = value
		case "--set":
			sets = append(sets, value)
		case "--provider":
			provider = value
		}
	}
	return profile, sets, provider, true
}

func mergeAutohandConfig(base *autohandAuthConfig, overlay autohandAuthConfig) {
	if overlay.Provider != "" {
		base.Provider = overlay.Provider
	}
	for _, provider := range []string{
		"autohandai", "openrouter", "anthropic", "ollama", "llamacpp", "openai", "mlx", "llmgateway",
		"azure", "zai", "sakana", "vertexai", "xai", "cerebras", "nvidia", "deepseek", "bedrock", "blueprint-local",
	} {
		settings, ok := overlay.autohandProvider(provider)
		if !ok {
			continue
		}
		current, _ := base.autohandProvider(provider)
		base.setAutohandProvider(provider, mergeAutohandProviderSettings(current, settings))
	}
	for name, settings := range overlay.CustomProviders {
		current := base.CustomProviders[name]
		if base.CustomProviders == nil {
			base.CustomProviders = make(map[string]autohandProviderSettings)
		}
		base.CustomProviders[name] = mergeAutohandProviderSettings(current, settings)
	}
	for name, settings := range overlay.ExtensionProviders {
		current := base.ExtensionProviders[name]
		if base.ExtensionProviders == nil {
			base.ExtensionProviders = make(map[string]autohandProviderSettings)
		}
		base.ExtensionProviders[name] = mergeAutohandProviderSettings(current, settings)
	}
}

func mergeAutohandProviderSettings(base, overlay autohandProviderSettings) autohandProviderSettings {
	if overlay.ID != "" {
		base.ID = overlay.ID
	}
	if overlay.DisplayName != "" {
		base.DisplayName = overlay.DisplayName
	}
	if overlay.APIFormat != "" {
		base.APIFormat = overlay.APIFormat
	}
	if overlay.APIKey != "" {
		base.APIKey = overlay.APIKey
	}
	if overlay.AuthToken != "" {
		base.AuthToken = overlay.AuthToken
	}
	if overlay.Model != "" {
		base.Model = overlay.Model
	}
	if overlay.Plan != "" {
		base.Plan = overlay.Plan
	}
	if overlay.AuthMode != "" {
		base.AuthMode = overlay.AuthMode
	}
	if overlay.AuthMethod != "" {
		base.AuthMethod = overlay.AuthMethod
	}
	if overlay.BaseURL != "" {
		base.BaseURL = overlay.BaseURL
	}
	if overlay.Endpoint != "" {
		base.Endpoint = overlay.Endpoint
	}
	if overlay.ResourceName != "" {
		base.ResourceName = overlay.ResourceName
	}
	if overlay.DeploymentName != "" {
		base.DeploymentName = overlay.DeploymentName
	}
	if overlay.TenantID != "" {
		base.TenantID = overlay.TenantID
	}
	if overlay.ClientID != "" {
		base.ClientID = overlay.ClientID
	}
	if overlay.ClientSecret != "" {
		base.ClientSecret = overlay.ClientSecret
	}
	if overlay.ProjectID != "" {
		base.ProjectID = overlay.ProjectID
	}
	if overlay.Region != "" {
		base.Region = overlay.Region
	}
	if overlay.Profile != "" {
		base.Profile = overlay.Profile
	}
	if overlay.APIMode != "" {
		base.APIMode = overlay.APIMode
	}
	if overlay.ModelPath != "" {
		base.ModelPath = overlay.ModelPath
	}
	if overlay.ModelSHA256 != "" {
		base.ModelSHA256 = overlay.ModelSHA256
	}
	if overlay.ChatGPTAuth != (autohandOAuthSettings{}) {
		base.ChatGPTAuth = overlay.ChatGPTAuth
	}
	if overlay.OAuthAuth != (autohandOAuthSettings{}) {
		base.OAuthAuth = overlay.OAuthAuth
	}
	if overlay.APIKeyRequired != nil {
		base.APIKeyRequired = overlay.APIKeyRequired
	}
	return base
}

func applyAutohandSet(config *autohandAuthConfig, input string) bool {
	key, raw, ok := strings.Cut(input, "=")
	if !ok || strings.TrimSpace(key) == "" {
		return false
	}
	parts := strings.Split(strings.TrimSpace(key), ".")
	for _, part := range parts {
		if !validAutohandSetSegment(part) {
			return false
		}
	}
	if parts[0] == "auth" || parts[0] == "profiles" {
		return false
	}
	if len(parts) == 1 && parts[0] == "provider" {
		config.Provider = strings.Trim(strings.TrimSpace(raw), `"`)
		return true
	}
	if len(parts) < 2 {
		return true
	}
	provider := parts[0]
	if provider == "customProviders" || provider == "extensionProviders" {
		if len(parts) != 3 {
			return false
		}
		if provider == "customProviders" {
			provider = "custom:" + parts[1]
		} else {
			provider = "extension:" + parts[1]
		}
		parts = []string{provider, parts[2]}
	}
	provider = normalizedAutohandProvider(provider)
	if !validAutohandProvider(provider) {
		return true
	}
	settings, _ := config.autohandProvider(provider)
	value := strings.TrimSpace(raw)
	var decoded any
	if value != "" && json.Unmarshal([]byte(value), &decoded) == nil {
		switch typed := decoded.(type) {
		case string:
			value = typed
		case bool:
			if parts[1] != "apiKeyRequired" {
				return false
			}
			settings.APIKeyRequired = &typed
			config.setAutohandProvider(provider, settings)
			return true
		default:
			return false
		}
	}
	if len(parts) == 3 {
		switch {
		case provider == "openai" && parts[1] == "chatgptAuth":
			switch parts[2] {
			case "accessToken":
				settings.ChatGPTAuth.AccessToken = value
			case "accountId":
				settings.ChatGPTAuth.AccountID = value
			default:
				return true
			}
		case provider == "xai" && parts[1] == "oauthAuth":
			if parts[2] != "accessToken" {
				return true
			}
			settings.OAuthAuth.AccessToken = value
		default:
			return true
		}
		config.setAutohandProvider(provider, settings)
		return true
	}
	switch parts[1] {
	case "id":
		settings.ID = value
	case "displayName":
		settings.DisplayName = value
	case "apiFormat":
		settings.APIFormat = value
	case "apiKey":
		settings.APIKey = value
	case "authToken":
		settings.AuthToken = value
	case "model":
		settings.Model = value
	case "plan":
		settings.Plan = value
	case "authMode":
		settings.AuthMode = value
	case "authMethod":
		settings.AuthMethod = value
	case "baseUrl":
		settings.BaseURL = value
	case "endpoint":
		settings.Endpoint = value
	case "resourceName":
		settings.ResourceName = value
	case "deploymentName":
		settings.DeploymentName = value
	case "tenantId":
		settings.TenantID = value
	case "clientId":
		settings.ClientID = value
	case "clientSecret":
		settings.ClientSecret = value
	case "projectId":
		settings.ProjectID = value
	case "region":
		settings.Region = value
	case "profile":
		settings.Profile = value
	case "apiMode":
		settings.APIMode = value
	case "modelPath":
		settings.ModelPath = value
	case "modelSha256":
		settings.ModelSHA256 = value
	default:
		return false
	}
	config.setAutohandProvider(provider, settings)
	return true
}

func validAutohandSetSegment(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func autohandProviderStatus(ctx context.Context, d authutil.Dependencies, config autohandAuthConfig, provider string) ports.AgentAuthStatus {
	settings, ok := config.autohandProvider(provider)
	if !ok || strings.TrimSpace(settings.Model) == "" {
		return ports.AgentAuthStatusUnknown
	}
	if provider == "blueprint-local" {
		if strings.TrimSpace(settings.ModelPath) == "" || strings.TrimSpace(settings.ModelSHA256) == "" {
			return ports.AgentAuthStatusUnknown
		}
		return ports.AgentAuthStatusNotApplicable
	}
	if provider == "ollama" || provider == "llamacpp" || provider == "mlx" {
		return ports.AgentAuthStatusNotApplicable
	}
	if strings.HasPrefix(provider, "custom:") {
		id := strings.TrimPrefix(provider, "custom:")
		if strings.TrimSpace(settings.ID) != id || strings.TrimSpace(settings.DisplayName) == "" ||
			strings.TrimSpace(settings.APIFormat) != "openai-compatible" || strings.TrimSpace(settings.BaseURL) == "" {
			return ports.AgentAuthStatusUnknown
		}
		if settings.APIKeyRequired != nil && !*settings.APIKeyRequired {
			return ports.AgentAuthStatusNotApplicable
		}
		return configuredIfSecret(settings.APIKey)
	}
	if strings.HasPrefix(provider, "extension:") {
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
	if provider == "openai" {
		switch strings.ToLower(strings.TrimSpace(settings.AuthMode)) {
		case "chatgpt":
			if usableSecret(settings.ChatGPTAuth.AccessToken) && usableSecret(settings.ChatGPTAuth.AccountID) {
				return ports.AgentAuthStatusConfigured
			}
			return ports.AgentAuthStatusUnknown
		case "", "api-key":
		default:
			return ports.AgentAuthStatusUnknown
		}
	}
	if provider == "xai" {
		switch strings.ToLower(strings.TrimSpace(settings.AuthMode)) {
		case "oauth":
			return configuredIfSecret(settings.OAuthAuth.AccessToken)
		case "", "api-key":
		default:
			return ports.AgentAuthStatusUnknown
		}
	}
	if provider == "bedrock" {
		apiMode := strings.ToLower(strings.TrimSpace(settings.APIMode))
		if apiMode == "" {
			apiMode = "converse"
		}
		if apiMode != "converse" && apiMode != "openai-chat" && apiMode != "openai-responses" {
			return ports.AgentAuthStatusUnknown
		}
		authMode := strings.ToLower(strings.TrimSpace(settings.AuthMode))
		if authMode == "" {
			if apiMode == "converse" {
				authMode = "aws-credentials"
			} else {
				authMode = "bedrock-api-key"
			}
		}
		if authMode == "bedrock-api-key" {
			return configuredIfSecret(settings.APIKey)
		}
		if authMode != "aws-credentials" {
			return ports.AgentAuthStatusUnknown
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
		baseURL := firstNonempty(settings.BaseURL, settings.Endpoint, d.Getenv("AZURE_OPENAI_ENDPOINT"))
		resource := strings.TrimSpace(settings.ResourceName)
		deployment := firstNonempty(settings.DeploymentName, d.Getenv("AZURE_OPENAI_DEPLOYMENT"))
		if baseURL == "" && (resource == "" || deployment == "") {
			return ports.AgentAuthStatusUnknown
		}
		switch strings.ToLower(strings.TrimSpace(settings.AuthMethod)) {
		case "", "api-key":
			if usableSecret(settings.APIKey) || usableSecret(d.Getenv("AZURE_OPENAI_KEY")) {
				return ports.AgentAuthStatusConfigured
			}
			return ports.AgentAuthStatusUnknown
		case "entra-id":
			if usableSecret(firstNonempty(settings.TenantID, d.Getenv("AZURE_TENANT_ID"))) &&
				usableSecret(firstNonempty(settings.ClientID, d.Getenv("AZURE_CLIENT_ID"))) &&
				usableSecret(firstNonempty(settings.ClientSecret, d.Getenv("AZURE_CLIENT_SECRET"))) {
				return ports.AgentAuthStatusConfigured
			}
			return ports.AgentAuthStatusUnknown
		case "managed-identity":
			return autohandManagedIdentityStatus(ctx, d)
		default:
			return ports.AgentAuthStatusUnknown
		}
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
	return entry, ok && entry != (autohandProviderSettings{})
}

func (config *autohandAuthConfig) setAutohandProvider(provider string, settings autohandProviderSettings) {
	if strings.HasPrefix(provider, "custom:") {
		if config.CustomProviders == nil {
			config.CustomProviders = make(map[string]autohandProviderSettings)
		}
		config.CustomProviders[strings.TrimPrefix(provider, "custom:")] = settings
		return
	}
	if strings.HasPrefix(provider, "extension:") {
		if config.ExtensionProviders == nil {
			config.ExtensionProviders = make(map[string]autohandProviderSettings)
		}
		config.ExtensionProviders[provider] = settings
		return
	}
	switch provider {
	case "autohandai":
		config.AutohandAI = settings
	case "openrouter":
		config.OpenRouter = settings
	case "anthropic":
		config.Anthropic = settings
	case "ollama":
		config.Ollama = settings
	case "llamacpp":
		config.LlamaCpp = settings
	case "openai":
		config.OpenAI = settings
	case "mlx":
		config.MLX = settings
	case "llmgateway":
		config.LLMGateway = settings
	case "azure":
		config.Azure = settings
	case "zai":
		config.ZAI = settings
	case "sakana":
		config.Sakana = settings
	case "vertexai":
		config.VertexAI = settings
	case "xai":
		config.XAI = settings
	case "cerebras":
		config.Cerebras = settings
	case "nvidia":
		config.NVIDIA = settings
	case "deepseek":
		config.DeepSeek = settings
	case "bedrock":
		config.Bedrock = settings
	case "blueprint-local":
		config.BlueprintLocal = settings
	}
}

var autohandProviderEnv = map[string]string{
	"autohandai": "AUTOHAND_AI_API_KEY", "openrouter": "OPENROUTER_API_KEY", "openai": "OPENAI_API_KEY",
	"llmgateway": "LLM_GATEWAY_API_KEY", "azure": "AZURE_OPENAI_KEY", "zai": "ZAI_API_KEY",
	"sakana": "SAKANA_API_KEY", "deepseek": "DEEPSEEK_API_KEY",
}

func validAutohandProvider(provider string) bool {
	if strings.HasPrefix(provider, "custom:") || strings.HasPrefix(provider, "extension:") {
		return strings.SplitN(provider, ":", 2)[1] != ""
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
	if strings.HasPrefix(auth.Token, "ahc_") {
		return ports.AgentAuthStatusConfigured
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

func autohandManagedIdentityStatus(ctx context.Context, d authutil.Dependencies) ports.AgentAuthStatus {
	baseGetenv := d.Getenv
	if baseGetenv == nil {
		baseGetenv = os.Getenv
	}
	d.Getenv = func(name string) string {
		switch name {
		case "IDENTITY_ENDPOINT", "IDENTITY_HEADER", "MSI_ENDPOINT", "MSI_SECRET":
			return baseGetenv(name)
		default:
			return ""
		}
	}
	d.Run = nil
	return authutil.AzureEvidence(ctx, d).Status
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
