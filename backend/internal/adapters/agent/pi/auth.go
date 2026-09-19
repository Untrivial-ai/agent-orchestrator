package pi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentScopedAuthChecker = (*Plugin)(nil)

// AuthStatus checks device-wide defaults using the scoped resolver.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.AuthStatusFor(ctx, ports.AgentAuthCheck{})
}

// AuthStatusFor checks credentials for the effective Pi invocation.
func (p *Plugin) AuthStatusFor(ctx context.Context, check ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	binary, err := p.ResolveBinary(ctx)
	if err != nil {
		if errors.Is(err, ports.ErrAgentBinaryNotFound) {
			return ports.AgentAuthStatusUnknown, nil
		}
		return ports.AgentAuthStatusUnknown, err
	}
	return piAuthStatus(ctx, binary, check, authutil.Dependencies{})
}

func piAuthStatus(ctx context.Context, binary string, check ports.AgentAuthCheck, d authutil.Dependencies) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	d.WorkingDir = check.WorkingDir
	d.Getenv = piScopedGetenv(check.Env, d.Getenv)
	provider, scoped := piSelectedProvider(check)
	if status := piSelectedNoAuthStatus(ctx, provider, d); status == ports.AgentAuthStatusNotApplicable {
		return status, nil
	}
	if provider != "" && binary != "" {
		status, localFallback, err := piNativeAuthStatus(ctx, binary, provider, check, d)
		if err != nil || !localFallback {
			return status, err
		}
	}
	if scoped && provider == "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	return piLocalProviderStatus(ctx, provider, d), ctx.Err()
}

func piSelectedNoAuthStatus(ctx context.Context, provider string, d authutil.Dependencies) ports.AgentAuthStatus {
	if provider == "llama.cpp" {
		return ports.AgentAuthStatusNotApplicable
	}
	root, ok := piConfigDirWith(d.Getenv)
	if !ok {
		return ports.AgentAuthStatusUnknown
	}
	var models piModelsFile
	if authutil.ReadJSON(ctx, d, filepath.Join(root, "models.json"), &models) != nil {
		return ports.AgentAuthStatusUnknown
	}
	config, ok := models.Providers[provider]
	if !ok || !piModelsProviderValid(provider, config) {
		return ports.AgentAuthStatusUnknown
	}
	if piLoopbackURL(config.BaseURL) {
		return ports.AgentAuthStatusNotApplicable
	}
	return ports.AgentAuthStatusUnknown
}

func piScopedGetenv(scoped map[string]string, base func(string) string) func(string) string {
	if base == nil {
		base = os.Getenv
	}
	return func(key string) string {
		if value, ok := scoped[key]; ok {
			return strings.TrimSpace(value)
		}
		return strings.TrimSpace(base(key))
	}
}

func piSelectedProvider(check ports.AgentAuthCheck) (string, bool) {
	provider := ""
	model := strings.TrimSpace(check.Config.Model)
	scoped := model != ""
	args := check.Args
	if len(args) > 0 && (filepath.Base(args[0]) == "pi" || filepath.Base(args[0]) == "pi.exe") {
		args = args[1:]
	}
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		switch {
		case strings.HasPrefix(args[i], "--provider="):
			scoped = true
			provider = strings.TrimSpace(strings.TrimPrefix(args[i], "--provider="))
		case args[i] == "--provider" && i+1 < len(args):
			scoped = true
			i++
			provider = strings.TrimSpace(args[i])
		case strings.HasPrefix(args[i], "--model="):
			scoped = true
			model = strings.TrimSpace(strings.TrimPrefix(args[i], "--model="))
		case args[i] == "--model" && i+1 < len(args):
			scoped = true
			i++
			model = strings.TrimSpace(args[i])
		}
	}
	if provider != "" {
		return provider, true
	}
	if slash := strings.IndexByte(model, '/'); slash > 0 {
		return strings.TrimSpace(model[:slash]), true
	}
	return "", scoped
}

func piNativeAuthStatus(ctx context.Context, binary, provider string, check ports.AgentAuthCheck, d authutil.Dependencies) (ports.AgentAuthStatus, bool, error) {
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	run := d.Run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := aoprocess.CommandContext(ctx, name, args...)
			cmd.Dir = check.WorkingDir
			cmd.Env = os.Environ()
			for key, value := range check.Env {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
			output := &piProbeOutput{}
			cmd.Stdout = output
			cmd.Stderr = io.Discard
			cmd.WaitDelay = 100 * time.Millisecond
			err := cmd.Run()
			if output.exceeded {
				return nil, piBoundedOutputError{}
			}
			return output.data, err
		}
	}
	out, err := run(probeCtx, binary, "auth", "check", "--provider", provider)
	if ctx.Err() != nil {
		return ports.AgentAuthStatusUnknown, false, ctx.Err()
	}
	if probeCtx.Err() != nil || len(out) > authutil.MaxFileSize {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	if piBoundedOutputExceeded(err) {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	output := strings.TrimSpace(string(out))
	if output == "ready" && err == nil {
		return ports.AgentAuthStatusAuthorized, false, nil
	}
	if output == "not_ready" && piExitCode(err) == 1 {
		return ports.AgentAuthStatusUnauthorized, false, nil
	}
	localFallback := output == "" && err != nil && piExitCode(err) == -1
	return ports.AgentAuthStatusUnknown, localFallback, nil
}

func piExitCode(err error) int {
	if err == nil {
		return 0
	}
	type exitCoder interface{ ExitCode() int }
	var exit exitCoder
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

type piProbeOutput struct {
	data     []byte
	exceeded bool
}

type piBoundedOutputError struct{}

func (piBoundedOutputError) Error() string               { return "Pi auth output exceeds limit" }
func (piBoundedOutputError) BoundedOutputExceeded() bool { return true }

func piBoundedOutputExceeded(err error) bool {
	type boundedOutputError interface {
		BoundedOutputExceeded() bool
	}
	var bounded boundedOutputError
	return errors.As(err, &bounded) && bounded.BoundedOutputExceeded()
}

func (b *piProbeOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := authutil.MaxFileSize - len(b.data)
	if len(p) > remaining {
		b.data = append(b.data, p[:remaining]...)
		b.exceeded = true
	} else {
		b.data = append(b.data, p...)
	}
	return n, nil
}

var piProviderEnv = map[string][]string{
	"anthropic":                  {"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY"},
	"ant-ling":                   {"ANT_LING_API_KEY"},
	"openai":                     {"OPENAI_API_KEY"},
	"azure-openai-responses":     {"AZURE_OPENAI_API_KEY"},
	"deepseek":                   {"DEEPSEEK_API_KEY"},
	"nvidia":                     {"NVIDIA_API_KEY"},
	"google":                     {"GEMINI_API_KEY"},
	"groq":                       {"GROQ_API_KEY"},
	"cerebras":                   {"CEREBRAS_API_KEY"},
	"xai":                        {"XAI_API_KEY"},
	"fireworks":                  {"FIREWORKS_API_KEY"},
	"together":                   {"TOGETHER_API_KEY"},
	"baseten":                    {"BASETEN_API_KEY"},
	"openrouter":                 {"OPENROUTER_API_KEY"},
	"vercel-ai-gateway":          {"AI_GATEWAY_API_KEY"},
	"zai":                        {"ZAI_API_KEY"},
	"zai-coding-cn":              {"ZAI_CODING_CN_API_KEY"},
	"mistral":                    {"MISTRAL_API_KEY"},
	"minimax":                    {"MINIMAX_API_KEY"},
	"minimax-cn":                 {"MINIMAX_CN_API_KEY"},
	"moonshotai":                 {"MOONSHOT_API_KEY"},
	"moonshotai-cn":              {"MOONSHOT_API_KEY"},
	"huggingface":                {"HF_TOKEN"},
	"opencode":                   {"OPENCODE_API_KEY"},
	"opencode-go":                {"OPENCODE_API_KEY"},
	"kimi-coding":                {"KIMI_API_KEY"},
	"qwen-token-plan":            {"QWEN_TOKEN_PLAN_API_KEY"},
	"qwen-token-plan-individual": {"QWEN_TOKEN_PLAN_API_KEY"},
	"qwen-token-plan-cn":         {"QWEN_TOKEN_PLAN_CN_API_KEY"},
	"xiaomi":                     {"XIAOMI_API_KEY"},
	"xiaomi-token-plan-cn":       {"XIAOMI_TOKEN_PLAN_CN_API_KEY"},
	"xiaomi-token-plan-ams":      {"XIAOMI_TOKEN_PLAN_AMS_API_KEY"},
	"xiaomi-token-plan-sgp":      {"XIAOMI_TOKEN_PLAN_SGP_API_KEY"},
	"github-copilot":             {"COPILOT_GITHUB_TOKEN"},
	"radius":                     {"RADIUS_API_KEY"},
	"cloudflare-workers-ai":      {"CLOUDFLARE_API_KEY"},
	"cloudflare-ai-gateway":      {"CLOUDFLARE_API_KEY"},
}

func piLocalProviderStatus(ctx context.Context, provider string, d authutil.Dependencies) ports.AgentAuthStatus {
	if d.Getenv == nil {
		d.Getenv = os.Getenv
	}
	root, ok := piConfigDirWith(d.Getenv)
	if !ok {
		return ports.AgentAuthStatusUnknown
	}
	entries, valid := piReadAuthEntries(ctx, d, filepath.Join(root, "auth.json"))
	if provider != "" {
		if valid {
			if entry, exists := entries[provider]; exists {
				status := piEntryStatus(entry, provider, d)
				if status != ports.AgentAuthStatusUnknown {
					return status
				}
			}
		}
		if piProviderEnvironmentStatus(provider, d.Getenv) == ports.AgentAuthStatusConfigured {
			return ports.AgentAuthStatusConfigured
		}
		switch provider {
		case "amazon-bedrock":
			return authutil.AWSEvidence(ctx, d).Status
		case "google-vertex":
			if strings.TrimSpace(d.Getenv("GOOGLE_CLOUD_API_KEY")) != "" {
				return ports.AgentAuthStatusConfigured
			}
			if strings.TrimSpace(d.Getenv("GOOGLE_CLOUD_PROJECT")) == "" && strings.TrimSpace(d.Getenv("GCLOUD_PROJECT")) == "" {
				return ports.AgentAuthStatusUnknown
			}
			if strings.TrimSpace(d.Getenv("GOOGLE_CLOUD_LOCATION")) == "" {
				return ports.AgentAuthStatusUnknown
			}
			return authutil.GoogleADCEvidence(ctx, d).Status
		}
		return piModelsProviderStatus(ctx, d, filepath.Join(root, "models.json"), provider)
	}

	for candidate := range piProviderEnv {
		if piProviderEnvironmentStatus(candidate, d.Getenv) == ports.AgentAuthStatusConfigured {
			return ports.AgentAuthStatusConfigured
		}
	}
	if authutil.AWSEvidence(ctx, d).Status == ports.AgentAuthStatusConfigured {
		return ports.AgentAuthStatusConfigured
	}
	if strings.TrimSpace(d.Getenv("GOOGLE_CLOUD_LOCATION")) != "" &&
		(strings.TrimSpace(d.Getenv("GOOGLE_CLOUD_PROJECT")) != "" || strings.TrimSpace(d.Getenv("GCLOUD_PROJECT")) != "") &&
		authutil.GoogleADCEvidence(ctx, d).Status == ports.AgentAuthStatusConfigured {
		return ports.AgentAuthStatusConfigured
	}
	if valid {
		for candidate, entry := range entries {
			if piEntryStatus(entry, candidate, d) == ports.AgentAuthStatusConfigured {
				return ports.AgentAuthStatusConfigured
			}
		}
	}
	return ports.AgentAuthStatusUnknown
}

func piProviderEnvironmentStatus(provider string, getenv func(string) string) ports.AgentAuthStatus {
	variables := piProviderEnv[provider]
	found := false
	for _, variable := range variables {
		if strings.TrimSpace(getenv(variable)) != "" {
			found = true
			break
		}
	}
	if !found {
		return ports.AgentAuthStatusUnknown
	}
	switch provider {
	case "cloudflare-workers-ai":
		if strings.TrimSpace(getenv("CLOUDFLARE_ACCOUNT_ID")) == "" {
			return ports.AgentAuthStatusUnknown
		}
	case "cloudflare-ai-gateway":
		if strings.TrimSpace(getenv("CLOUDFLARE_ACCOUNT_ID")) == "" || strings.TrimSpace(getenv("CLOUDFLARE_GATEWAY_ID")) == "" {
			return ports.AgentAuthStatusUnknown
		}
	}
	return ports.AgentAuthStatusConfigured
}

type piAuthEntry struct {
	Type    string            `json:"type"`
	Key     *string           `json:"key"`
	Env     map[string]string `json:"env"`
	Access  *string           `json:"access"`
	Refresh *string           `json:"refresh"`
	Expires *float64          `json:"expires"`
}

func piReadAuthEntries(ctx context.Context, d authutil.Dependencies, path string) (map[string]piAuthEntry, bool) {
	var raw map[string]json.RawMessage
	if authutil.ReadJSON(ctx, d, path, &raw) != nil {
		return nil, false
	}
	entries := make(map[string]piAuthEntry, len(raw))
	for provider, encoded := range raw {
		if strings.TrimSpace(provider) == "" {
			return nil, false
		}
		var entry piAuthEntry
		if json.Unmarshal(encoded, &entry) != nil {
			return nil, false
		}
		switch entry.Type {
		case "api_key":
			if entry.Access != nil || entry.Refresh != nil || entry.Expires != nil {
				return nil, false
			}
		case "oauth":
			if entry.Access == nil || entry.Refresh == nil || entry.Expires == nil ||
				*entry.Expires <= 0 || *entry.Expires >= float64(math.MaxInt64) || math.IsNaN(*entry.Expires) || math.IsInf(*entry.Expires, 0) {
				return nil, false
			}
		default:
			return nil, false
		}
		entries[provider] = entry
	}
	return entries, true
}

func piEntryStatus(entry piAuthEntry, provider string, d authutil.Dependencies) ports.AgentAuthStatus {
	switch entry.Type {
	case "oauth":
		if entry.Access == nil || entry.Refresh == nil || entry.Expires == nil || strings.TrimSpace(*entry.Access) == "" ||
			*entry.Expires <= 0 || *entry.Expires >= float64(math.MaxInt64) || math.IsNaN(*entry.Expires) || math.IsInf(*entry.Expires, 0) {
			return ports.AgentAuthStatusUnknown
		}
		return authutil.ExpiryEvidence(time.UnixMilli(int64(*entry.Expires)), strings.TrimSpace(*entry.Refresh) != "", piNow(d)).Status
	case "api_key":
		if entry.Key != nil && piResolvedValue(*entry.Key, entry.Env, d.Getenv) {
			return ports.AgentAuthStatusConfigured
		}
		getenv := func(key string) string {
			if value, ok := entry.Env[key]; ok {
				return strings.TrimSpace(value)
			}
			return d.Getenv(key)
		}
		return piProviderEnvironmentStatus(provider, getenv)
	default:
		return ports.AgentAuthStatusUnknown
	}
}

func piResolvedValue(value string, local map[string]string, getenv func(string) string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "!") {
		return false
	}
	resolved := true
	expanded := os.Expand(value, func(name string) string {
		if name == "$" || name == "!" {
			return name
		}
		candidate, ok := local[name]
		if !ok {
			candidate = getenv(name)
		}
		if strings.TrimSpace(candidate) == "" {
			resolved = false
		}
		return candidate
	})
	return resolved && strings.TrimSpace(expanded) != ""
}

type piModelsFile struct {
	Providers map[string]piModelsProvider `json:"providers"`
}

// Only omission may leave this empty. JSON calls this decoder for every
// present value, including null, so invalid fields cannot inherit an API.
type piModelsAPI string

func (api *piModelsAPI) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if !piAPIIdentifier(value) {
		return errors.New("api must be a nonempty identifier")
	}
	*api = piModelsAPI(value)
	return nil
}

type piModelsProvider struct {
	BaseURL string          `json:"baseUrl"`
	API     piModelsAPI     `json:"api"`
	APIKey  *string         `json:"apiKey"`
	Models  []piModelsModel `json:"models"`
}

type piModelsModel struct {
	ID  string      `json:"id"`
	API piModelsAPI `json:"api"`
}

func piModelsProviderStatus(ctx context.Context, d authutil.Dependencies, path, provider string) ports.AgentAuthStatus {
	var models piModelsFile
	if authutil.ReadJSON(ctx, d, path, &models) != nil {
		return ports.AgentAuthStatusUnknown
	}
	config, ok := models.Providers[provider]
	if !ok || !piModelsProviderValid(provider, config) {
		return ports.AgentAuthStatusUnknown
	}
	if piLoopbackURL(config.BaseURL) {
		return ports.AgentAuthStatusNotApplicable
	}
	if _, valid := piRemoteURL(config.BaseURL); !valid {
		return ports.AgentAuthStatusUnknown
	}
	if config.APIKey != nil && piResolvedValue(*config.APIKey, nil, d.Getenv) {
		return ports.AgentAuthStatusConfigured
	}
	return ports.AgentAuthStatusUnknown
}

func piModelsProviderValid(provider string, config piModelsProvider) bool {
	if len(config.Models) == 0 {
		return false
	}
	_, builtIn := piProviderEnv[provider]
	builtIn = builtIn || provider == "amazon-bedrock" || provider == "google-vertex" || provider == "openai-codex"
	for _, model := range config.Models {
		if strings.TrimSpace(model.ID) == "" {
			return false
		}
		api := string(model.API)
		if api == "" {
			api = string(config.API)
		}
		if api == "" {
			if builtIn {
				continue
			}
			return false
		}
		if !piAPIIdentifier(api) {
			return false
		}
	}
	return true
}

func piAPIIdentifier(api string) bool {
	if api == "" {
		return false
	}
	for _, character := range api {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func piRemoteURL(value string) (*url.URL, bool) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, false
	}
	return parsed, true
}

func piLoopbackURL(value string) bool {
	parsed, ok := piRemoteURL(value)
	if !ok {
		return false
	}
	host := strings.TrimSpace(parsed.Hostname())
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func piNow(d authutil.Dependencies) time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func piConfigDirWith(getenv func(string) string) (string, bool) {
	if configDir := strings.TrimSpace(getenv("PI_CODING_AGENT_DIR")); filepath.IsAbs(configDir) {
		return configDir, true
	}
	home := strings.TrimSpace(getenv("HOME"))
	if !filepath.IsAbs(home) {
		return "", false
	}
	return filepath.Join(home, ".pi", "agent"), true
}

func piLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	status := piLocalProviderStatus(ctx, "", authutil.Dependencies{})
	return status, status != ports.AgentAuthStatusUnknown, ctx.Err()
}

func piAuthJSONStatus(path string) (ports.AgentAuthStatus, bool, error) {
	if strings.TrimSpace(path) == "" {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // compatibility helper for a caller-selected auth file
	if os.IsNotExist(err) {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	var raw map[string]json.RawMessage
	if valid := json.Unmarshal(data, &raw) == nil; !valid {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	for provider, encoded := range raw {
		var entry piAuthEntry
		if json.Unmarshal(encoded, &entry) != nil {
			continue
		}
		if piEntryStatus(entry, provider, authutil.Dependencies{Getenv: os.Getenv}) == ports.AgentAuthStatusConfigured {
			return ports.AgentAuthStatusConfigured, true, nil
		}
	}
	return ports.AgentAuthStatusUnknown, false, nil
}
