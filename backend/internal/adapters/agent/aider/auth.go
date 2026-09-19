package aider

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"gopkg.in/yaml.v3"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentScopedAuthChecker = (*Plugin)(nil)

// AuthStatus reports device-wide Aider credentials without assuming a workspace.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.AuthStatusFor(ctx, ports.AgentAuthCheck{})
}

// AuthStatusFor reports credentials for Aider's effective workspace and model.
func (p *Plugin) AuthStatusFor(ctx context.Context, check ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return p.authStatusFor(ctx, check, authutil.Dependencies{})
}

func (p *Plugin) authStatusFor(ctx context.Context, check ports.AgentAuthCheck, d authutil.Dependencies) (ports.AgentAuthStatus, error) {
	return aiderAuthStatus(ctx, check, d)
}

type aiderStringList []string

func (values *aiderStringList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag != "!!str" {
			return errors.New("invalid Aider string list")
		}
		*values = []string{node.Value}
		return nil
	case yaml.SequenceNode:
		result := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
				return errors.New("invalid Aider string list")
			}
			result = append(result, item.Value)
		}
		*values = result
		return nil
	default:
		return errors.New("invalid Aider string list")
	}
}

type aiderConfigLayer struct {
	Model           *string          `yaml:"model"`
	EnvFile         *string          `yaml:"env-file"`
	OpenAIAPIKey    *string          `yaml:"openai-api-key"`
	AnthropicAPIKey *string          `yaml:"anthropic-api-key"`
	OpenAIAPIBase   *string          `yaml:"openai-api-base"`
	APIKey          *aiderStringList `yaml:"api-key"`
	SetEnv          *aiderStringList `yaml:"set-env"`
}

type aiderConfig struct {
	Model           string
	EnvFile         string
	OpenAIAPIKey    string
	AnthropicAPIKey string
	OpenAIAPIBase   string
	APIKey          []string
	SetEnv          []string
}

func (config *aiderConfig) apply(layer aiderConfigLayer) {
	if layer.Model != nil {
		config.Model = *layer.Model
	}
	if layer.EnvFile != nil {
		config.EnvFile = *layer.EnvFile
	}
	if layer.OpenAIAPIKey != nil {
		config.OpenAIAPIKey = *layer.OpenAIAPIKey
	}
	if layer.AnthropicAPIKey != nil {
		config.AnthropicAPIKey = *layer.AnthropicAPIKey
	}
	if layer.OpenAIAPIBase != nil {
		config.OpenAIAPIBase = *layer.OpenAIAPIBase
	}
	if layer.APIKey != nil {
		config.APIKey = append([]string(nil), (*layer.APIKey)...)
	}
	if layer.SetEnv != nil {
		config.SetEnv = append([]string(nil), (*layer.SetEnv)...)
	}
}

type aiderArgs struct {
	ConfigPath       string
	ConfigPathSet    bool
	EnvFile          string
	EnvFileSet       bool
	Model            string
	ModelSet         bool
	OpenAIAPIKey     string
	OpenAIKeySet     bool
	AnthropicAPIKey  string
	AnthropicKeySet  bool
	APIKeys          []string
	EnvironmentPairs []string
}

type aiderEnvironment struct {
	overrides  map[string]string
	base       func(string) string
	baseLookup func(string) (string, bool)
}

func (environment aiderEnvironment) get(name string) string {
	if value, ok := environment.overrides[name]; ok {
		return value
	}
	return environment.base(name)
}

func (environment aiderEnvironment) set(name, value string) {
	environment.overrides[name] = value
}

func (environment aiderEnvironment) lookup(name string) (string, bool) {
	if value, ok := environment.overrides[name]; ok {
		return value, true
	}
	if environment.baseLookup != nil {
		return environment.baseLookup(name)
	}
	value := environment.base(name)
	return value, value != ""
}

func aiderAuthStatus(ctx context.Context, check ports.AgentAuthCheck, d authutil.Dependencies) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	args, valid := parseAiderArgs(check.Args)
	if !valid {
		return ports.AgentAuthStatusUnknown, nil
	}
	baseGetenv := d.Getenv
	var baseLookup func(string) (string, bool)
	if baseGetenv == nil {
		baseGetenv = os.Getenv
		baseLookup = os.LookupEnv
	}
	environment := aiderEnvironment{overrides: make(map[string]string), base: baseGetenv, baseLookup: baseLookup}
	for name, value := range check.Env {
		environment.set(name, value)
	}
	d.Getenv = environment.get

	home := aiderHome(d)
	workingDir := ""
	if filepath.IsAbs(check.WorkingDir) {
		workingDir = filepath.Clean(check.WorkingDir)
	}
	gitRoot, err := aiderGitRoot(ctx, d, workingDir)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}

	configPaths, explicitConfig := aiderConfigPaths(args, home, gitRoot, workingDir)
	if explicitConfig && len(configPaths) == 0 {
		return ports.AgentAuthStatusUnknown, nil
	}
	config := aiderConfig{}
	for _, path := range configPaths {
		if !aiderFileExists(d, path) {
			if explicitConfig {
				return ports.AgentAuthStatusUnknown, nil
			}
			continue
		}
		var layer aiderConfigLayer
		if authutil.ReadYAML(ctx, d, path, &layer) != nil {
			if err := ctx.Err(); err != nil {
				return ports.AgentAuthStatusUnknown, err
			}
			return ports.AgentAuthStatusUnknown, nil
		}
		config.apply(layer)
	}

	explicitEnvFile := config.EnvFile
	if value := strings.TrimSpace(environment.get("AIDER_ENV_FILE")); value != "" {
		explicitEnvFile = value
	}
	if args.EnvFileSet {
		explicitEnvFile = args.EnvFile
	}
	for _, path := range aiderDotenvPaths(home, gitRoot, workingDir, explicitEnvFile) {
		if !aiderFileExists(d, path) {
			continue
		}
		data, readErr := authutil.ReadFile(ctx, d, path)
		if readErr != nil {
			if err := ctx.Err(); err != nil {
				return ports.AgentAuthStatusUnknown, err
			}
			continue
		}
		if applyAiderDotenv(data, environment) != nil {
			continue
		}
	}

	if !applyAiderAssignments(environment, config.SetEnv, false) {
		return ports.AgentAuthStatusUnknown, nil
	}
	if value := strings.TrimSpace(environment.get("AIDER_SET_ENV")); value != "" &&
		!applyAiderAssignments(environment, []string{value}, false) {
		return ports.AgentAuthStatusUnknown, nil
	}
	if !applyAiderAssignments(environment, args.EnvironmentPairs, false) {
		return ports.AgentAuthStatusUnknown, nil
	}
	if !applyAiderAssignments(environment, config.APIKey, true) {
		return ports.AgentAuthStatusUnknown, nil
	}
	if value := strings.TrimSpace(environment.get("AIDER_API_KEY")); value != "" &&
		!applyAiderAssignments(environment, []string{value}, true) {
		return ports.AgentAuthStatusUnknown, nil
	}
	if !applyAiderAssignments(environment, args.APIKeys, true) {
		return ports.AgentAuthStatusUnknown, nil
	}
	applyAiderDirectKeys(environment, config, args)
	d.Getenv = environment.get

	model := strings.TrimSpace(config.Model)
	if value := strings.TrimSpace(environment.get("AIDER_MODEL")); value != "" {
		model = value
	}
	if value := strings.TrimSpace(check.Config.Model); value != "" {
		model = value
	}
	if args.ModelSet {
		model = strings.TrimSpace(args.Model)
	}
	if model == "" {
		model = aiderDefaultModel(environment)
	}
	provider := aiderModelProvider(model)
	if provider == "" {
		return ports.AgentAuthStatusUnknown, nil
	}

	status := aiderProviderStatus(ctx, provider, environment, d)
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return status, nil
}

func applyAiderDotenv(data []byte, environment aiderEnvironment) error {
	if _, err := authutil.ParseDotenv(data); err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		values, err := authutil.ParseDotenv([]byte(line))
		if err != nil {
			return err
		}
		for name, value := range values {
			environment.set(name, interpolateAiderDotenv(value, environment))
		}
	}
	return nil
}

func interpolateAiderDotenv(value string, environment aiderEnvironment) string {
	var result strings.Builder
	for {
		start := strings.Index(value, "${")
		if start < 0 {
			result.WriteString(value)
			return result.String()
		}
		result.WriteString(value[:start])
		rest := value[start+2:]
		end := strings.IndexByte(rest, '}')
		if end < 0 {
			result.WriteString(value[start:])
			return result.String()
		}
		expression := rest[:end]
		name, fallback, hasFallback := strings.Cut(expression, ":-")
		if strings.Contains(name, ":") {
			result.WriteString(value[start : start+2+end+1])
		} else if resolved, ok := environment.lookup(name); ok {
			result.WriteString(resolved)
		} else if hasFallback {
			result.WriteString(fallback)
		}
		value = rest[end+1:]
	}
}

func parseAiderArgs(values []string) (aiderArgs, bool) {
	var result aiderArgs
	for index := 0; index < len(values); index++ {
		if values[index] == "--" {
			break
		}
		name, value, inline := strings.Cut(values[index], "=")
		var target *string
		switch name {
		case "--config", "-c":
			target, result.ConfigPathSet = &result.ConfigPath, true
		case "--env-file":
			target, result.EnvFileSet = &result.EnvFile, true
		case "--model":
			target, result.ModelSet = &result.Model, true
		case "--openai-api-key":
			target, result.OpenAIKeySet = &result.OpenAIAPIKey, true
		case "--anthropic-api-key":
			target, result.AnthropicKeySet = &result.AnthropicAPIKey, true
		case "--api-key", "--set-env":
			if !inline {
				if index+1 >= len(values) {
					return aiderArgs{}, false
				}
				index++
				value = values[index]
			}
			if strings.TrimSpace(value) == "" {
				return aiderArgs{}, false
			}
			if name == "--api-key" {
				result.APIKeys = append(result.APIKeys, value)
			} else {
				result.EnvironmentPairs = append(result.EnvironmentPairs, value)
			}
			continue
		default:
			continue
		}
		if !inline {
			if index+1 >= len(values) {
				return aiderArgs{}, false
			}
			index++
			value = values[index]
		}
		if strings.TrimSpace(value) == "" {
			return aiderArgs{}, false
		}
		*target = value
	}
	return result, true
}

func aiderHome(d authutil.Dependencies) string {
	goos := d.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos == "windows" {
		if home := strings.TrimSpace(d.Getenv("USERPROFILE")); filepath.IsAbs(home) {
			return filepath.Clean(home)
		}
	}
	if home := strings.TrimSpace(d.Getenv("HOME")); filepath.IsAbs(home) {
		return filepath.Clean(home)
	}
	return ""
}

func aiderGitRoot(ctx context.Context, d authutil.Dependencies, start string) (string, error) {
	if start == "" {
		return "", nil
	}
	lstat := d.Lstat
	if lstat == nil {
		lstat = os.Lstat
	}
	for dir := start; ; dir = filepath.Dir(dir) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if _, err := lstat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		if filepath.Dir(dir) == dir {
			return "", nil
		}
	}
}

func aiderConfigPaths(args aiderArgs, home, gitRoot, workingDir string) ([]string, bool) {
	path, explicit := "", false
	if args.ConfigPathSet {
		path, explicit = args.ConfigPath, true
	}
	if explicit {
		if resolved := aiderResolvePath(path, workingDir); resolved != "" {
			return []string{resolved}, true
		}
		return nil, true
	}
	return uniqueAiderPaths(
		aiderJoinPath(home, ".aider.conf.yml"),
		aiderJoinPath(gitRoot, ".aider.conf.yml"),
		aiderJoinPath(workingDir, ".aider.conf.yml"),
	), false
}

func aiderDotenvPaths(home, gitRoot, workingDir, explicit string) []string {
	paths := []string{aiderJoinPath(home, ".aider", "oauth-keys.env"), aiderJoinPath(home, ".env")}
	paths = append(paths, aiderJoinPath(gitRoot, ".env"), aiderJoinPath(workingDir, ".env"))
	if path := aiderResolvePath(explicit, workingDir); path != "" {
		paths = append(paths, path)
	}
	return uniqueAiderPaths(paths...)
}

func aiderJoinPath(root string, elements ...string) string {
	if root == "" {
		return ""
	}
	return filepath.Join(append([]string{root}, elements...)...)
}

func uniqueAiderPaths(paths ...string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" || path == "." {
			continue
		}
		path = filepath.Clean(path)
		if seen[path] {
			continue
		}
		seen[path] = true
		result = append(result, path)
	}
	return result
}

func aiderResolvePath(path, workingDir string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if workingDir == "" {
		return ""
	}
	return filepath.Join(workingDir, path)
}

func aiderFileExists(d authutil.Dependencies, path string) bool {
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

func applyAiderAssignments(environment aiderEnvironment, values []string, apiKey bool) bool {
	for _, entry := range values {
		name, value, ok := strings.Cut(entry, "=")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || name == "" {
			return false
		}
		if apiKey {
			name = strings.ToUpper(name) + "_API_KEY"
		} else if !validAiderEnvName(name) {
			return false
		}
		environment.set(name, value)
	}
	return true
}

func validAiderEnvName(name string) bool {
	if name == "" {
		return false
	}
	for index, char := range name {
		if char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || index > 0 && char >= '0' && char <= '9' {
			continue
		}
		return false
	}
	return true
}

func applyAiderDirectKeys(environment aiderEnvironment, config aiderConfig, args aiderArgs) {
	openAIKey, anthropicKey := config.OpenAIAPIKey, config.AnthropicAPIKey
	if value := environment.get("AIDER_OPENAI_API_KEY"); strings.TrimSpace(value) != "" {
		openAIKey = value
	}
	if value := environment.get("AIDER_ANTHROPIC_API_KEY"); strings.TrimSpace(value) != "" {
		anthropicKey = value
	}
	if args.OpenAIKeySet {
		openAIKey = args.OpenAIAPIKey
	}
	if args.AnthropicKeySet {
		anthropicKey = args.AnthropicAPIKey
	}
	if openAIKey != "" {
		environment.set("OPENAI_API_KEY", openAIKey)
	}
	if anthropicKey != "" {
		environment.set("ANTHROPIC_API_KEY", anthropicKey)
	}
	if config.OpenAIAPIBase != "" && strings.TrimSpace(environment.get("AIDER_OPENAI_API_BASE")) == "" {
		environment.set("OPENAI_API_BASE", config.OpenAIAPIBase)
	}
}

func aiderDefaultModel(environment aiderEnvironment) string {
	for _, candidate := range []struct {
		name  string
		model string
	}{
		{"OPENROUTER_API_KEY", "openrouter/default"},
		{"ANTHROPIC_API_KEY", "anthropic/default"},
		{"DEEPSEEK_API_KEY", "deepseek/default"},
		{"OPENAI_API_KEY", "openai/default"},
		{"GEMINI_API_KEY", "gemini/default"},
		{"VERTEXAI_PROJECT", "vertex_ai/default"},
	} {
		if usableAiderSecret(environment.get(candidate.name)) {
			return candidate.model
		}
	}
	return ""
}

func aiderModelProvider(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return ""
	}
	if resolved, ok := aiderBuiltInModelAliases[model]; ok {
		model = resolved
	}
	if strings.HasPrefix(model, "us.anthropic.") || strings.HasPrefix(model, "global.anthropic.") {
		return "bedrock"
	}
	provider, _, hasSlash := strings.Cut(model, "/")
	if !hasSlash {
		switch {
		case model == "sonnet" || model == "opus" || model == "haiku" || strings.HasPrefix(model, "claude-"):
			return "anthropic"
		case model == "deepseek" || strings.HasPrefix(model, "deepseek-"):
			return "deepseek"
		case model == "gemini" || strings.HasPrefix(model, "gemini-"):
			return "gemini"
		case strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "o1") || strings.HasPrefix(model, "o3") || strings.HasPrefix(model, "o4"):
			return "openai"
		default:
			return ""
		}
	}
	switch provider {
	case "amazon-bedrock":
		return "bedrock"
	case "vertex", "vertexai":
		return "vertex_ai"
	case "lm-studio":
		return "lm_studio"
	case "ollama_chat":
		return "ollama"
	case "fireworks":
		return "fireworks_ai"
	case "perplexity":
		return "perplexity_ai"
	case "together":
		return "together_ai"
	case "nvidia":
		return "nvidia_nim"
	case "cohere_chat":
		return "cohere"
	default:
		return provider
	}
}

var aiderBuiltInModelAliases = map[string]string{
	"sonnet":               "claude-sonnet-4-6",
	"haiku":                "claude-haiku-4-5",
	"opus":                 "claude-opus-4-7",
	"4":                    "gpt-4-0613",
	"4o":                   "gpt-4o",
	"4-turbo":              "gpt-4-1106-preview",
	"35turbo":              "gpt-3.5-turbo",
	"35-turbo":             "gpt-3.5-turbo",
	"3":                    "gpt-3.5-turbo",
	"deepseek":             "deepseek/deepseek-chat",
	"flash":                "gemini/gemini-flash-latest",
	"flash-lite":           "gemini/gemini-2.5-flash-lite",
	"quasar":               "openrouter/openrouter/quasar-alpha",
	"r1":                   "deepseek/deepseek-reasoner",
	"gemini-2.5-pro":       "gemini/gemini-2.5-pro",
	"gemini-3-pro-preview": "gemini/gemini-3-pro-preview",
	"gemini":               "gemini/gemini-3-pro-preview",
	"gemini-exp":           "gemini/gemini-2.5-pro-exp-03-25",
	"grok3":                "xai/grok-3-beta",
	"optimus":              "openrouter/openrouter/optimus-alpha",
}

func aiderProviderStatus(ctx context.Context, provider string, environment aiderEnvironment, d authutil.Dependencies) ports.AgentAuthStatus {
	d.Getenv = environment.get
	switch provider {
	case "bedrock":
		return authutil.AWSEvidence(ctx, d).Status
	case "vertex_ai":
		if strings.TrimSpace(environment.get("VERTEXAI_PROJECT")) == "" || strings.TrimSpace(environment.get("VERTEXAI_LOCATION")) == "" {
			return ports.AgentAuthStatusUnknown
		}
		return authutil.GoogleADCEvidence(ctx, d).Status
	case "azure":
		base := firstAiderValue(environment.get("AZURE_API_BASE"), environment.get("AZURE_OPENAI_ENDPOINT"))
		version := firstAiderValue(environment.get("AZURE_API_VERSION"), environment.get("AZURE_OPENAI_API_VERSION"))
		if !validAiderURL(base) || version == "" {
			return ports.AgentAuthStatusUnknown
		}
		if aiderHasProviderKey(provider, environment) {
			return ports.AgentAuthStatusConfigured
		}
		return authutil.AzureEvidence(ctx, d).Status
	case "azure_ai":
		if !validAiderURL(environment.get("AZURE_AI_API_BASE")) {
			return ports.AgentAuthStatusUnknown
		}
		if aiderHasProviderKey(provider, environment) {
			return ports.AgentAuthStatusConfigured
		}
		return ports.AgentAuthStatusUnknown
	case "ollama":
		if usableAiderSecret(environment.get("OLLAMA_API_KEY")) {
			return ports.AgentAuthStatusConfigured
		}
		return ports.AgentAuthStatusNotApplicable
	case "lm_studio":
		if usableAiderSecret(environment.get("LM_STUDIO_API_KEY")) {
			return ports.AgentAuthStatusConfigured
		}
		return ports.AgentAuthStatusUnknown
	case "openai":
		if usableAiderSecret(environment.get("GITHUB_COPILOT_TOKEN")) {
			return ports.AgentAuthStatusConfigured
		}
	}
	if aiderHasProviderKey(provider, environment) {
		return ports.AgentAuthStatusConfigured
	}
	return ports.AgentAuthStatusUnknown
}

func aiderHasProviderKey(provider string, environment aiderEnvironment) bool {
	for _, name := range aiderProviderEnvVars[provider] {
		if usableAiderSecret(environment.get(name)) {
			return true
		}
	}
	generic := strings.ToUpper(provider) + "_API_KEY"
	return validAiderEnvName(generic) && usableAiderSecret(environment.get(generic))
}

func usableAiderSecret(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	switch strings.ToLower(value) {
	case "api key", "apikey", "your api key", "your-api-key", "your_api_key", "token", "your token", "your-token", "your_token", "changeme", "change-me", "replace-me", "replace_me":
		return false
	default:
		return true
	}
}

func validAiderURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Hostname() != ""
}

func firstAiderValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

var aiderProviderEnvVars = map[string][]string{
	"aleph_alpha":       {"ALEPH_ALPHA_API_KEY", "ALEPHALPHA_API_KEY"},
	"anthropic":         {"ANTHROPIC_API_KEY"},
	"anyscale":          {"ANYSCALE_API_KEY"},
	"azure":             {"AZURE_API_KEY", "AZURE_OPENAI_API_KEY"},
	"azure_ai":          {"AZURE_AI_API_KEY"},
	"baseten":           {"BASETEN_API_KEY"},
	"bytez":             {"BYTEZ_API_KEY"},
	"cerebras":          {"CEREBRAS_API_KEY"},
	"clarifai":          {"CLARIFAI_API_KEY"},
	"cloudflare":        {"CLOUDFLARE_API_KEY"},
	"codestral":         {"CODESTRAL_API_KEY"},
	"cohere":            {"CO_API_KEY", "COHERE_API_KEY"},
	"compactifai":       {"COMPACTIFAI_API_KEY"},
	"dashscope":         {"DASHSCOPE_API_KEY"},
	"databricks":        {"DATABRICKS_API_KEY"},
	"deepinfra":         {"DEEPINFRA_API_KEY"},
	"deepseek":          {"DEEPSEEK_API_KEY"},
	"featherless_ai":    {"FEATHERLESS_AI_API_KEY"},
	"fireworks_ai":      {"FIREWORKS_AI_API_KEY", "FIREWORKS_API_KEY", "FIREWORKSAI_API_KEY"},
	"gemini":            {"GEMINI_API_KEY", "GOOGLE_API_KEY", "PALM_API_KEY"},
	"groq":              {"GROQ_API_KEY"},
	"huggingface":       {"HUGGINGFACE_API_KEY"},
	"infinity":          {"INFINITY_API_KEY"},
	"maritalk":          {"MARITALK_API_KEY"},
	"mistral":           {"MISTRAL_API_KEY"},
	"moonshot":          {"MOONSHOT_API_KEY"},
	"nebius":            {"NEBIUS_API_KEY"},
	"nlp_cloud":         {"NLP_CLOUD_API_KEY"},
	"novita":            {"NOVITA_API_KEY"},
	"nvidia_nim":        {"NVIDIA_NIM_API_KEY"},
	"openai":            {"OPENAI_API_KEY"},
	"openai_like":       {"OPENAI_LIKE_API_KEY"},
	"openrouter":        {"OPENROUTER_API_KEY", "OR_API_KEY"},
	"ovhcloud":          {"OVHCLOUD_API_KEY"},
	"perplexity_ai":     {"PERPLEXITYAI_API_KEY"},
	"predibase":         {"PREDIBASE_API_KEY"},
	"provider":          {"PROVIDER_API_KEY"},
	"replicate":         {"REPLICATE_API_KEY"},
	"sambanova":         {"SAMBANOVA_API_KEY"},
	"together_ai":       {"TOGETHERAI_API_KEY"},
	"user":              {"USER_API_KEY"},
	"vercel_ai_gateway": {"VERCEL_AI_GATEWAY_API_KEY"},
	"volcengine":        {"ARK_API_KEY", "VOLCENGINE_API_KEY"},
	"voyage":            {"VOYAGE_API_KEY"},
	"wandb":             {"WANDB_API_KEY"},
	"watsonx":           {"WATSONX_API_KEY", "WX_API_KEY"},
	"xai":               {"XAI_API_KEY"},
	"xinference":        {"XINFERENCE_API_KEY"},
}
