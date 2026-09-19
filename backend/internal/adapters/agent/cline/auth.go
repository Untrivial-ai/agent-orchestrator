package cline

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

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
	return clineAuthStatus(ctx, scope, authutil.Dependencies{})
}

type clineProvidersFile struct {
	Version          int                      `json:"version"`
	LastUsedProvider json.RawMessage          `json:"lastUsedProvider"`
	Modes            json.RawMessage          `json:"modes"`
	Providers        map[string]clineProvider `json:"providers"`
}

type clineProvider struct {
	Settings    clineProviderSettings `json:"settings"`
	UpdatedAt   string                `json:"updatedAt"`
	TokenSource json.RawMessage       `json:"tokenSource"`
}

type clineProviderSettings struct {
	Provider          string                     `json:"provider"`
	APIKey            string                     `json:"apiKey"`
	Auth              *clineProviderAuth         `json:"auth"`
	Model             string                     `json:"model"`
	BaseURL           string                     `json:"baseUrl"`
	AWS               *clineAWSSettings          `json:"aws"`
	GCP               *clineGCPSettings          `json:"gcp"`
	Azure             *clineAzureSettings        `json:"azure"`
	SAP               *clineSAPSettings          `json:"sap"`
	OCA               *clineOCASettings          `json:"oca"`
	Protocol          string                     `json:"protocol"`
	Client            string                     `json:"client"`
	APILine           string                     `json:"apiLine"`
	Capabilities      []string                   `json:"capabilities"`
	MaxTokens         *int                       `json:"maxTokens"`
	ContextWindow     *int                       `json:"contextWindow"`
	Timeout           *int                       `json:"timeout"`
	Headers           map[string]string          `json:"headers"`
	RoutingProviderID string                     `json:"routingProviderId"`
	Region            string                     `json:"region"`
	Reasoning         *clineReasoningSettings    `json:"reasoning"`
	ModelCatalog      *clineModelCatalogSettings `json:"modelCatalog"`
}

type clineProviderAuth struct {
	APIKey       string `json:"apiKey"`
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    *int64 `json:"expiresAt"`
}

type clineAWSSettings struct {
	AccessKey               string `json:"accessKey"`
	SecretKey               string `json:"secretKey"`
	SessionToken            string `json:"sessionToken"`
	Region                  string `json:"region"`
	Profile                 string `json:"profile"`
	Authentication          string `json:"authentication"`
	UsePromptCache          *bool  `json:"usePromptCache"`
	UseCrossRegionInference *bool  `json:"useCrossRegionInference"`
	UseGlobalInference      *bool  `json:"useGlobalInference"`
	Endpoint                string `json:"endpoint"`
	CustomModelBaseID       string `json:"customModelBaseId"`
}

type clineGCPSettings struct {
	ProjectID string `json:"projectId"`
	Region    string `json:"region"`
}

type clineAzureSettings struct {
	APIVersion  string `json:"apiVersion"`
	UseIdentity bool   `json:"useIdentity"`
}

type clineSAPSettings struct {
	ClientID             string         `json:"clientId"`
	ClientSecret         string         `json:"clientSecret"`
	TokenURL             string         `json:"tokenUrl"`
	ResourceGroup        string         `json:"resourceGroup"`
	DeploymentID         string         `json:"deploymentId"`
	API                  string         `json:"api"`
	UseOrchestrationMode *bool          `json:"useOrchestrationMode"`
	DefaultSettings      map[string]any `json:"defaultSettings"`
}

type clineOCASettings struct {
	Mode string `json:"mode"`
}

type clineReasoningSettings struct {
	Enabled      *bool  `json:"enabled"`
	Effort       string `json:"effort"`
	BudgetTokens *int   `json:"budgetTokens"`
}

type clineModelCatalogSettings struct {
	LoadLatestOnInit        *bool  `json:"loadLatestOnInit"`
	IncludeClineCloudModels *bool  `json:"includeClineCloudModels"`
	LoadPrivateOnAuth       *bool  `json:"loadPrivateOnAuth"`
	URL                     string `json:"url"`
	CacheTTLMS              *int   `json:"cacheTtlMs"`
	FailOnError             *bool  `json:"failOnError"`
}

func clineAuthStatus(ctx context.Context, scope ports.AgentAuthCheck, d authutil.Dependencies) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	baseEnv := d.Getenv
	if baseEnv == nil {
		baseEnv = os.Getenv
	}
	d.Getenv = func(key string) string {
		if value, ok := scope.Env[key]; ok {
			return value
		}
		return baseEnv(key)
	}
	if d.GOOS == "" {
		d.GOOS = runtime.GOOS
	}

	args := clineAuthArgs(scope.Args)
	selected := strings.TrimSpace(args.provider)
	if strings.TrimSpace(args.key) != "" {
		return ports.AgentAuthStatusConfigured, nil
	}
	path := clineProviderSettingsPath(scope.WorkingDir, args, d)
	var providers clineProvidersFile
	if path != "" {
		if data, err := authutil.ReadFile(ctx, d, path); err == nil {
			if json.Unmarshal(data, &providers) != nil || !providers.valid() {
				providers = clineProvidersFile{}
			}
		}
	}
	if selected == "" {
		selected, _ = clineOptionalString(providers.LastUsedProvider)
	}
	if selected == "" {
		selected = "cline"
	}
	selected = clineNormalizeProviderID(selected)

	settings, ok := providers.selectedSettings(selected)
	if !ok {
		settings.Provider = selected
	}
	if settings.Provider != selected {
		return ports.AgentAuthStatusUnknown, nil
	}
	if (selected == "cline" || selected == "cline-pass") && strings.TrimSpace(d.Getenv("CLINE_API_KEY")) != "" {
		return ports.AgentAuthStatusConfigured, nil
	}
	return clineProviderEvidence(ctx, d, settings), ctx.Err()
}

func (f clineProvidersFile) valid() bool {
	if f.Version != 1 || f.Providers == nil || !clineValidModes(f.Modes) {
		return false
	}
	if lastUsed, ok := clineOptionalString(f.LastUsedProvider); !ok || (len(f.LastUsedProvider) > 0 && strings.TrimSpace(lastUsed) == "") {
		return false
	}
	for id, entry := range f.Providers {
		if !clineValidProviderID(id) || !clineValidProviderID(entry.Settings.Provider) || entry.Settings.Provider != id || !entry.Settings.valid() {
			return false
		}
		if _, err := time.Parse(time.RFC3339, entry.UpdatedAt); err != nil {
			return false
		}
		if len(entry.TokenSource) > 0 {
			tokenSource, ok := clineOptionalString(entry.TokenSource)
			if !ok {
				return false
			}
			switch tokenSource {
			case "manual", "oauth", "migration":
			default:
				return false
			}
		}
	}
	return true
}

func (s clineProviderSettings) valid() bool {
	if s.BaseURL != "" && !clineValidURL(s.BaseURL) {
		return false
	}
	if s.Auth != nil && s.Auth.ExpiresAt != nil && *s.Auth.ExpiresAt <= 0 {
		return false
	}
	if !clineEnum(s.Protocol, "anthropic", "gemini", "openai-chat", "openai-responses", "openai-r1", "ai-sdk") ||
		!clineEnum(s.Client, "anthropic", "ai-sdk", "ai-sdk-community", "openai", "openai-compatible", "openai-r1", "gemini", "bedrock", "custom", "fetch", "vertex") ||
		!clineEnum(s.APILine, "china", "international") ||
		!clinePositive(s.MaxTokens) || !clinePositive(s.ContextWindow) || !clinePositive(s.Timeout) {
		return false
	}
	for _, capability := range s.Capabilities {
		if !clineEnum(capability, "reasoning", "prompt-cache", "streaming", "tools", "vision", "computer-use", "oauth", "popular") || capability == "" {
			return false
		}
	}
	if s.AWS != nil {
		if !clineEnum(s.AWS.Authentication, "iam", "api-key", "apikey", "profile") || (s.AWS.Endpoint != "" && !clineValidURL(s.AWS.Endpoint)) {
			return false
		}
	}
	if s.SAP != nil {
		if !clineEnum(s.SAP.API, "orchestration", "foundation-models") || (s.SAP.TokenURL != "" && !clineValidURL(s.SAP.TokenURL)) {
			return false
		}
	}
	if s.OCA != nil && !clineEnum(s.OCA.Mode, "internal", "external") {
		return false
	}
	if s.Reasoning != nil && (!clineEnum(s.Reasoning.Effort, "none", "minimal", "low", "medium", "high", "xhigh", "max") || !clinePositive(s.Reasoning.BudgetTokens)) {
		return false
	}
	return s.ModelCatalog == nil || ((s.ModelCatalog.URL == "" || clineValidURL(s.ModelCatalog.URL)) && clinePositive(s.ModelCatalog.CacheTTLMS))
}

func (f clineProvidersFile) selectedSettings(selected string) (clineProviderSettings, bool) {
	entry, ok := f.Providers[selected]
	if !ok {
		for id, candidate := range f.Providers {
			if clineNormalizeProviderID(id) == selected {
				entry, ok = candidate, true
				break
			}
		}
	}
	if selected != "cline-pass" {
		if ok {
			entry.Settings.Provider = clineNormalizeProviderID(entry.Settings.Provider)
		}
		return entry.Settings, ok
	}
	storage, storageOK := f.Providers["cline"]
	if !ok && !storageOK {
		return clineProviderSettings{}, false
	}
	settings := clineProviderSettings{Provider: "cline-pass"}
	if storageOK {
		settings.APIKey = storage.Settings.APIKey
		settings.Auth = storage.Settings.Auth
		settings.BaseURL = storage.Settings.BaseURL
	}
	if ok {
		settings.Model = entry.Settings.Model
		if entry.Settings.APIKey != "" {
			settings.APIKey = entry.Settings.APIKey
		}
		if entry.Settings.Auth != nil {
			settings.Auth = entry.Settings.Auth
		}
		if entry.Settings.BaseURL != "" {
			settings.BaseURL = entry.Settings.BaseURL
		}
	}
	return settings, true
}

func clineProviderEvidence(ctx context.Context, d authutil.Dependencies, settings clineProviderSettings) ports.AgentAuthStatus {
	if settings.BaseURL != "" && !clineValidURL(settings.BaseURL) {
		return ports.AgentAuthStatusUnknown
	}
	if strings.TrimSpace(settings.APIKey) != "" || (settings.Auth != nil && strings.TrimSpace(settings.Auth.APIKey) != "") {
		return ports.AgentAuthStatusConfigured
	}
	for _, name := range clineProviderAPIKeyEnv[clineNormalizeProviderID(settings.Provider)] {
		if strings.TrimSpace(d.Getenv(name)) != "" {
			return ports.AgentAuthStatusConfigured
		}
	}
	if settings.Auth != nil && clineOAuthProvider(settings.Provider) {
		auth := settings.Auth
		if strings.TrimSpace(auth.AccessToken) != "" {
			refreshable := clineOAuthProvider(settings.Provider) && strings.TrimSpace(auth.RefreshToken) != ""
			if auth.ExpiresAt != nil && !time.UnixMilli(*auth.ExpiresAt).After(clineNow(d)) && !refreshable {
				return ports.AgentAuthStatusUnauthorized
			}
			return ports.AgentAuthStatusConfigured
		}
	}

	switch strings.ToLower(clineNormalizeProviderID(settings.Provider)) {
	case "ollama", "lmstudio":
		if settings.BaseURL == "" || clineLocalURL(settings.BaseURL) {
			return ports.AgentAuthStatusNotApplicable
		}
		return ports.AgentAuthStatusUnknown
	case "bedrock":
		if settings.AWS != nil && (settings.AWS.Authentication == "api-key" || settings.AWS.Authentication == "apikey") {
			return ports.AgentAuthStatusUnknown
		}
		if settings.AWS != nil && strings.TrimSpace(settings.AWS.AccessKey) != "" && strings.TrimSpace(settings.AWS.SecretKey) != "" {
			return ports.AgentAuthStatusConfigured
		}
		cloud := d
		baseEnv := cloud.Getenv
		cloud.Getenv = func(key string) string {
			if key == "AWS_PROFILE" && settings.AWS != nil && strings.TrimSpace(settings.AWS.Profile) != "" {
				return strings.TrimSpace(settings.AWS.Profile)
			}
			if key == "AWS_REGION" && settings.AWS != nil && strings.TrimSpace(settings.AWS.Region) != "" {
				return strings.TrimSpace(settings.AWS.Region)
			}
			return baseEnv(key)
		}
		return authutil.AWSEvidence(ctx, cloud).Status
	case "vertex":
		if settings.GCP == nil || strings.TrimSpace(settings.GCP.ProjectID) == "" {
			return ports.AgentAuthStatusUnknown
		}
		return authutil.GoogleADCEvidence(ctx, d).Status
	case "azure", "azure-openai":
		if settings.Azure == nil || !settings.Azure.UseIdentity {
			return ports.AgentAuthStatusUnknown
		}
		cloud := d
		baseEnv := cloud.Getenv
		cloud.Getenv = func(key string) string {
			if key == "AZURE_OPENAI_API_KEY" || key == "AZURE_API_KEY" {
				return ""
			}
			return baseEnv(key)
		}
		return authutil.AzureEvidence(ctx, cloud).Status
	case "sapaicore", "sap-ai-core":
		if settings.SAP != nil && strings.TrimSpace(settings.SAP.ClientID) != "" && strings.TrimSpace(settings.SAP.ClientSecret) != "" && clineValidURL(settings.SAP.TokenURL) {
			return ports.AgentAuthStatusConfigured
		}
	}
	return ports.AgentAuthStatusUnknown
}

var clineProviderAPIKeyEnv = map[string][]string{
	"anthropic":         {"ANTHROPIC_API_KEY"},
	"aihubmix":          {"AIHUBMIX_API_KEY"},
	"asksage":           {"ASKSAGE_API_KEY"},
	"cline":             {"CLINE_API_KEY"},
	"cline-pass":        {"CLINE_API_KEY"},
	"cerebras":          {"CEREBRAS_API_KEY"},
	"crusoe":            {"CRUSOE_API_KEY"},
	"deepseek":          {"DEEPSEEK_API_KEY"},
	"dify":              {"DIFY_API_KEY"},
	"doubao":            {"DOUBAO_API_KEY"},
	"elevenlabs":        {"ELEVENLABS_API_KEY"},
	"gemini":            {"GOOGLE_GENERATIVE_AI_API_KEY", "GEMINI_API_KEY"},
	"groq":              {"GROQ_API_KEY"},
	"hicap":             {"HICAP_API_KEY"},
	"huawei-cloud-maas": {"HUAWEI_CLOUD_MAAS_API_KEY"},
	"kilo":              {"KILO_GATEWAY_API_KEY"},
	"litellm":           {"LITELLM_API_KEY"},
	"lmstudio":          {"LMSTUDIO_API_KEY"},
	"nousResearch":      {"NOUS_RESEARCH_API_KEY", "NOUSRESEARCH_API_KEY"},
	"oca":               {"OCA_API_KEY"},
	"ollama":            {"OLLAMA_API_KEY"},
	"openai-compatible": {"OPENAI_API_KEY"},
	"openai-native":     {"OPENAI_API_KEY"},
	"openrouter":        {"OPENROUTER_API_KEY"},
	"qwen":              {"QWEN_API_KEY"},
	"sambanova":         {"SAMBANOVA_API_KEY"},
	"sapaicore":         {"AICORE_SERVICE_KEY", "VCAP_SERVICES"},
	"together":          {"TOGETHER_API_KEY"},
	"v0":                {"V0_API_KEY"},
	"vercel-ai-gateway": {"AI_GATEWAY_API_KEY"},
	"xai":               {"XAI_API_KEY"},
	"zai":               {"ZHIPU_API_KEY"},
	"zai-coding-plan":   {"ZHIPU_API_KEY"},
}

func clineNormalizeProviderID(provider string) string {
	switch strings.TrimSpace(provider) {
	case "openai":
		return "openai-compatible"
	case "togetherai":
		return "together"
	case "sap-ai-core":
		return "sapaicore"
	default:
		return strings.TrimSpace(provider)
	}
}

func clineOAuthProvider(provider string) bool {
	switch clineNormalizeProviderID(provider) {
	case "cline", "cline-pass", "oca", "openai-codex":
		return true
	default:
		return false
	}
}

func clineValidProviderID(value string) bool {
	if value == "" {
		return false
	}
	for index, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || (r == '-' && index > 0) {
			continue
		}
		return false
	}
	return true
}

func clineValidModes(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return false
	}
	voice, ok := fields["voiceInput"]
	if !ok {
		return true
	}
	var settings struct {
		ProviderID string `json:"providerId"`
		ModelID    string `json:"modelId"`
	}
	return !bytes.Equal(bytes.TrimSpace(voice), []byte("null")) && json.Unmarshal(voice, &settings) == nil && strings.TrimSpace(settings.ProviderID) != "" && strings.TrimSpace(settings.ModelID) != ""
}

func clineEnum(value string, allowed ...string) bool {
	if value == "" {
		return true
	}
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func clinePositive(value *int) bool {
	return value == nil || *value > 0
}

func clineOptionalString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", true
	}
	var value string
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

type clineArgs struct {
	configDir string
	dataDir   string
	provider  string
	key       string
}

func clineAuthArgs(args []string) clineArgs {
	var result clineArgs
	for index := 0; index < len(args); index++ {
		arg := args[index]
		assign := func(target *string) {
			if index+1 < len(args) && !strings.HasPrefix(args[index+1], "-") {
				index++
				*target = strings.TrimSpace(args[index])
			}
		}
		switch arg {
		case "--config":
			assign(&result.configDir)
		case "--data-dir":
			assign(&result.dataDir)
		case "--provider", "-P":
			assign(&result.provider)
		case "--key", "-k":
			assign(&result.key)
		default:
			for prefix, target := range map[string]*string{
				"--config=": &result.configDir, "--data-dir=": &result.dataDir,
				"--provider=": &result.provider, "--key=": &result.key,
			} {
				if strings.HasPrefix(arg, prefix) {
					*target = strings.TrimSpace(strings.TrimPrefix(arg, prefix))
					break
				}
			}
		}
	}
	return result
}

func clineProviderSettingsPath(workingDir string, args clineArgs, d authutil.Dependencies) string {
	resolve := func(path string) string {
		path = strings.TrimSpace(path)
		if path == "" || filepath.IsAbs(path) || workingDir == "" {
			return path
		}
		return filepath.Join(workingDir, path)
	}
	if args.dataDir != "" {
		return filepath.Join(resolve(args.dataDir), "settings", "providers.json")
	}
	if path := d.Getenv("CLINE_PROVIDER_SETTINGS_PATH"); strings.TrimSpace(path) != "" {
		return resolve(path)
	}
	dataDir := strings.TrimSpace(d.Getenv("CLINE_DATA_DIR"))
	if dataDir == "" {
		clineDir := args.configDir
		if clineDir == "" {
			clineDir = strings.TrimSpace(d.Getenv("CLINE_DIR"))
		}
		if clineDir == "" {
			home := d.Getenv("HOME")
			if d.GOOS == "windows" {
				home = d.Getenv("USERPROFILE")
			}
			if home != "" {
				clineDir = filepath.Join(home, ".cline")
			}
		}
		if clineDir != "" {
			dataDir = filepath.Join(resolve(clineDir), "data")
		}
	} else {
		dataDir = resolve(dataDir)
	}
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "settings", "providers.json")
}

func clineValidURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != ""
}

func clineLocalURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func clineNow(d authutil.Dependencies) time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}
