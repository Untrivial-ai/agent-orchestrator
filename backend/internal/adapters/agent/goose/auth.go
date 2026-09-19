package goose

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
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

type gooseThinkingPreservationFormat string

func (f *gooseThinkingPreservationFormat) UnmarshalJSON(data []byte) error {
	value, err := gooseEnumValue(data, "content_prepend", "content_xml", "reasoning_content")
	if err != nil {
		return err
	}
	*f = gooseThinkingPreservationFormat(value)
	return nil
}

type gooseProviderSetupCategory string

func (c *gooseProviderSetupCategory) UnmarshalJSON(data []byte) error {
	value, err := gooseEnumValue(data, "agent", "model")
	if err != nil {
		return err
	}
	*c = gooseProviderSetupCategory(value)
	return nil
}

type gooseProviderSetupMethod string

func (m *gooseProviderSetupMethod) UnmarshalJSON(data []byte) error {
	value, err := gooseEnumValue(data, "none", "single_api_key", "config_fields", "host_with_oauth_fallback", "oauth_browser", "oauth_device_code", "cloud_credentials", "local", "cli_auth")
	if err != nil {
		return err
	}
	*m = gooseProviderSetupMethod(value)
	return nil
}

type gooseProviderSetupGroup string

func (g *gooseProviderSetupGroup) UnmarshalJSON(data []byte) error {
	value, err := gooseEnumValue(data, "default", "additional")
	if err != nil {
		return err
	}
	*g = gooseProviderSetupGroup(value)
	return nil
}

func gooseEnumValue(data []byte, allowed ...string) (string, error) {
	var value string
	if json.Unmarshal(data, &value) != nil {
		return "", errors.New("invalid provider enum")
	}
	for _, candidate := range allowed {
		if value == candidate {
			return value, nil
		}
	}
	return "", errors.New("invalid provider enum")
}

type gooseProviderSetupMetadata struct {
	Category           gooseProviderSetupCategory `json:"category"`
	ACP                bool                       `json:"acp"`
	SetupMethod        gooseProviderSetupMethod   `json:"setup_method"`
	Group              gooseProviderSetupGroup    `json:"group"`
	DocsURL            *string                    `json:"docs_url"`
	Aliases            []*string                  `json:"aliases"`
	NativeConnectQuery *string                    `json:"native_connect_query"`
	BinaryName         *string                    `json:"binary_name"`
	SetupCapabilities  *struct {
		Install    *bool `json:"install"`
		Auth       *bool `json:"auth"`
		AuthStatus *bool `json:"auth_status"`
	} `json:"setup_capabilities"`
	ShowOnlyWhenInstalled bool `json:"show_only_when_installed"`
	FieldOverrides        []*struct {
		Key          *string `json:"key"`
		Label        *string `json:"label"`
		Placeholder  *string `json:"placeholder"`
		DefaultValue *string `json:"default_value"`
	} `json:"field_overrides"`
}

func (m *gooseProviderSetupMetadata) UnmarshalJSON(data []byte) error {
	type wire gooseProviderSetupMetadata
	var value wire
	if err := json.Unmarshal(data, &value); err != nil {
		return errors.New("invalid provider setup metadata")
	}
	if value.Category == "" || value.SetupMethod == "" || value.Group == "" {
		return errors.New("missing provider setup metadata")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return errors.New("invalid provider setup metadata")
	}
	allowed := map[string]bool{
		"category": true, "acp": true, "setup_method": true, "group": true,
		"docs_url": true, "aliases": true, "native_connect_query": true,
		"binary_name": true, "setup_capabilities": true,
		"show_only_when_installed": true, "field_overrides": true,
	}
	for name := range fields {
		if !allowed[name] {
			return errors.New("unknown provider setup field")
		}
	}
	if gooseHasNullField(fields, "acp", "aliases", "setup_capabilities", "show_only_when_installed", "field_overrides") {
		return errors.New("invalid null provider setup field")
	}
	for _, alias := range value.Aliases {
		if alias == nil {
			return errors.New("invalid provider setup alias")
		}
	}
	if value.SetupCapabilities != nil && (value.SetupCapabilities.Install == nil || value.SetupCapabilities.Auth == nil || value.SetupCapabilities.AuthStatus == nil) {
		return errors.New("invalid provider setup capabilities")
	}
	for _, field := range value.FieldOverrides {
		if field == nil || field.Key == nil || field.Label == nil {
			return errors.New("invalid provider setup field override")
		}
	}
	*m = gooseProviderSetupMetadata(value)
	return nil
}

type gooseProviderMetadata struct {
	Name         string  `json:"name"`
	DisplayName  *string `json:"display_name"`
	Engine       string  `json:"engine"`
	BaseURL      string  `json:"base_url"`
	Description  *string `json:"description"`
	APIKeyEnv    string  `json:"api_key_env"`
	RequiresAuth *bool   `json:"requires_auth"`
	Models       *[]struct {
		Name                       *string                          `json:"name"`
		ResolvedModel              *string                          `json:"resolved_model"`
		ContextLimit               *uint64                          `json:"context_limit"`
		InputTokenCost             *float64                         `json:"input_token_cost"`
		OutputTokenCost            *float64                         `json:"output_token_cost"`
		Currency                   *string                          `json:"currency"`
		SupportsCacheControl       *bool                            `json:"supports_cache_control"`
		Reasoning                  bool                             `json:"reasoning"`
		ThinkingPreservationFormat *gooseThinkingPreservationFormat `json:"thinking_preservation_format"`
		RequestParams              map[string]json.RawMessage       `json:"request_params"`
	} `json:"models"`
	Headers                 map[string]*string          `json:"headers"`
	TimeoutSeconds          *uint64                     `json:"timeout_seconds"`
	SupportsStreaming       *bool                       `json:"supports_streaming"`
	DynamicModels           *bool                       `json:"dynamic_models"`
	SessionIDHeaderOverride *string                     `json:"session_id_header_override"`
	CatalogProviderID       *string                     `json:"catalog_provider_id"`
	BasePath                *string                     `json:"base_path"`
	ModelDocLink            *string                     `json:"model_doc_link"`
	SetupSteps              []*string                   `json:"setup_steps"`
	SkipCanonicalFiltering  bool                        `json:"skip_canonical_filtering"`
	ToolShim                bool                        `json:"toolshim"`
	PreservesThinking       bool                        `json:"preserves_thinking"`
	EmitClearThinking       bool                        `json:"emit_clear_thinking"`
	Setup                   *gooseProviderSetupMetadata `json:"setup"`
	Auth                    *struct {
		Command         string    `json:"command"`
		Args            []*string `json:"args"`
		RefreshInterval *uint64   `json:"refresh_interval"`
		TimeoutSeconds  *uint64   `json:"timeout_seconds"`
		Cwd             *string   `json:"cwd"`
	} `json:"auth"`
	EnvVars []struct {
		Name        *string `json:"name"`
		Required    bool    `json:"required"`
		Secret      bool    `json:"secret"`
		Default     *string `json:"default"`
		Primary     *bool   `json:"primary"`
		Description *string `json:"description"`
	} `json:"env_vars"`
}

// Match serde's required fields and scalar types. Go's decoder otherwise
// accepts null for strings/numbers and cannot distinguish an absent slice
// from an explicitly null array. Only these declared schema fields are read.
func (m *gooseProviderMetadata) UnmarshalJSON(data []byte) error {
	type wire gooseProviderMetadata
	var value wire
	if err := json.Unmarshal(data, &value); err != nil {
		return errors.New("invalid provider metadata")
	}
	if value.DisplayName == nil || value.Models == nil {
		return errors.New("missing provider metadata")
	}
	for _, model := range *value.Models {
		if model.Name == nil {
			return errors.New("missing model name")
		}
	}
	for _, header := range value.Headers {
		if header == nil {
			return errors.New("invalid provider header")
		}
	}
	for _, step := range value.SetupSteps {
		if step == nil {
			return errors.New("invalid provider setup step")
		}
	}
	for _, field := range value.EnvVars {
		if field.Name == nil {
			return errors.New("missing provider environment name")
		}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return errors.New("invalid provider metadata")
	}
	if gooseHasNullField(fields, "api_key_env", "requires_auth", "setup_steps", "skip_canonical_filtering", "toolshim", "preserves_thinking", "emit_clear_thinking") {
		return errors.New("invalid null provider field")
	}
	for _, group := range []struct {
		name     string
		booleans []string
	}{
		{"models", []string{"reasoning"}},
		{"env_vars", []string{"required", "secret"}},
	} {
		if len(fields[group.name]) == 0 {
			continue
		}
		var entries []map[string]json.RawMessage
		if json.Unmarshal(fields[group.name], &entries) != nil {
			return errors.New("invalid provider metadata list")
		}
		for _, entry := range entries {
			if gooseHasNullField(entry, group.booleans...) {
				return errors.New("invalid null provider boolean")
			}
		}
	}
	if value.Auth != nil {
		var auth map[string]json.RawMessage
		if json.Unmarshal(fields["auth"], &auth) != nil || gooseHasNullField(auth, "command", "args", "refresh_interval") {
			return errors.New("invalid command credential metadata")
		}
		if _, exists := auth["command"]; !exists {
			return errors.New("missing credential command")
		}
		for _, arg := range value.Auth.Args {
			if arg == nil {
				return errors.New("invalid command credential argument")
			}
		}
	}
	*m = gooseProviderMetadata(value)
	return nil
}

func gooseHasNullField(fields map[string]json.RawMessage, names ...string) bool {
	for _, name := range names {
		if strings.TrimSpace(string(fields[name])) == "null" {
			return true
		}
	}
	return false
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
				raw, fallback := gooseKeyringResult(ctx, d)
				disabled = fallback
				if !fallback {
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
			if disabled && dir != "" {
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
		if metadata.Name != provider {
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
			placeholder := "${" + *field.Name + "}"
			if !strings.Contains(metadata.BaseURL, placeholder) {
				continue
			}
			value := param(*field.Name)
			if field.Secret {
				value = secret(*field.Name)
			}
			if value == "" && field.Default != nil {
				value = *field.Default
			}
			if value == "" && field.Required && field.Default == nil {
				return ports.AgentAuthStatusUnknown, nil
			}
			if value != "" || field.Default != nil {
				metadata.BaseURL = strings.ReplaceAll(metadata.BaseURL, placeholder, value)
			}
		}
		endpoint, err := url.Parse(metadata.BaseURL)
		if strings.Contains(metadata.BaseURL, "${") || err != nil || endpoint.Hostname() == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.User != nil {
			return ports.AgentAuthStatusUnknown, nil
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
			if authutil.ReadJSON(ctx, d, path, &token) == nil && strings.TrimSpace(token.Access) != "" && token.Refresh != nil && strings.TrimSpace(*token.Refresh) != "" {
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

// gooseKeyringResult separates a selected keyring (including an empty or
// malformed record) from the native missing/unavailable fallback conditions.
// The shared helper intentionally erases command errors, so retain only the
// numeric OS outcome before sanitization; never expose output/error text.
func gooseKeyringResult(ctx context.Context, d authutil.Dependencies) ([]byte, bool) {
	goos := d.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos != "darwin" {
		return nil, true
	}
	run := d.Run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := aoprocess.CommandContext(ctx, name, args...)
			output := &gooseKeyringOutput{}
			cmd.Stdout, cmd.Stderr = output, io.Discard
			cmd.WaitDelay = 100 * time.Millisecond
			err := cmd.Run()
			if output.exceeded {
				return nil, errors.New("credential output exceeds limit")
			}
			return output.data, err
		}
	}
	fallback := false
	d.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		out, err := run(ctx, name, args...)
		var exit interface{ ExitCode() int }
		if errors.As(err, &exit) {
			// security exits with OSStatus modulo 256: errSecItemNotFound
			// (-25300) and errSecNotAvailable (-25291), respectively.
			fallback = exit.ExitCode() == 44 || exit.ExitCode() == 37
		}
		return out, err
	}
	out, err := authutil.GenericPassword(ctx, d, "goose", "secrets")
	if err == nil {
		return out, false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, false
	}
	return nil, fallback
}

type gooseKeyringOutput struct {
	data     []byte
	exceeded bool
}

func (b *gooseKeyringOutput) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := authutil.MaxFileSize - len(b.data); n > remaining {
		b.data = append(b.data, p[:remaining]...)
		b.exceeded = true
	} else {
		b.data = append(b.data, p...)
	}
	return n, nil
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
	Access  string  `json:"access_token"`
	Refresh *string `json:"refresh_token"`
	Expires string  `json:"expires_at"`
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
	// These providers require a string refresh_token in their native schema.
	// Missing/null is malformed; an explicitly empty string is a valid record
	// with no refresh path. Databricks handles its optional field separately.
	if strings.TrimSpace(token.Access) == "" || token.Refresh == nil {
		return ports.AgentAuthStatusUnknown
	}
	expiry, err := time.Parse(time.RFC3339, token.Expires)
	if err != nil {
		return ports.AgentAuthStatusUnknown
	}
	return authutil.ExpiryEvidence(expiry, strings.TrimSpace(*token.Refresh) != "", now).Status
}
