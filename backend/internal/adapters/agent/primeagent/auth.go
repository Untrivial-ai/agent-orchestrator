package primeagent

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentScopedAuthChecker = (*Plugin)(nil)

// AuthStatus inspects local evidence, never launching Prime or validating a key.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.AuthStatusFor(ctx, ports.AgentAuthCheck{})
}

func (p *Plugin) AuthStatusFor(ctx context.Context, scope ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		if errors.Is(err, ports.ErrAgentBinaryNotFound) {
			return ports.AgentAuthStatusUnknown, nil
		}
		return ports.AgentAuthStatusUnknown, err
	}
	return primeAuthStatus(ctx, scope, authutil.Dependencies{})
}

type primeAuthEntry struct {
	Type    string  `json:"type"`
	Key     string  `json:"key"`
	Access  string  `json:"access"`
	Refresh *string `json:"refresh"`
	Expires *int64  `json:"expires"`
}
type primeModelProvider struct {
	APIKey  string `json:"apiKey"`
	BaseURL string `json:"baseUrl"`
	Models  []struct {
		ID string `json:"id"`
	} `json:"models"`
}

func primeAuthStatus(ctx context.Context, scope ports.AgentAuthCheck, d authutil.Dependencies) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	d.WorkingDir = scope.WorkingDir
	inherited := d.Getenv
	if inherited == nil {
		inherited = os.Getenv
	}
	d.Getenv = func(name string) string {
		if value, ok := scope.Env[name]; ok {
			return value
		}
		return inherited(name)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	home := d.Getenv("HOME")
	if home == "" {
		home = d.Getenv("USERPROFILE")
	}
	resolvePath := func(path string) string {
		if path == "~" {
			return home
		}
		if strings.HasPrefix(path, "~/") {
			return filepath.Join(home, path[2:])
		}
		if !filepath.IsAbs(path) && scope.WorkingDir != "" {
			return filepath.Join(scope.WorkingDir, path)
		}
		return path
	}
	dir := strings.TrimSpace(scope.Env[primeAgentCodingAgentDirEnv])
	if dir == "" && scope.DataDir != "" {
		dir, _ = primeDataDir(scope.DataDir)
	}
	if dir == "" {
		dir = strings.TrimSpace(d.Getenv(primeAgentCodingAgentDirEnv))
	}
	if dir == "" && d.Getenv("AO_DATA_DIR") != "" {
		dir, _ = primeDataDir(d.Getenv("AO_DATA_DIR"))
	}
	if dir == "" && home != "" {
		dir = filepath.Join(home, ".prime", "agent")
	}
	if dir != "" {
		dir = resolvePath(dir)
	}

	var settings struct {
		DefaultProvider string `json:"defaultProvider"`
		DefaultModel    string `json:"defaultModel"`
	}
	var entries map[string]json.RawMessage
	var models struct {
		Providers map[string]primeModelProvider `json:"providers"`
	}
	if dir != "" {
		if authutil.ReadJSON(ctx, d, filepath.Join(dir, "settings.json"), &settings) != nil {
			settings.DefaultProvider = ""
			settings.DefaultModel = ""
		}
		if authutil.ReadJSON(ctx, d, filepath.Join(dir, "auth.json"), &entries) != nil {
			entries = nil
		}
		if authutil.ReadJSON(ctx, d, filepath.Join(dir, "models.json"), &models) != nil {
			models.Providers = nil
		}
	}
	provider := settings.DefaultProvider
	model := strings.TrimSpace(scope.Config.Model)
	explicitModel := model != ""
	if model == "" {
		model = settings.DefaultModel
	}
	explicitProvider, runtimeKey := "", ""
	for i := 0; i < len(scope.Args); i++ {
		arg := scope.Args[i]
		if arg == "--" {
			break
		}
		name, value, inline := strings.Cut(arg, "=")
		switch name {
		case "--provider", "--model", "--api-key", "--cwd", "--mode", "--daemon-socket",
			"--system-prompt", "--append-system-prompt", "--fork", "--session-dir", "--models",
			"--tools", "-t", "--thinking", "--extension", "-e", "--skill", "--prompt-template", "--theme",
			"--autonomous-gate", "--autonomous-gate-retries", "--autonomous-gate-timeout-ms",
			"--autonomous-max-continuations", "--autonomous-max-turns", "--autonomous-max-tokens",
			"--autonomous-timeout-ms", "--goal", "--goal-token-budget", "--resume", "-r",
			"--print", "-p", "--export", "--list-models":
		default:
			continue
		}
		if !inline && i+1 < len(scope.Args) {
			next := scope.Args[i+1]
			// Native prompt values are arbitrary text, including auth-looking flags.
			consume := !strings.HasPrefix(next, "-")
			switch name {
			case "--system-prompt", "--append-system-prompt":
				consume = true
			case "--goal", "--autonomous-gate":
				consume = !strings.HasPrefix(next, "--")
			case "--resume", "-r", "--list-models":
				consume = consume && !strings.HasPrefix(next, "@")
			case "--print", "-p":
				consume = (consume || strings.HasPrefix(next, "---")) && !strings.HasPrefix(next, "@")
			}
			if consume && next != "--" {
				i++
				value = next
			}
		}
		switch name {
		case "--provider":
			explicitProvider = value
		case "--model":
			model = value
			explicitModel = true
		case "--api-key":
			runtimeKey = value
		}
	}
	// A launch model is not paired with the saved provider. Resolve it on its
	// own (or use an explicit provider), never borrow the saved provider's key.
	if explicitModel {
		provider = ""
	}
	if prefix, _, ok := strings.Cut(model, "/"); ok {
		if !primeKnownProvider(prefix, models.Providers) && explicitProvider == "" {
			return ports.AgentAuthStatusUnknown, nil
		}
		provider = prefix
	} else if model != "" && provider == "" && explicitProvider == "" {
		for id, item := range models.Providers {
			for _, candidate := range item.Models {
				if candidate.ID == model {
					if provider != "" && provider != id {
						return ports.AgentAuthStatusUnknown, nil
					}
					provider = id
				}
			}
		}
	}
	if explicitProvider != "" {
		provider = explicitProvider
	}
	if model != "" && provider == "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	if runtimeKey != "" && provider != "" && primeLiteralCredential(runtimeKey) {
		return ports.AgentAuthStatusConfigured, nil
	}
	resolve := func(provider string, selected bool) ports.AgentAuthStatus {
		if !primeKnownProvider(provider, models.Providers) {
			return ports.AgentAuthStatusUnknown
		}
		custom := models.Providers[provider]
		if custom.BaseURL != "" && !primeValidURL(custom.BaseURL) {
			return ports.AgentAuthStatusUnknown
		}
		if selected && provider == "amazon-bedrock" && d.Getenv("AWS_BEDROCK_SKIP_AUTH") == "1" {
			return ports.AgentAuthStatusNotApplicable
		}
		if provider == "ollama" && primeLocalURL(custom.BaseURL) {
			if selected {
				return ports.AgentAuthStatusNotApplicable
			}
			return ports.AgentAuthStatusUnknown
		}
		envStatus := func() ports.AgentAuthStatus {
			for _, key := range primeProviderEnv[provider] {
				if primeLiteralCredential(d.Getenv(key)) {
					return ports.AgentAuthStatusConfigured
				}
			}
			switch provider {
			case "amazon-bedrock":
				// Prime accepts this chain input without a role ARN. Validate the file.
				if path := d.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE"); path != "" {
					if data, err := authutil.ReadFile(ctx, d, resolvePath(path)); err == nil && strings.TrimSpace(string(data)) != "" {
						return ports.AgentAuthStatusConfigured
					}
				}
				return authutil.AWSEvidence(ctx, d).Status
			case "google-vertex":
				if (d.Getenv("GOOGLE_CLOUD_PROJECT") != "" || d.Getenv("GCLOUD_PROJECT") != "") && d.Getenv("GOOGLE_CLOUD_LOCATION") != "" {
					return authutil.GoogleADCEvidence(ctx, d).Status
				}
			}
			return ports.AgentAuthStatusUnknown
		}
		// Prime Inference prefers environment; other providers prefer auth.json.
		if provider == "prime-inference" {
			if status := envStatus(); status != ports.AgentAuthStatusUnknown {
				return status
			}
		}
		var entry primeAuthEntry
		if data := entries[provider]; data != nil && json.Unmarshal(data, &entry) == nil {
			if status := primeEntryStatus(entry, provider, d); status != ports.AgentAuthStatusUnknown {
				return status
			}
		}
		if status := envStatus(); status != ports.AgentAuthStatusUnknown {
			return status
		}
		if primeConfigCredential(custom.APIKey, d.Getenv) {
			return ports.AgentAuthStatusConfigured
		}
		return ports.AgentAuthStatusUnknown
	}
	if provider != "" {
		return resolve(provider, true), ctx.Err()
	}
	// A global check may report a usable provider, never one provider's expiry
	// as rejection of every provider, or an unselected no-auth provider.
	providers := make(map[string]bool)
	for id := range primeProviderEnv {
		providers[id] = true
	}
	for id := range entries {
		providers[id] = true
	}
	for id := range models.Providers {
		providers[id] = true
	}
	providers["amazon-bedrock"], providers["google-vertex"] = true, true
	for id := range providers {
		if resolve(id, false) == ports.AgentAuthStatusConfigured {
			return ports.AgentAuthStatusConfigured, ctx.Err()
		}
	}
	return ports.AgentAuthStatusUnknown, ctx.Err()
}

func primeEntryStatus(entry primeAuthEntry, provider string, d authutil.Dependencies) ports.AgentAuthStatus {
	switch entry.Type {
	case "api_key":
		if primeConfigCredential(entry.Key, d.Getenv) {
			return ports.AgentAuthStatusConfigured
		}
	case "oauth":
		if !primeOAuthProvider(provider) || !primeLiteralCredential(entry.Access) || entry.Refresh == nil || entry.Expires == nil || *entry.Expires <= 0 {
			return ports.AgentAuthStatusUnknown
		}
		return authutil.ExpiryEvidence(time.UnixMilli(*entry.Expires), primeLiteralCredential(*entry.Refresh), d.Now()).Status
	}
	return ports.AgentAuthStatusUnknown
}

var primeEnvReference = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// Commands and unresolved env names are not credential bytes. Prime permits
// literals too; AO conservatively rejects ambiguous uppercase identifiers.
func primeConfigCredential(value string, getenv func(string) string) bool {
	value = strings.TrimSpace(value)
	if !primeLiteralCredential(value) {
		return false
	}
	if resolved := getenv(value); resolved != "" {
		return primeLiteralCredential(resolved)
	}
	return !primeEnvReference.MatchString(value)
}
func primeLiteralCredential(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.HasPrefix(value, "!") && !strings.HasPrefix(value, "$")
}
func primeKnownProvider(provider string, models map[string]primeModelProvider) bool {
	_, builtin := primeProviderEnv[provider]
	_, custom := models[provider]
	return builtin || custom || primeOAuthProvider(provider) || provider == "amazon-bedrock"
}
func primeOAuthProvider(provider string) bool {
	switch provider {
	case "anthropic", "github-copilot", "openai-codex", "xai":
		return true
	}
	return false
}
func primeValidURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil
}
func primeLocalURL(value string) bool {
	if !primeValidURL(value) {
		return false
	}
	u, _ := url.Parse(value)
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// Source: Prime env-api-keys.ts (pinned in the task report).
var primeProviderEnv = map[string][]string{
	"anthropic":              {"ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY"},
	"github-copilot":         {"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"},
	"openai":                 {"OPENAI_API_KEY"},
	"azure-openai-responses": {"AZURE_OPENAI_API_KEY"},
	"prime-inference":        {"PRIME_API_KEY"},
	"deepseek":               {"DEEPSEEK_API_KEY"},
	"google":                 {"GEMINI_API_KEY"},
	"google-vertex":          {"GOOGLE_CLOUD_API_KEY"},
	"groq":                   {"GROQ_API_KEY"},
	"cerebras":               {"CEREBRAS_API_KEY"},
	"xai":                    {"XAI_API_KEY"},
	"openrouter":             {"OPENROUTER_API_KEY"},
	"vercel-ai-gateway":      {"AI_GATEWAY_API_KEY"},
	"zai":                    {"ZAI_API_KEY"},
	"mistral":                {"MISTRAL_API_KEY"},
	"minimax":                {"MINIMAX_API_KEY"},
	"minimax-cn":             {"MINIMAX_CN_API_KEY"},
	"moonshotai":             {"MOONSHOT_API_KEY"},
	"moonshotai-cn":          {"MOONSHOT_API_KEY"},
	"huggingface":            {"HF_TOKEN"},
	"fireworks":              {"FIREWORKS_API_KEY"},
	"opencode":               {"OPENCODE_API_KEY"},
	"opencode-go":            {"OPENCODE_API_KEY"},
	"kimi-coding":            {"KIMI_API_KEY"},
	"cloudflare-workers-ai":  {"CLOUDFLARE_API_KEY"},
	"cloudflare-ai-gateway":  {"CLOUDFLARE_API_KEY"},
	"xiaomi":                 {"XIAOMI_API_KEY"},
	"xiaomi-token-plan-cn":   {"XIAOMI_TOKEN_PLAN_CN_API_KEY"},
	"xiaomi-token-plan-ams":  {"XIAOMI_TOKEN_PLAN_AMS_API_KEY"},
	"xiaomi-token-plan-sgp":  {"XIAOMI_TOKEN_PLAN_SGP_API_KEY"},
}
