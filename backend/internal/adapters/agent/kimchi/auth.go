package kimchi

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const kimchiDefaultEndpoint = "https://llm.kimchi.dev/openai/v1"

var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentScopedAuthChecker = (*Plugin)(nil)

func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.AuthStatusFor(ctx, ports.AgentAuthCheck{})
}

// AuthStatusFor checks only credentials relevant to the effective model.
func (p *Plugin) AuthStatusFor(ctx context.Context, check ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		if errors.Is(err, ports.ErrAgentBinaryNotFound) {
			return ports.AgentAuthStatusUnknown, nil
		}
		return ports.AgentAuthStatusUnknown, err
	}
	return kimchiAuthStatus(ctx, check, authutil.Dependencies{}, nil)
}

// kimchiAPIKeyProbe represents Kimchi's official Cast API-key validator. It is
// injected so local readiness checks never make an unsolicited provider call.
type kimchiAPIKeyProbe func(context.Context, string) (ports.AgentAuthStatus, error)

func kimchiAuthStatus(ctx context.Context, check ports.AgentAuthCheck, d authutil.Dependencies, probe kimchiAPIKeyProbe) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	d.Getenv = kimchiScopedGetenv(check.Env, d.Getenv)
	home := strings.TrimSpace(d.Getenv("HOME"))
	provider := kimchiSelectedProvider(check)
	if provider != "" && !isKimchiAuthProvider(provider) {
		return kimchiHarnessCredentialStatus(ctx, d, home, provider), ctx.Err()
	}

	key, endpoint := "", kimchiDefaultEndpoint
	if filepath.IsAbs(home) {
		kimchiApplyConfig(ctx, d, filepath.Join(home, ".config", "kimchi", "config.json"), &key, &endpoint)
	}
	if filepath.IsAbs(check.WorkingDir) {
		kimchiApplyConfig(ctx, d, filepath.Join(check.WorkingDir, ".kimchi", "config.json"), &key, &endpoint)
	}
	if environmentKey := strings.TrimSpace(d.Getenv("KIMCHI_API_KEY")); environmentKey != "" {
		key = environmentKey
	}
	normalizedEndpoint, valid := kimchiEndpoint(endpoint)
	if !valid {
		return ports.AgentAuthStatusUnknown, nil
	}
	if key == "" {
		selected := provider
		if selected == "" {
			selected = "kimchi-dev"
		}
		return kimchiHarnessCredentialStatus(ctx, d, home, selected), ctx.Err()
	}
	if normalizedEndpoint != kimchiDefaultEndpoint || probe == nil {
		return ports.AgentAuthStatusConfigured, nil
	}
	status, err := probe(ctx, key)
	if err != nil {
		if ctx.Err() != nil {
			return ports.AgentAuthStatusUnknown, ctx.Err()
		}
		return ports.AgentAuthStatusConfigured, nil
	}
	if status == ports.AgentAuthStatusAuthorized || status == ports.AgentAuthStatusUnauthorized {
		return status, nil
	}
	return ports.AgentAuthStatusConfigured, nil
}

func kimchiScopedGetenv(scoped map[string]string, base func(string) string) func(string) string {
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

func kimchiSelectedProvider(check ports.AgentAuthCheck) string {
	provider := ""
	model := strings.TrimSpace(check.Config.Model)
	args := check.Args
	if len(args) > 0 && (filepath.Base(args[0]) == "kimchi" || filepath.Base(args[0]) == "kimchi.exe") {
		args = args[1:]
	}
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		if strings.HasPrefix(args[i], "--provider=") {
			provider = strings.TrimSpace(strings.TrimPrefix(args[i], "--provider="))
		} else if args[i] == "--provider" && i+1 < len(args) {
			i++
			provider = strings.TrimSpace(args[i])
		} else if strings.HasPrefix(args[i], "--model=") {
			model = strings.TrimSpace(strings.TrimPrefix(args[i], "--model="))
		} else if args[i] == "--model" && i+1 < len(args) {
			i++
			model = strings.TrimSpace(args[i])
		}
	}
	if provider != "" {
		return provider
	}
	if slash := strings.IndexByte(model, '/'); slash > 0 {
		return strings.TrimSpace(model[:slash])
	}
	return ""
}

func isKimchiAuthProvider(provider string) bool {
	return provider == "kimchi-dev" || provider == "kimchi-experimental" || strings.HasPrefix(provider, "kimchi-dev/")
}

type kimchiConfigFile struct {
	APIKey      json.RawMessage `json:"apiKey"`
	LegacyKey   json.RawMessage `json:"api_key"`
	LLMEndpoint string          `json:"llmEndpoint"`
}

func kimchiApplyConfig(ctx context.Context, d authutil.Dependencies, path string, key, endpoint *string) {
	var config kimchiConfigFile
	if authutil.ReadJSON(ctx, d, path, &config) != nil {
		return
	}
	fields := map[string]json.RawMessage{"apiKey": config.APIKey, "api_key": config.LegacyKey}
	if value := kimchiExtractAPIKey(fields); value != "" {
		*key = value
	}
	if value := strings.TrimSpace(config.LLMEndpoint); value != "" {
		*endpoint = value
	}
}

func kimchiEndpoint(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = kimchiDefaultEndpoint
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", false
	}
	return strings.TrimRight(value, "/"), true
}

type kimchiAuthEntry struct {
	Type    string            `json:"type"`
	Key     *string           `json:"key"`
	Env     map[string]string `json:"env"`
	Access  *string           `json:"access"`
	Refresh *string           `json:"refresh"`
	Expires *float64          `json:"expires"`
}

func kimchiHarnessCredentialStatus(ctx context.Context, d authutil.Dependencies, home, provider string) ports.AgentAuthStatus {
	root := strings.TrimSpace(d.Getenv("KIMCHI_CODING_AGENT_DIR"))
	if root == "" && filepath.IsAbs(home) {
		root = filepath.Join(home, ".config", "kimchi", "harness")
	}
	if !filepath.IsAbs(root) || strings.TrimSpace(provider) == "" {
		return ports.AgentAuthStatusUnknown
	}
	var entries map[string]kimchiAuthEntry
	if authutil.ReadJSON(ctx, d, filepath.Join(root, "auth.json"), &entries) != nil {
		return ports.AgentAuthStatusUnknown
	}
	entry, ok := entries[provider]
	if !ok {
		return ports.AgentAuthStatusUnknown
	}
	switch entry.Type {
	case "api_key":
		if entry.Key != nil && kimchiResolvedValue(*entry.Key, entry.Env, d.Getenv) {
			return ports.AgentAuthStatusConfigured
		}
	case "oauth":
		if entry.Access == nil || entry.Refresh == nil || entry.Expires == nil || strings.TrimSpace(*entry.Access) == "" ||
			*entry.Expires <= 0 || *entry.Expires >= float64(math.MaxInt64) || math.IsNaN(*entry.Expires) || math.IsInf(*entry.Expires, 0) {
			return ports.AgentAuthStatusUnknown
		}
		return authutil.ExpiryEvidence(time.UnixMilli(int64(*entry.Expires)), strings.TrimSpace(*entry.Refresh) != "", timeNow(d)).Status
	}
	return ports.AgentAuthStatusUnknown
}

func kimchiResolvedValue(value string, local map[string]string, getenv func(string) string) bool {
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
		if !ok && getenv != nil {
			candidate = getenv(name)
		}
		if strings.TrimSpace(candidate) == "" {
			resolved = false
		}
		return candidate
	})
	return resolved && strings.TrimSpace(expanded) != ""
}

func timeNow(d authutil.Dependencies) time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func kimchiGlobalConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "kimchi", "config.json")
}

// kimchiConfigAuthStatus is retained for package callers that probe one file.
func kimchiConfigAuthStatus(ctx context.Context, configPath string) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if configPath == "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	data, err := os.ReadFile(configPath) //nolint:gosec // path is the caller-selected Kimchi config
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ports.AgentAuthStatusUnknown, nil
		}
		return ports.AgentAuthStatusUnknown, err
	}
	var config map[string]json.RawMessage
	if json.Unmarshal(data, &config) != nil {
		return ports.AgentAuthStatusUnknown, nil
	}
	if kimchiExtractAPIKey(config) != "" {
		return ports.AgentAuthStatusConfigured, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

func kimchiExtractAPIKey(config map[string]json.RawMessage) string {
	for _, field := range []string{"apiKey", "api_key"} {
		raw, ok := config[field]
		if !ok || len(raw) == 0 {
			continue
		}
		var key string
		if json.Unmarshal(raw, &key) == nil && strings.TrimSpace(key) != "" {
			return strings.TrimSpace(key)
		}
	}
	return ""
}
