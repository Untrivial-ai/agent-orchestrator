package droid

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

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
	return droidAuthStatus(ctx, scope, authutil.Dependencies{})
}

type droidAuthModel struct {
	ID           string            `json:"id"`
	Model        string            `json:"model"`
	DisplayName  string            `json:"displayName"`
	BaseURL      string            `json:"baseUrl"`
	APIKey       string            `json:"apiKey"`
	APIKeyHelper string            `json:"apiKeyHelper"`
	Provider     string            `json:"provider"`
	ExtraHeaders map[string]string `json:"extraHeaders"`
	Bedrock      *droidBedrock     `json:"bedrock"`
	legacy       bool
}

type droidBedrock struct {
	AWSRegion           string            `json:"awsRegion"`
	AWSProfile          string            `json:"awsProfile"`
	BedrockBaseURL      string            `json:"bedrockBaseUrl"`
	AWSAuthRefresh      string            `json:"awsAuthRefresh"`
	AWSCredentialExport string            `json:"awsCredentialExport"`
	RequestMetadata     map[string]string `json:"requestMetadata"`
}

type droidAuthConfig struct {
	model        string
	customModels map[string]droidAuthModel
	order        []string
}

type droidSettingsLayer struct {
	Model                  *string `json:"model"`
	SessionDefaultSettings *struct {
		Model *string `json:"model"`
	} `json:"sessionDefaultSettings"`
	CustomModels []droidAuthModel `json:"customModels"`
}

type droidLegacyModel struct {
	ID           string            `json:"id"`
	Model        string            `json:"model"`
	DisplayName  string            `json:"display_name"`
	BaseURL      string            `json:"base_url"`
	APIKey       string            `json:"api_key"`
	APIKeyHelper string            `json:"api_key_helper"`
	Provider     string            `json:"provider"`
	ExtraHeaders map[string]string `json:"extra_headers"`
	Bedrock      *droidBedrock     `json:"bedrock"`
}

type droidLegacyLayer struct {
	Model        *string            `json:"model"`
	CustomModels []droidLegacyModel `json:"custom_models"`
}

func droidAuthStatus(ctx context.Context, scope ports.AgentAuthCheck, d authutil.Dependencies) (ports.AgentAuthStatus, error) {
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
	home := d.Getenv("HOME")
	if d.GOOS == "windows" {
		home = d.Getenv("USERPROFILE")
	}
	factoryDir := ""
	if home != "" {
		factoryDir = filepath.Join(home, ".factory")
	}
	cfg := droidAuthConfig{customModels: make(map[string]droidAuthModel)}
	if factoryDir != "" {
		if ok, invalid := cfg.readLegacy(ctx, d, filepath.Join(factoryDir, "config.json")); invalid {
			return ports.AgentAuthStatusUnknown, nil
		} else if !ok && ctx.Err() != nil {
			return ports.AgentAuthStatusUnknown, ctx.Err()
		}
		for _, name := range []string{"settings.json", "settings.local.json"} {
			if invalid := cfg.readSettings(ctx, d, filepath.Join(factoryDir, name)); invalid {
				return ports.AgentAuthStatusUnknown, nil
			}
		}
	}
	if filepath.IsAbs(scope.WorkingDir) {
		for _, name := range []string{"settings.json", "settings.local.json"} {
			if invalid := cfg.readSettings(ctx, d, filepath.Join(scope.WorkingDir, ".factory", name)); invalid {
				return ports.AgentAuthStatusUnknown, nil
			}
		}
	}
	if path := droidArgValue(scope.Args, "--settings"); path != "" {
		path = droidResolvePath(scope.WorkingDir, path)
		if invalid := cfg.readSettings(ctx, d, path); invalid {
			return ports.AgentAuthStatusUnknown, nil
		}
	}
	if model := strings.TrimSpace(scope.Config.Model); model != "" {
		cfg.model = model
	}

	if strings.HasPrefix(cfg.model, "custom:") {
		model, ok := cfg.selectedCustomModel()
		if !ok {
			return ports.AgentAuthStatusUnknown, nil
		}
		return droidCustomModelEvidence(ctx, d, model).Status, ctx.Err()
	}
	if strings.TrimSpace(d.Getenv("FACTORY_API_KEY")) != "" {
		return ports.AgentAuthStatusConfigured, nil
	}
	if factoryDir != "" && droidBrowserLoginConfigured(ctx, d, factoryDir) {
		return ports.AgentAuthStatusConfigured, nil
	}
	return ports.AgentAuthStatusUnknown, ctx.Err()
}

func (c *droidAuthConfig) readLegacy(ctx context.Context, d authutil.Dependencies, path string) (bool, bool) {
	data, present, invalid := droidReadOptional(ctx, d, path)
	if !present || invalid {
		return present, invalid
	}
	var fields map[string]json.RawMessage
	var layer droidLegacyLayer
	if json.Unmarshal(data, &fields) != nil || fields == nil || json.Unmarshal(data, &layer) != nil || droidNullField(fields, "model", "custom_models") {
		return true, true
	}
	if layer.Model != nil {
		c.model = strings.TrimSpace(*layer.Model)
	}
	for _, legacy := range layer.CustomModels {
		c.mergeModel(droidAuthModel{
			ID: legacy.ID, Model: legacy.Model, DisplayName: legacy.DisplayName,
			BaseURL: legacy.BaseURL, APIKey: legacy.APIKey, APIKeyHelper: legacy.APIKeyHelper,
			Provider: legacy.Provider, ExtraHeaders: legacy.ExtraHeaders, Bedrock: legacy.Bedrock,
			legacy: true,
		})
	}
	return true, false
}

func (c *droidAuthConfig) readSettings(ctx context.Context, d authutil.Dependencies, path string) bool {
	data, present, invalid := droidReadOptional(ctx, d, path)
	if !present || invalid {
		return invalid
	}
	var fields map[string]json.RawMessage
	var layer droidSettingsLayer
	if json.Unmarshal(data, &fields) != nil || fields == nil || json.Unmarshal(data, &layer) != nil || droidNullField(fields, "model", "sessionDefaultSettings", "customModels") {
		return true
	}
	if layer.Model != nil {
		c.model = strings.TrimSpace(*layer.Model)
	}
	if layer.SessionDefaultSettings != nil && layer.SessionDefaultSettings.Model != nil {
		c.model = strings.TrimSpace(*layer.SessionDefaultSettings.Model)
	}
	for _, model := range layer.CustomModels {
		c.mergeModel(model)
	}
	return false
}

func (c *droidAuthConfig) mergeModel(model droidAuthModel) {
	key := strings.TrimSpace(model.ID)
	if key == "" {
		key = strings.TrimSpace(model.Model)
	}
	if key == "" {
		return
	}
	if _, exists := c.customModels[key]; !exists {
		c.order = append(c.order, key)
	}
	c.customModels[key] = model
}

func (c droidAuthConfig) selectedCustomModel() (droidAuthModel, bool) {
	selected := strings.TrimSpace(c.model)
	if model, ok := c.customModels[selected]; ok {
		return model, true
	}
	for index, key := range c.order {
		model := c.customModels[key]
		aliases := []string{
			strings.TrimSpace(model.Model), strings.TrimSpace(model.DisplayName),
			"custom:" + strings.TrimSpace(model.Model), "custom:" + strings.TrimSpace(model.DisplayName),
		}
		if model.DisplayName != "" {
			aliases = append(aliases, "custom:"+strings.TrimSpace(model.DisplayName)+"-"+strconv.Itoa(index))
		}
		for _, alias := range aliases {
			if alias != "" && alias != "custom:" && selected == alias {
				return model, true
			}
		}
	}
	return droidAuthModel{}, false
}

func droidCustomModelEvidence(ctx context.Context, d authutil.Dependencies, model droidAuthModel) authutil.Evidence {
	unknown := authutil.Evidence{Status: ports.AgentAuthStatusUnknown}
	provider := strings.TrimSpace(model.Provider)
	if strings.TrimSpace(model.Model) == "" {
		return unknown
	}
	if model.Bedrock != nil {
		if provider != "anthropic" && provider != "openai" && provider != "bedrock-converse" {
			return unknown
		}
		if strings.TrimSpace(model.Bedrock.BedrockBaseURL) != "" && !droidValidHTTPURL(droidModelValue(model, model.Bedrock.BedrockBaseURL, d.Getenv)) {
			return unknown
		}
		cloud := d
		baseEnv := cloud.Getenv
		cloud.Getenv = func(key string) string {
			switch key {
			case "AWS_PROFILE":
				if value := droidModelValue(model, model.Bedrock.AWSProfile, baseEnv); value != "" {
					return value
				}
			case "AWS_REGION":
				if value := droidModelValue(model, model.Bedrock.AWSRegion, baseEnv); value != "" {
					return value
				}
			}
			return baseEnv(key)
		}
		return authutil.AWSEvidence(ctx, cloud)
	}
	if provider != "anthropic" && provider != "openai" && provider != "generic-chat-completion-api" {
		return unknown
	}
	if !droidValidHTTPURL(droidModelValue(model, model.BaseURL, d.Getenv)) {
		return unknown
	}
	if strings.TrimSpace(model.APIKey) != "" {
		if droidModelValue(model, model.APIKey, d.Getenv) != "" {
			return authutil.Evidence{Status: ports.AgentAuthStatusConfigured, Source: "api-key"}
		}
		return unknown
	}
	hasHeader := false
	for name, value := range model.ExtraHeaders {
		if strings.TrimSpace(name) == "" {
			continue
		}
		hasHeader = true
		if droidModelValue(model, value, d.Getenv) != "" {
			return authutil.Evidence{Status: ports.AgentAuthStatusConfigured, Source: "static-header"}
		}
	}
	if hasHeader {
		return unknown
	}
	return authutil.NoAuthEvidence(true)
}

func droidModelValue(model droidAuthModel, value string, env func(string) string) string {
	if model.legacy {
		value = strings.TrimSpace(value)
		if strings.Contains(value, "${") || strings.Contains(value, "$(") || strings.ContainsAny(value, "`\n") {
			return ""
		}
		return value
	}
	return droidExpand(value, env)
}

func droidReadOptional(ctx context.Context, d authutil.Dependencies, path string) ([]byte, bool, bool) {
	info, err := droidLstat(d, path)
	if os.IsNotExist(err) {
		return nil, false, false
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, true, true
	}
	data, err := authutil.ReadFile(ctx, d, path)
	return data, true, err != nil
}

func droidLstat(d authutil.Dependencies, path string) (os.FileInfo, error) {
	if d.Lstat != nil {
		return d.Lstat(path)
	}
	return os.Lstat(path)
}

func droidNullField(fields map[string]json.RawMessage, names ...string) bool {
	for _, name := range names {
		if value, ok := fields[name]; ok && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return true
		}
	}
	return false
}

func droidBrowserLoginConfigured(ctx context.Context, d authutil.Dependencies, factoryDir string) bool {
	for _, name := range []string{"auth.v2.file", "auth.v2.key"} {
		data, err := authutil.ReadFile(ctx, d, filepath.Join(factoryDir, name))
		if err != nil || len(bytes.TrimSpace(data)) == 0 {
			return false
		}
	}
	return true
}

func droidExpand(value string, env func(string) string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "$(") || strings.ContainsAny(value, "`\n") {
		return ""
	}
	unresolved := false
	value = os.Expand(value, func(name string) string {
		if name == "" {
			unresolved = true
			return ""
		}
		resolved := strings.TrimSpace(env(name))
		if resolved == "" {
			unresolved = true
		}
		return resolved
	})
	if unresolved || strings.Contains(value, "$") {
		return ""
	}
	return strings.TrimSpace(value)
}

func droidValidHTTPURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != ""
}

func droidArgValue(args []string, name string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == name && i+1 < len(args) {
			return strings.TrimSpace(args[i+1])
		}
		if strings.HasPrefix(args[i], name+"=") {
			return strings.TrimSpace(strings.TrimPrefix(args[i], name+"="))
		}
	}
	return ""
}

func droidResolvePath(workingDir, path string) string {
	if filepath.IsAbs(path) || workingDir == "" {
		return path
	}
	return filepath.Join(workingDir, path)
}
