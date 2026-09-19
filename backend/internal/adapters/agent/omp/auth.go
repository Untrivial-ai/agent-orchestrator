package omp

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentScopedAuthChecker = (*Plugin)(nil)

type ompAuthDependencies struct {
	authutil.Dependencies
	OpenDB func(string) (*sql.DB, error)
}

// OMP positional status commands start its interactive agent. Only inspect
// bounded local sources, without launching a CLI, refreshing, or contacting a broker.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.AuthStatusFor(ctx, ports.AgentAuthCheck{})
}
func (p *Plugin) AuthStatusFor(ctx context.Context, scope ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	if _, err := p.ResolveBinary(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return ompAuthStatus(ctx, scope, ompAuthDependencies{})
}

// YAML's default string coercion would turn numbers/bools into fake keys.
type ompAuthString string

func (s *ompAuthString) UnmarshalYAML(n *yaml.Node) error {
	if n.Tag != "!!str" {
		return errors.New("credential field must be a string")
	}
	*s = ompAuthString(n.Value)
	return nil
}

// Native model roles accept a string or a list joined as comma-separated selectors.
type ompModelRole string

func (s *ompModelRole) UnmarshalYAML(n *yaml.Node) error {
	if n.Tag == "!!str" {
		*s = ompModelRole(n.Value)
		return nil
	}
	if n.Kind != yaml.SequenceNode {
		return errors.New("model role must be a string or string list")
	}
	values := make([]string, 0, len(n.Content))
	for _, value := range n.Content {
		if value.Tag != "!!str" {
			return errors.New("model role list entries must be strings")
		}
		values = append(values, value.Value)
	}
	*s = ompModelRole(strings.Join(values, ","))
	return nil
}

type ompProviderConfig struct {
	APIKey  *ompAuthString `yaml:"apiKey"`
	BaseURL ompAuthString  `yaml:"baseUrl"`
	Auth    ompAuthString  `yaml:"auth"`
	Models  []struct {
		ID ompAuthString `yaml:"id"`
	} `yaml:"models"`
}
type ompSettings struct {
	DisabledProviders []ompAuthString         `yaml:"disabledProviders"`
	ModelRoles        map[string]ompModelRole `yaml:"modelRoles"`
	Auth              struct {
		Broker struct {
			URL   ompAuthString `yaml:"url"`
			Token ompAuthString `yaml:"token"`
		} `yaml:"broker"`
	} `yaml:"auth"`
	BrokerURL   ompAuthString `yaml:"auth.broker.url"`
	BrokerToken ompAuthString `yaml:"auth.broker.token"`
}
type ompCredential struct {
	Type    string  `json:"type"`
	Key     string  `json:"key"`
	Access  string  `json:"access"`
	Refresh *string `json:"refresh"`
	Expires *int64  `json:"expires"`
	Source  string  `json:"source"`
}
type ompCredentialRow struct {
	Provider   string
	Credential ompCredential
}

func ompAuthStatus(ctx context.Context, scope ports.AgentAuthCheck, d ompAuthDependencies) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	d.WorkingDir = scope.WorkingDir
	inherited := d.Getenv
	processEnv := inherited == nil
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
	if d.Lstat == nil {
		d.Lstat = os.Lstat
	}
	home := d.Getenv("HOME")
	if home == "" {
		home = d.Getenv("USERPROFILE")
	}
	resolvePath := func(path string) string {
		if strings.HasPrefix(path, "~/") {
			return filepath.Join(home, path[2:])
		}
		if !filepath.IsAbs(path) && scope.WorkingDir != "" {
			return filepath.Join(scope.WorkingDir, path)
		}
		return path
	}
	root := filepath.Join(home, ".omp")
	dir := strings.TrimSpace(d.Getenv("PI_CODING_AGENT_DIR"))
	if dir == "" {
		dir = filepath.Join(root, "agent")
	} else {
		dir = resolvePath(dir)
	}
	// OMP fills missing env entries from project, agent, config-root, then home.
	dotenv := make(map[string]string)
	paths := []string{}
	if scope.WorkingDir != "" {
		paths = append(paths, filepath.Join(scope.WorkingDir, ".env"))
	}
	paths = append(paths, filepath.Join(dir, ".env"), filepath.Join(root, ".env"), filepath.Join(home, ".env"))
	for _, path := range paths {
		if data, err := authutil.ReadFile(ctx, d.Dependencies, path); err == nil {
			if values, err := authutil.ParseDotenv(data); err == nil {
				for key, value := range values {
					if dotenv[key] == "" {
						dotenv[key] = value
					}
				}
			}
		}
	}
	baseEnv := d.Getenv
	d.Getenv = func(name string) string {
		if value, ok := scope.Env[name]; ok {
			return value
		}
		if value := baseEnv(name); value != "" {
			return value
		}
		return dotenv[name]
	}
	profile := d.Getenv("OMP_PROFILE")
	_, scopedProfile := scope.Env["OMP_PROFILE"]
	canonicalProfileSet := scopedProfile || inherited("OMP_PROFILE") != ""
	if processEnv {
		_, present := os.LookupEnv("OMP_PROFILE")
		canonicalProfileSet = canonicalProfileSet || present
	}
	if !canonicalProfileSet && profile == "" {
		profile = d.Getenv("PI_PROFILE")
	}
	provider, model, runtimeKey := "", scope.Config.Model, ""
	for i := 0; i < len(scope.Args); i++ {
		arg := scope.Args[i]
		if arg == "--" {
			break
		}
		name, value, inline := strings.Cut(arg, "=")
		switch name {
		case "--provider", "--model", "--api-key", "--profile", "--alias", "--config",
			"--cwd", "--add-dir", "--mode", "--fork", "--smol", "--slow", "--plan",
			"--prewalk-into", "--plan-yolo-into", "--max-time", "--service-tier",
			"--system-prompt", "--append-system-prompt", "--provider-session-id", "--prompt-cache-key",
			"--session-dir", "--models", "--tools", "--thinking", "--export", "--hook",
			"--extension", "-e", "--trusted-extension", "--plugin-dir", "--skills", "--approval-mode":
			// Native built-in string options consume flag-looking values too.
			if !inline && i+1 < len(scope.Args) {
				// Native profile bootstrap treats --plan specially because an
				// extension can shadow it as a boolean. Do not guess that routing.
				if name == "--plan" && strings.HasPrefix(scope.Args[i+1], "-") {
					return ports.AgentAuthStatusUnknown, nil
				}
				i++
				value = scope.Args[i]
			}
		case "--resume", "-r", "--session":
			if !inline && i+1 < len(scope.Args) && !strings.HasPrefix(scope.Args[i+1], "-") && scope.Args[i+1] != "" {
				i++
			}
			continue
		default:
			continue
		}
		switch name {
		case "--provider":
			provider = value
		case "--model":
			model = value
		case "--api-key":
			runtimeKey = value
		case "--profile":
			profile = value
		case "--config", "--alias":
			return ports.AgentAuthStatusUnknown, nil
		}
	}
	// Named profiles and config overlays remain unresolved. Explicit default
	// (including a scoped empty canonical env value) is the native default root.
	profile = strings.TrimSpace(profile)
	if (profile != "" && profile != "default") || strings.TrimSpace(d.Getenv("PI_CONFIG_FILES")) != "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	var settings ompSettings
	for _, name := range []string{"config.yml", "config.yaml"} {
		path := filepath.Join(dir, name)
		if _, err := d.Lstat(path); err != nil {
			continue
		}
		if authutil.ReadYAML(ctx, d.Dependencies, path, &settings) != nil {
			settings = ompSettings{}
		}
		break
	}
	if scope.WorkingDir != "" {
		var project struct {
			ModelRoles map[string]*ompModelRole `yaml:"modelRoles"`
		}
		if authutil.ReadYAML(ctx, d.Dependencies, filepath.Join(scope.WorkingDir, ".omp", "config.yml"), &project) == nil {
			if settings.ModelRoles == nil {
				settings.ModelRoles = make(map[string]ompModelRole)
			}
			for role, value := range project.ModelRoles {
				// Native project null clears the override, exposing the global role.
				if value != nil {
					settings.ModelRoles[role] = *value
				}
			}
		}
	}
	var models struct {
		Providers map[string]ompProviderConfig `yaml:"providers"`
	}
	for _, name := range []string{"models.yml", "models.yaml"} {
		path := filepath.Join(dir, name)
		if _, err := d.Lstat(path); err != nil {
			continue
		}
		if authutil.ReadYAML(ctx, d.Dependencies, path, &models) != nil {
			models.Providers = nil
		}
		break
	}
	if model == "" {
		model = string(settings.ModelRoles["default"])
	}
	if provider == "" && model != "" {
		// Choosing among multiple native selectors requires model resolution;
		// the first selector's provider is not proof of the effective provider.
		if strings.Contains(model, ",") {
			return ports.AgentAuthStatusUnknown, nil
		}
		if prefix, _, ok := strings.Cut(model, "/"); ok {
			provider = prefix
		} else {
			for id, p := range models.Providers {
				for _, m := range p.Models {
					if string(m.ID) == model {
						if provider != "" && provider != id {
							return ports.AgentAuthStatusUnknown, nil
						}
						provider = id
					}
				}
			}
			if provider == "" {
				return ports.AgentAuthStatusUnknown, nil
			}
		}
	}
	disabled := make(map[string]bool)
	for _, id := range settings.DisabledProviders {
		disabled[string(id)] = true
	}
	if disabled[provider] {
		return ports.AgentAuthStatusUnknown, nil
	}
	custom := models.Providers[provider]
	if provider != "" {
		if custom.Auth != "" && custom.Auth != "none" && custom.Auth != "apiKey" && custom.Auth != "oauth" {
			return ports.AgentAuthStatusUnknown, nil
		}
		if custom.BaseURL != "" && !ompValidURL(string(custom.BaseURL)) {
			return ports.AgentAuthStatusUnknown, nil
		}
		if custom.Auth == "none" {
			return ports.AgentAuthStatusNotApplicable, nil
		}
		if ompLiteralCredential(runtimeKey) {
			return ports.AgentAuthStatusConfigured, nil
		}
		// Explicit models.yml credentials override stored/broker/env credentials.
		if custom.APIKey != nil {
			if ompConfigCredential(string(*custom.APIKey), d.Getenv) {
				return ports.AgentAuthStatusConfigured, nil
			}
			return ports.AgentAuthStatusUnknown, nil
		}
	}
	brokerURL := d.Getenv("OMP_AUTH_BROKER_URL")
	if brokerURL == "" {
		brokerURL = string(settings.Auth.Broker.URL)
		if brokerURL == "" {
			brokerURL = string(settings.BrokerURL)
		}
		if value := d.Getenv(brokerURL); value != "" {
			brokerURL = value
		}
	}
	brokerToken := d.Getenv("OMP_AUTH_BROKER_TOKEN")
	if brokerToken == "" {
		value := string(settings.Auth.Broker.Token)
		if value == "" {
			value = string(settings.BrokerToken)
		}
		if ompConfigCredential(value, d.Getenv) {
			brokerToken = value
			if resolved := d.Getenv(value); resolved != "" {
				brokerToken = resolved
			}
		}
	}
	if brokerToken == "" && brokerURL != "" {
		if data, err := authutil.ReadFile(ctx, d.Dependencies, filepath.Join(root, "auth-broker.token")); err == nil {
			brokerToken = strings.TrimSpace(string(data))
		}
	}
	var rows []ompCredentialRow
	// XDG_CONFIG_HOME is unused by the native resolver; state roots do not
	// route auth. Data/cache redirect only for the default agent directory on
	// Linux/macOS when the corresponding migrated app root already exists.
	xdgRoot := func(key, fallback string) string {
		platform := d.GOOS
		if platform == "" {
			platform = runtime.GOOS
		}
		if (platform == "linux" || platform == "darwin") && dir == filepath.Join(root, "agent") && d.Getenv(key) != "" {
			path := resolvePath(filepath.Join(d.Getenv(key), "omp"))
			if _, err := d.Lstat(path); err == nil {
				return path
			}
		}
		return fallback
	}
	if brokerURL != "" {
		// Broker selection replaces local SQLite; never silently mix credential pools.
		if strings.TrimSpace(d.Getenv("OMP_AUTH_BROKER_ACCOUNT_POOL_FILE")) != "" {
			return ports.AgentAuthStatusUnknown, nil
		}
		if !ompValidURL(brokerURL) || !ompLiteralCredential(brokerToken) {
			return ports.AgentAuthStatusUnknown, nil
		}
		if provider == "" {
			return ports.AgentAuthStatusConfigured, nil
		}
		cachePath := d.Getenv("OMP_AUTH_BROKER_SNAPSHOT_CACHE")
		if cachePath == "" {
			cachePath = filepath.Join(xdgRoot("XDG_CACHE_HOME", root), "cache", "auth-broker-snapshot.enc")
		}
		rows = ompBrokerCache(ctx, d, resolvePath(cachePath), brokerURL, brokerToken)
	} else {
		rows = ompDatabaseCredentials(ctx, d, filepath.Join(xdgRoot("XDG_DATA_HOME", dir), "agent.db"), provider)
	}
	var fallback map[string]json.RawMessage
	if brokerURL == "" {
		if authutil.ReadJSON(ctx, d.Dependencies, filepath.Join(dir, "auth.json"), &fallback) != nil {
			fallback = nil
		}
	}
	evaluate := func(id string, selected bool) ports.AgentAuthStatus {
		if disabled[id] || id == "" || strings.HasPrefix(id, "mcp:") {
			return ports.AgentAuthStatusUnknown
		}
		custom := models.Providers[id]
		if custom.BaseURL != "" && !ompValidURL(string(custom.BaseURL)) {
			return ports.AgentAuthStatusUnknown
		}
		if custom.Auth != "" && custom.Auth != "none" && custom.Auth != "apiKey" && custom.Auth != "oauth" {
			return ports.AgentAuthStatusUnknown
		}
		if custom.Auth == "none" {
			if selected {
				return ports.AgentAuthStatusNotApplicable
			}
			return ports.AgentAuthStatusUnknown
		}
		if selected && id == "amazon-bedrock" && d.Getenv("AWS_BEDROCK_SKIP_AUTH") == "1" {
			return ports.AgentAuthStatusNotApplicable
		}
		if custom.APIKey != nil {
			if ompConfigCredential(string(*custom.APIKey), d.Getenv) {
				return ports.AgentAuthStatusConfigured
			}
			return ports.AgentAuthStatusUnknown
		}
		expired := false
		for _, row := range rows {
			if row.Provider != id {
				continue
			}
			switch ompCredentialStatus(row.Credential, id, d) {
			case ports.AgentAuthStatusConfigured:
				return ports.AgentAuthStatusConfigured
			case ports.AgentAuthStatusUnauthorized:
				expired = true
			}
		}
		for _, key := range ompProviderEnv[id] {
			if ompLiteralCredential(d.Getenv(key)) {
				return ports.AgentAuthStatusConfigured
			}
		}
		switch id {
		case "amazon-bedrock", "bedrock-mantle":
			if status := authutil.AWSEvidence(ctx, d.Dependencies).Status; status != ports.AgentAuthStatusUnknown {
				return status
			}
		case "google-vertex":
			project := d.Getenv("GOOGLE_CLOUD_PROJECT") + d.Getenv("GCP_PROJECT") + d.Getenv("GCLOUD_PROJECT")
			location := d.Getenv("GOOGLE_VERTEX_LOCATION") + d.Getenv("GOOGLE_CLOUD_LOCATION") + d.Getenv("VERTEX_LOCATION")
			if strings.TrimSpace(project) != "" && strings.TrimSpace(location) != "" {
				if status := authutil.GoogleADCEvidence(ctx, d.Dependencies).Status; status != ports.AgentAuthStatusUnknown {
					return status
				}
			}
		}
		var credential ompCredential
		if json.Unmarshal(fallback[id], &credential) == nil {
			if status := ompCredentialStatus(credential, id, d); status != ports.AgentAuthStatusUnknown {
				return status
			}
		}
		if expired && selected {
			return ports.AgentAuthStatusUnauthorized
		}
		return ports.AgentAuthStatusUnknown
	}
	if provider != "" {
		return evaluate(provider, true), ctx.Err()
	}
	candidates := make(map[string]bool)
	for id := range ompProviderEnv {
		candidates[id] = true
	}
	for id := range models.Providers {
		candidates[id] = true
	}
	for _, row := range rows {
		candidates[row.Provider] = true
	}
	for id := range fallback {
		candidates[id] = true
	}
	for id := range candidates {
		if evaluate(id, false) == ports.AgentAuthStatusConfigured {
			return ports.AgentAuthStatusConfigured, ctx.Err()
		}
	}
	return ports.AgentAuthStatusUnknown, ctx.Err()
}

func ompDatabaseCredentials(ctx context.Context, d ompAuthDependencies, path, provider string) []ompCredentialRow {
	info, err := d.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	open := d.OpenDB
	if open == nil {
		open = func(dsn string) (*sql.DB, error) { return sql.Open("sqlite", dsn) }
	}
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(100)"}
	db, err := open(uri.String())
	if err != nil {
		return nil
	}
	defer db.Close()
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	query := "SELECT provider, credential_type, substr(data,1,1048577) FROM auth_credentials WHERE disabled_cause IS NULL"
	args := []any{}
	if provider != "" {
		query += " AND provider = ?"
		args = append(args, provider)
	}
	query += " ORDER BY id LIMIT 1024"
	result, err := db.QueryContext(queryCtx, query, args...)
	if err != nil {
		return nil
	}
	defer result.Close()
	var rows []ompCredentialRow
	for result.Next() {
		var id, kind, payload string
		if result.Scan(&id, &kind, &payload) != nil || len(payload) > authutil.MaxFileSize {
			continue
		}
		var credential ompCredential
		if json.Unmarshal([]byte(payload), &credential) != nil {
			continue
		}
		credential.Type = kind
		rows = append(rows, ompCredentialRow{Provider: id, Credential: credential})
	}
	if result.Err() != nil {
		return nil
	}
	return rows
}

func ompCredentialStatus(c ompCredential, provider string, d ompAuthDependencies) ports.AgentAuthStatus {
	switch c.Type {
	case "api_key":
		if ompConfigCredential(c.Key, d.Getenv) {
			return ports.AgentAuthStatusConfigured
		}
	case "oauth":
		if !ompOAuthProviders[provider] || !ompLiteralCredential(c.Access) || c.Refresh == nil || c.Expires == nil || *c.Expires <= 0 {
			return ports.AgentAuthStatusUnknown
		}
		return authutil.ExpiryEvidence(time.UnixMilli(*c.Expires), ompLiteralCredential(*c.Refresh), d.Now()).Status
	}
	return ports.AgentAuthStatusUnknown
}

var ompEnvReference = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

func ompConfigCredential(value string, getenv func(string) string) bool {
	value = strings.TrimSpace(value)
	if !ompLiteralCredential(value) {
		return false
	}
	if resolved := getenv(value); resolved != "" {
		return ompLiteralCredential(resolved)
	}
	return !ompEnvReference.MatchString(value)
}
func ompLiteralCredential(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.HasPrefix(value, "!") && !strings.HasPrefix(value, "$")
}
func ompValidURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil
}

func ompBrokerCache(ctx context.Context, d ompAuthDependencies, path, brokerURL, token string) []ompCredentialRow {
	data, err := authutil.ReadFile(ctx, d.Dependencies, path)
	if err != nil || len(data) <= 17 || string(data[:5]) != "OMPS"+string([]byte{2}) {
		return nil
	}
	key := sha256.Sum256([]byte(token))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil
	}
	additional := append([]byte("OMPS"+string([]byte{2})), []byte(brokerURL)...)
	plaintext, err := gcm.Open(nil, data[5:17], data[17:], additional)
	if err != nil {
		return nil
	}
	var snapshot struct {
		Generation  *int64 `json:"generation"`
		GeneratedAt int64  `json:"generatedAt"`
		ServerNowMS int64  `json:"serverNowMs"`
		Refresher   *struct {
			Enabled bool `json:"enabled"`
		} `json:"refresher"`
		Credentials []struct {
			ID         int64         `json:"id"`
			Provider   string        `json:"provider"`
			Credential ompCredential `json:"credential"`
		} `json:"credentials"`
	}
	if json.Unmarshal(plaintext, &snapshot) != nil || snapshot.Generation == nil || snapshot.GeneratedAt <= 0 || snapshot.ServerNowMS <= 0 || snapshot.Refresher == nil {
		return nil
	}
	ttl := int64(time.Hour / time.Millisecond)
	if value := d.Getenv("OMP_AUTH_BROKER_SNAPSHOT_TTL_MS"); value != "" {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil && parsed >= 0 {
			ttl = parsed
		}
	}
	age := d.Now().UnixMilli() - snapshot.GeneratedAt
	if ttl == 0 || age < 0 || age > ttl {
		return nil
	}
	rows := make([]ompCredentialRow, 0, len(snapshot.Credentials))
	for _, entry := range snapshot.Credentials {
		if entry.ID > 0 {
			rows = append(rows, ompCredentialRow{Provider: entry.Provider, Credential: entry.Credential})
		}
	}
	return rows
}

var ompOAuthProviders = map[string]bool{
	"anthropic": true, "openai-codex": true, "github-copilot": true, "google-gemini-cli": true, "google-antigravity": true,
	"openrouter": true, "cursor": true, "devin": true, "xai-oauth": true, "gitlab-duo": true, "gitlab-duo-agent": true,
	"kimi-code": true, "muse-code": true, "zai-coding-plan": true, "stencil": true,
	"kilo": true, "perplexity": true, "alibaba-coding-plan": true, "alibaba-token-plan": true,
	"cloudflare-ai-gateway": true, "xiaomi": true, "openai-codex-device": true,
}

// Credential variables from OMP's compiled catalog and explicit auth overrides.
// Cloud hooks are evaluated separately; discovery-only variables are excluded.
var ompProviderEnv = map[string][]string{
	"abliteration":          {"ABLITERATION_API_KEY", "ABLIT_KEY"},
	"aiand":                 {"AIAND_API_KEY"},
	"aimlapi":               {"AIMLAPI_API_KEY"},
	"alibaba-coding-plan":   {"ALIBABA_CODING_PLAN_API_KEY"},
	"alibaba-token-plan":    {"ALIBABA_TOKEN_PLAN_API_KEY", "BAILIAN_TOKEN_PLAN_API_KEY"},
	"amazon-bedrock":        {},
	"anthropic":             {"ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY"},
	"azure":                 {"AZURE_OPENAI_API_KEY"},
	"baseten":               {"BASETEN_API_KEY"},
	"bedrock-mantle":        {"AWS_BEARER_TOKEN_BEDROCK"},
	"cerebras":              {"CEREBRAS_API_KEY"},
	"charm-hyper":           {"CHARM_HYPER_API_KEY", "HYPER_API_KEY"},
	"cline-pass":            {"CLINE_API_KEY"},
	"cloudflare-ai-gateway": {"CLOUDFLARE_AI_GATEWAY_API_KEY"},
	"commandcode":           {"COMMAND_CODE_API_KEY", "COMMANDCODE_API_KEY"},
	"coreweave":             {"COREWEAVE_API_KEY", "WANDB_API_KEY"},
	"cursor":                {"CURSOR_ACCESS_TOKEN"},
	"deepinfra":             {"DEEPINFRA_API_KEY"},
	"deepseek":              {"DEEPSEEK_API_KEY"},
	"devin":                 {"DEVIN_API_KEY"},
	"firepass":              {"FIREPASS_API_KEY"},
	"fireworks":             {"FIREWORKS_API_KEY"},
	"github-copilot":        {"COPILOT_GITHUB_TOKEN"},
	"gitlab-duo":            {"GITLAB_TOKEN"},
	"gitlab-duo-agent":      {"GITLAB_TOKEN"},
	"gmi-cloud":             {"GMI_API_KEY"},
	"google":                {"GEMINI_API_KEY"},
	"google-antigravity":    {},
	"google-gemini-cli":     {},
	"google-vertex":         {"GOOGLE_CLOUD_API_KEY"},
	"groq":                  {"GROQ_API_KEY"},
	"huggingface":           {"HUGGINGFACE_HUB_TOKEN", "HF_TOKEN"},
	"kilo":                  {"KILO_API_KEY"},
	"kimi-code":             {},
	"litellm":               {"LITELLM_API_KEY"},
	"lm-studio":             {"LM_STUDIO_API_KEY"},
	"meta":                  {"MODEL_API_KEY", "META_API_KEY"},
	"minimax":               {"MINIMAX_API_KEY"},
	"minimax-code":          {"MINIMAX_CODE_API_KEY"},
	"minimax-code-cn":       {"MINIMAX_CODE_CN_API_KEY"},
	"mistral":               {"MISTRAL_API_KEY"},
	"moonshot":              {"MOONSHOT_API_KEY", "KIMI_API_KEY"},
	"muse-code":             {},
	"nanogpt":               {"NANO_GPT_API_KEY"},
	"novita":                {"NOVITA_API_KEY"},
	"nvidia":                {"NVIDIA_API_KEY"},
	"ollama":                {"OLLAMA_API_KEY"},
	"ollama-cloud":          {"OLLAMA_CLOUD_API_KEY"},
	"openai":                {"OPENAI_API_KEY"},
	"openai-codex":          {"OPENAI_CODEX_OAUTH_TOKEN"},
	"opencode-go":           {"OPENCODE_API_KEY"},
	"opencode-zen":          {"OPENCODE_API_KEY"},
	"openrouter":            {"OPENROUTER_API_KEY"},
	"qianfan":               {"QIANFAN_API_KEY"},
	"qwen-portal":           {"QWEN_OAUTH_TOKEN", "QWEN_PORTAL_API_KEY"},
	"sakana":                {"SAKANA_API_KEY", "FUGU_API_KEY"},
	"siliconflow":           {"SILICONFLOW_API_KEY"},
	"siliconflow-cn":        {"SILICONFLOW_CN_API_KEY"},
	"synthetic":             {"SYNTHETIC_API_KEY"},
	"together":              {"TOGETHER_API_KEY"},
	"umans":                 {"UMANS_AI_CODING_PLAN_API_KEY"},
	"venice":                {"VENICE_API_KEY"},
	"vercel-ai-gateway":     {"AI_GATEWAY_API_KEY"},
	"vllm":                  {"VLLM_API_KEY"},
	"wafer-serverless":      {"WAFER_SERVERLESS_API_KEY"},
	"xai":                   {"XAI_API_KEY"},
	"xai-oauth":             {"XAI_OAUTH_TOKEN", "XAI_API_KEY"},
	"xiaomi":                {"XIAOMI_API_KEY"},
	"xiaomi-token-plan-ams": {"XIAOMI_TOKEN_PLAN_AMS_API_KEY"},
	"xiaomi-token-plan-cn":  {"XIAOMI_TOKEN_PLAN_CN_API_KEY"},
	"xiaomi-token-plan-sgp": {"XIAOMI_TOKEN_PLAN_SGP_API_KEY"},
	"yolo-auto":             {"YOLO_AUTO_API_KEY"},
	"zai":                   {"ZAI_API_KEY"},
	"zenmux":                {"ZENMUX_API_KEY"},
	"zhipu-coding-plan":     {"ZHIPU_API_KEY"},
}
