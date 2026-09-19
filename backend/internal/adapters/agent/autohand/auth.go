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
	Profiles           map[string]map[string]any           `json:"profiles" toml:"profiles" yaml:"profiles"`
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
	Disabled       bool                  `json:"disabled" toml:"disabled" yaml:"disabled"`
	ChatGPTAuth    autohandOAuthSettings `json:"chatgptAuth" toml:"chatgptAuth" yaml:"chatgptAuth"`
	OAuthAuth      autohandOAuthSettings `json:"oauthAuth" toml:"oauthAuth" yaml:"oauthAuth"`
	APIKeyRequired *bool                 `json:"apiKeyRequired" toml:"apiKeyRequired" yaml:"apiKeyRequired"`
}

type autohandOAuthSettings struct {
	AccessToken  string `json:"accessToken" toml:"accessToken" yaml:"accessToken"`
	RefreshToken string `json:"refreshToken" toml:"refreshToken" yaml:"refreshToken"`
	AccountID    string `json:"accountId" toml:"accountId" yaml:"accountId"`
	ExpiresAt    string `json:"expiresAt" toml:"expiresAt" yaml:"expiresAt"`
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
		if !applyAutohandProfile(config, overlay) {
			return false
		}
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

func applyAutohandProfile(config *autohandAuthConfig, profile map[string]any) bool {
	var applyLeaves func([]string, any) bool
	applyLeaves = func(path []string, value any) bool {
		if object, ok := value.(map[string]any); ok && len(object) > 0 {
			for key, child := range object {
				if !applyLeaves(append(path, key), child) {
					return false
				}
			}
			return true
		}
		return applyAutohandPath(config, path, value)
	}
	for key, value := range profile {
		if !applyLeaves([]string{key}, value) {
			return false
		}
	}
	return true
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
	value := any(strings.TrimSpace(raw))
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(strings.TrimSpace(raw)), &value)
	}
	return applyAutohandPath(config, parts, value)
}

func applyAutohandPath(config *autohandAuthConfig, parts []string, value any) bool {
	if len(parts) == 0 {
		return false
	}
	for index, part := range parts {
		if index == 1 && parts[0] == "extensionProviders" && strings.HasPrefix(part, "extension:") {
			continue
		}
		if !validAutohandSetSegment(part) {
			return false
		}
	}
	switch parts[0] {
	case "profiles", "auth", "configPath", "isNewConfig", "workspaceOverlay", "workspaceTrust", "overlayWorkspaceRoot", "runOverlay":
		return false
	}
	if len(parts) == 1 && parts[0] == "provider" {
		provider, ok := value.(string)
		if !ok {
			return false
		}
		config.Provider = provider
		return true
	}
	if len(parts) < 2 {
		return true
	}
	provider := parts[0]
	if provider == "customProviders" || provider == "extensionProviders" {
		if len(parts) < 2 {
			return false
		}
		if provider == "customProviders" {
			provider = "custom:" + parts[1]
		} else {
			provider = parts[1]
			if !strings.HasPrefix(provider, "extension:") {
				provider = "extension:" + provider
			}
		}
		parts = append([]string{provider}, parts[2:]...)
	}
	provider = normalizedAutohandProvider(provider)
	if !validAutohandProvider(provider) {
		return true
	}
	if len(parts) == 1 {
		if object, ok := value.(map[string]any); !ok || len(object) != 0 {
			return false
		}
		config.setAutohandProvider(provider, autohandProviderSettings{})
		return true
	}
	settings, _ := config.autohandProvider(provider)
	if len(parts) == 2 && (parts[1] == "chatgptAuth" || parts[1] == "oauthAuth") {
		if object, ok := value.(map[string]any); !ok || len(object) != 0 {
			return false
		}
		if parts[1] == "chatgptAuth" {
			settings.ChatGPTAuth = autohandOAuthSettings{}
		} else {
			settings.OAuthAuth = autohandOAuthSettings{}
		}
		config.setAutohandProvider(provider, settings)
		return true
	}
	if len(parts) == 3 {
		text, ok := value.(string)
		if !ok {
			return false
		}
		switch {
		case provider == "openai" && parts[1] == "chatgptAuth":
			switch parts[2] {
			case "accessToken":
				settings.ChatGPTAuth.AccessToken = text
			case "refreshToken":
				settings.ChatGPTAuth.RefreshToken = text
			case "accountId":
				settings.ChatGPTAuth.AccountID = text
			case "expiresAt":
				settings.ChatGPTAuth.ExpiresAt = text
			default:
				return true
			}
		case provider == "xai" && parts[1] == "oauthAuth":
			switch parts[2] {
			case "accessToken":
				settings.OAuthAuth.AccessToken = text
			case "refreshToken":
				settings.OAuthAuth.RefreshToken = text
			case "expiresAt":
				settings.OAuthAuth.ExpiresAt = text
			default:
				return true
			}
		default:
			return true
		}
		config.setAutohandProvider(provider, settings)
		return true
	}
	if len(parts) != 2 {
		return true
	}
	if parts[1] == "apiKeyRequired" {
		required, ok := value.(bool)
		if !ok {
			return false
		}
		settings.APIKeyRequired = &required
		config.setAutohandProvider(provider, settings)
		return true
	}
	if parts[1] == "disabled" {
		disabled, ok := value.(bool)
		if !ok {
			return false
		}
		settings.Disabled = disabled
		config.setAutohandProvider(provider, settings)
		return true
	}
	text, ok := value.(string)
	if !ok {
		return false
	}
	switch parts[1] {
	case "id":
		settings.ID = text
	case "displayName":
		settings.DisplayName = text
	case "apiFormat":
		settings.APIFormat = text
	case "apiKey":
		settings.APIKey = text
	case "authToken":
		settings.AuthToken = text
	case "model":
		settings.Model = text
	case "plan":
		settings.Plan = text
	case "authMode":
		settings.AuthMode = text
	case "authMethod":
		settings.AuthMethod = text
	case "baseUrl":
		settings.BaseURL = text
	case "endpoint":
		settings.Endpoint = text
	case "resourceName":
		settings.ResourceName = text
	case "deploymentName":
		settings.DeploymentName = text
	case "tenantId":
		settings.TenantID = text
	case "clientId":
		settings.ClientID = text
	case "clientSecret":
		settings.ClientSecret = text
	case "projectId":
		settings.ProjectID = text
	case "region":
		settings.Region = text
	case "profile":
		settings.Profile = text
	case "apiMode":
		settings.APIMode = text
	case "modelPath":
		settings.ModelPath = text
	case "modelSha256":
		settings.ModelSHA256 = text
	default:
		return true
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
		modelPath := strings.TrimSpace(settings.ModelPath)
		modelSHA256 := strings.TrimSpace(settings.ModelSHA256)
		if !filepath.IsAbs(modelPath) || filepath.Clean(modelPath) != modelPath || filepath.Ext(modelPath) != ".gguf" ||
			!validAutohandSHA256(modelSHA256) {
			return ports.AgentAuthStatusUnknown
		}
		return ports.AgentAuthStatusNotApplicable
	}
	if provider == "ollama" || provider == "llamacpp" || provider == "mlx" {
		return ports.AgentAuthStatusNotApplicable
	}
	if strings.HasPrefix(provider, "custom:") {
		id := strings.TrimPrefix(provider, "custom:")
		if settings.Disabled || strings.TrimSpace(settings.ID) != id || strings.TrimSpace(settings.DisplayName) == "" ||
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
			return autohandOAuthStatus(settings.ChatGPTAuth, true, d)
		case "", "api-key":
		default:
			return ports.AgentAuthStatusUnknown
		}
	}
	if provider == "xai" {
		switch strings.ToLower(strings.TrimSpace(settings.AuthMode)) {
		case "oauth":
			return autohandOAuthStatus(settings.OAuthAuth, false, d)
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
		if (apiMode == "converse" && authMode != "aws-credentials") ||
			(apiMode != "converse" && authMode != "bedrock-api-key") {
			return ports.AgentAuthStatusUnknown
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
		baseURL := firstNonempty(settings.BaseURL, d.Getenv("AZURE_OPENAI_ENDPOINT"))
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

func validAutohandSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func autohandOAuthStatus(auth autohandOAuthSettings, requireAccount bool, d authutil.Dependencies) ports.AgentAuthStatus {
	if !usableSecret(auth.AccessToken) || (requireAccount && !usableSecret(auth.AccountID)) {
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
	if !expires.After(now()) && !usableSecret(auth.RefreshToken) {
		return ports.AgentAuthStatusUnknown
	}
	return ports.AgentAuthStatusConfigured
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
