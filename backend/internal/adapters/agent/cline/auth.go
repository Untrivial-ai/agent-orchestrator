package cline

import (
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
	LastUsedProvider string                   `json:"lastUsedProvider"`
	Providers        map[string]clineProvider `json:"providers"`
}

type clineProvider struct {
	Settings    clineProviderSettings `json:"settings"`
	UpdatedAt   string                `json:"updatedAt"`
	TokenSource string                `json:"tokenSource"`
}

type clineProviderSettings struct {
	Provider string              `json:"provider"`
	APIKey   string              `json:"apiKey"`
	Auth     *clineProviderAuth  `json:"auth"`
	Model    string              `json:"model"`
	BaseURL  string              `json:"baseUrl"`
	AWS      *clineAWSSettings   `json:"aws"`
	GCP      *clineGCPSettings   `json:"gcp"`
	Azure    *clineAzureSettings `json:"azure"`
	SAP      *clineSAPSettings   `json:"sap"`
}

type clineProviderAuth struct {
	APIKey       string `json:"apiKey"`
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    int64  `json:"expiresAt"`
}

type clineAWSSettings struct {
	AccessKey      string `json:"accessKey"`
	SecretKey      string `json:"secretKey"`
	SessionToken   string `json:"sessionToken"`
	Region         string `json:"region"`
	Profile        string `json:"profile"`
	Authentication string `json:"authentication"`
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
	ClientID      string `json:"clientId"`
	ClientSecret  string `json:"clientSecret"`
	TokenURL      string `json:"tokenUrl"`
	ResourceGroup string `json:"resourceGroup"`
	DeploymentID  string `json:"deploymentId"`
	API           string `json:"api"`
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
		selected = strings.TrimSpace(providers.LastUsedProvider)
	}
	if selected == "" {
		selected = "cline"
	}

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
	if f.Version != 1 || f.Providers == nil {
		return false
	}
	for id, entry := range f.Providers {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(entry.Settings.Provider) == "" || entry.Settings.Provider != id {
			return false
		}
		if entry.Settings.Auth != nil && entry.Settings.Auth.ExpiresAt < 0 {
			return false
		}
		if entry.UpdatedAt != "" {
			if _, err := time.Parse(time.RFC3339, entry.UpdatedAt); err != nil {
				return false
			}
		}
		switch entry.TokenSource {
		case "", "manual", "oauth", "migration":
		default:
			return false
		}
	}
	return true
}

func (f clineProvidersFile) selectedSettings(selected string) (clineProviderSettings, bool) {
	entry, ok := f.Providers[selected]
	if selected != "cline-pass" {
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
	if settings.Auth != nil {
		auth := settings.Auth
		if strings.TrimSpace(auth.RefreshToken) != "" {
			return ports.AgentAuthStatusConfigured
		}
		if strings.TrimSpace(auth.AccessToken) != "" {
			if auth.ExpiresAt > 0 && !time.UnixMilli(auth.ExpiresAt).After(clineNow(d)) {
				return ports.AgentAuthStatusUnauthorized
			}
			return ports.AgentAuthStatusConfigured
		}
	}

	switch strings.ToLower(strings.TrimSpace(settings.Provider)) {
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
