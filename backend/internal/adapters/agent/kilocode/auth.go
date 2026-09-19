package kilocode

import (
	"context"
	"database/sql"
	"encoding/json"
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

	_ "modernc.org/sqlite" // register the read-only credential database driver
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentScopedAuthChecker = (*Plugin)(nil)

func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.AuthStatusFor(ctx, ports.AgentAuthCheck{})
}

func (p *Plugin) AuthStatusFor(ctx context.Context, in ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	binary, err := p.ResolveBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return kilocodeAuthStatusFor(ctx, binary, in, authutil.Dependencies{})
}

// kilocodeAuthStatusFor keeps native effects injectable and local credentials
// provider-scoped. Listing or parsing a credential never validates it remotely.
func kilocodeAuthStatusFor(ctx context.Context, binary string, in ports.AgentAuthCheck, deps authutil.Dependencies) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	baseGetenv := deps.Getenv
	if baseGetenv == nil {
		baseGetenv = os.Getenv
	}
	deps.Getenv = func(name string) string {
		if value, ok := in.Env[name]; ok {
			return value
		}
		return baseGetenv(name)
	}
	if deps.Run == nil {
		deps.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := aoprocess.CommandContext(ctx, name, args...)
			cmd.Dir = in.WorkingDir
			cmd.Env = os.Environ()
			for key, value := range in.Env {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
			out := &kiloAuthOutput{}
			cmd.Stdout, cmd.Stderr = out, io.Discard
			cmd.WaitDelay = 100 * time.Millisecond
			err := cmd.Run()
			return out.data, err
		}
	}
	now := time.Now()
	if deps.Now != nil {
		now = deps.Now()
	}
	config := kiloAuthConfigFor(ctx, in, deps)
	model := config.Model
	if in.Config.Model != "" {
		model = in.Config.Model
	}
	for i, arg := range in.Args {
		if (arg == "--model" || arg == "-m") && i+1 < len(in.Args) {
			model = in.Args[i+1]
		}
		if strings.HasPrefix(arg, "--model=") {
			model = strings.TrimPrefix(arg, "--model=")
		}
	}
	provider, modelID, _ := strings.Cut(strings.TrimSpace(model), "/")
	if model == "" {
		provider = ""
	}
	if provider == "ollama" || provider == "lmstudio" ||
		(provider == "kilo" && strings.HasSuffix(modelID, ":free")) {
		return ports.AgentAuthStatusNotApplicable, nil
	}
	if provider != "" {
		key := ""
		if value := config.Provider[provider].Options.APIKey; value != nil {
			key = strings.TrimSpace(*value)
		}
		if strings.HasPrefix(key, "{env:") && strings.HasSuffix(key, "}") {
			key = strings.TrimSpace(deps.Getenv(strings.TrimSuffix(strings.TrimPrefix(key, "{env:"), "}")))
		}
		if key != "" && !strings.Contains(key, "{") {
			return ports.AgentAuthStatusConfigured, nil
		}
	}
	for id, names := range kilocodeProviderEnv {
		if provider != "" && provider != id {
			continue
		}
		for _, name := range names {
			if strings.TrimSpace(deps.Getenv(name)) != "" {
				return ports.AgentAuthStatusConfigured, nil
			}
		}
	}
	// KILO_AUTH_CONTENT is a process-local credential store, including when
	// explicitly empty. Native Kilo does not fall back to host storage in this mode.
	content, isolated := in.Env["KILO_AUTH_CONTENT"]
	if !isolated {
		content = deps.Getenv("KILO_AUTH_CONTENT")
		_, isolated = os.LookupEnv("KILO_AUTH_CONTENT")
		isolated = isolated || content != ""
	}
	if isolated {
		if kiloAuthEntries([]byte(content), provider, now) {
			return ports.AgentAuthStatusConfigured, nil
		}
		return ports.AgentAuthStatusUnknown, nil
	}
	dataDir := deps.Getenv("XDG_DATA_HOME")
	if home := kiloAuthHome(deps); dataDir == "" && home != "" {
		dataDir = filepath.Join(home, ".local", "share")
	}
	if dataDir != "" {
		data, err := authutil.ReadFile(ctx, deps, filepath.Join(dataDir, "kilo", "auth.json"))
		if err == nil && kiloAuthEntries(data, provider, now) {
			return ports.AgentAuthStatusConfigured, nil
		}
	}
	dbPath := strings.TrimSpace(deps.Getenv("KILO_DB"))
	// Native KILO_DB resolves relative names under Global.Path.data, not cwd.
	if dbPath != "" && dbPath != ":memory:" && !filepath.IsAbs(dbPath) && dataDir != "" {
		dbPath = filepath.Join(dataDir, "kilo", dbPath)
	}
	if dbPath == "" {
		if data, err := authutil.RunCommand(ctx, deps, binary, "db", "path"); err == nil {
			dbPath = strings.TrimSpace(string(data))
		}
	}
	if filepath.IsAbs(dbPath) && !strings.ContainsAny(dbPath, "\r\n") {
		if kiloDatabaseEvidence(ctx, dbPath, provider, now) {
			return ports.AgentAuthStatusConfigured, nil
		}
	}
	// Aggregate counts cannot establish that the selected provider is configured.
	if provider == "" {
		if data, err := authutil.RunCommand(ctx, deps, binary, "auth", "list"); err == nil {
			if status, ok := kilocodeAuthListStatus(string(data)); ok {
				return status, nil
			}
		}
	}
	return ports.AgentAuthStatusUnknown, ctx.Err()
}

type kiloAuthOutput struct{ data []byte }

func (o *kiloAuthOutput) Write(p []byte) (int, error) {
	// One extra byte lets authutil.RunCommand reject oversized output.
	remaining := authutil.MaxFileSize + 1 - len(o.data)
	if remaining > len(p) {
		remaining = len(p)
	}
	if remaining > 0 {
		o.data = append(o.data, p[:remaining]...)
	}
	return len(p), nil
}

var kilocodeProviderEnv = map[string][]string{
	"kilo":   {"KILO_API_KEY", "KILOCODE_API_KEY"},
	"openai": {"OPENAI_API_KEY"}, "anthropic": {"ANTHROPIC_API_KEY"},
	"google": {"GEMINI_API_KEY", "GOOGLE_API_KEY"}, "openrouter": {"OPENROUTER_API_KEY"},
	"deepseek": {"DEEPSEEK_API_KEY"}, "groq": {"GROQ_API_KEY"}, "xai": {"XAI_API_KEY"},
	"mistral": {"MISTRAL_API_KEY"}, "cohere": {"COHERE_API_KEY"},
}

var kilocodeAPIKeyEnvVars = []string{
	"KILO_API_KEY", "KILOCODE_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY",
	"GEMINI_API_KEY", "GOOGLE_API_KEY", "OPENROUTER_API_KEY", "DEEPSEEK_API_KEY",
	"GROQ_API_KEY", "XAI_API_KEY", "MISTRAL_API_KEY", "COHERE_API_KEY",
}

type kiloCredential struct {
	Type     string  `json:"type"`
	Key      string  `json:"key"`
	Access   *string `json:"access"`
	Refresh  *string `json:"refresh"`
	Expires  *int64  `json:"expires"`
	MethodID string  `json:"methodID"`
}

func (c kiloCredential) configured(now time.Time, database bool) bool {
	switch c.Type {
	case "api":
		return !database && strings.TrimSpace(c.Key) != ""
	case "key":
		return database && strings.TrimSpace(c.Key) != ""
	case "oauth":
		if c.Access == nil || c.Refresh == nil || c.Expires == nil || *c.Expires < 0 || (database && c.MethodID == "") {
			return false
		}
		return strings.TrimSpace(*c.Refresh) != "" || (strings.TrimSpace(*c.Access) != "" && time.UnixMilli(*c.Expires).After(now))
	default:
		return false
	}
}

func kiloAuthEntries(data []byte, provider string, now time.Time) bool {
	var entries map[string]json.RawMessage
	if json.Unmarshal(data, &entries) != nil {
		return false
	}
	for id, raw := range entries {
		if strings.TrimSpace(id) == "" || (provider != "" && strings.TrimRight(id, "/") != provider) {
			continue
		}
		var credential kiloCredential
		if json.Unmarshal(raw, &credential) == nil && credential.configured(now, false) {
			return true
		}
	}
	return false
}

type kiloAuthConfig struct {
	Model    string `json:"model"`
	Provider map[string]struct {
		Options struct {
			APIKey *string `json:"apiKey"`
		} `json:"options"`
	} `json:"provider"`
}

func kiloAuthConfigFor(ctx context.Context, in ports.AgentAuthCheck, deps authutil.Dependencies) kiloAuthConfig {
	var result kiloAuthConfig
	configHome := deps.Getenv("XDG_CONFIG_HOME")
	if home := kiloAuthHome(deps); configHome == "" && home != "" {
		configHome = filepath.Join(home, ".config")
	}
	var paths []string
	if configHome != "" {
		paths = append(paths, filepath.Join(configHome, "kilo", "kilo.json"))
	}
	if in.WorkingDir != "" {
		found, _ := authutil.FindUpward(ctx, deps, in.WorkingDir, "kilo.json")
		for i := len(found) - 1; i >= 0; i-- {
			paths = append(paths, found[i])
		}
	}
	if path := deps.Getenv("KILO_CONFIG"); path != "" {
		paths = append(paths, path)
	}
	merge := func(data []byte) {
		var next kiloAuthConfig
		if json.Unmarshal(data, &next) != nil {
			return
		}
		if next.Model != "" {
			result.Model = next.Model
		}
		if result.Provider == nil {
			result.Provider = next.Provider
		} else {
			for id, value := range next.Provider {
				// Native config merges nested objects. Omitted API keys inherit;
				// an explicitly supplied empty string still replaces the key.
				if value.Options.APIKey != nil {
					current := result.Provider[id]
					current.Options.APIKey = value.Options.APIKey
					result.Provider[id] = current
				}
			}
		}
	}
	for _, path := range paths {
		if data, err := authutil.ReadFile(ctx, deps, path); err == nil {
			merge(data)
		}
	}
	if content := deps.Getenv("KILO_CONFIG_CONTENT"); content != "" {
		merge([]byte(content))
	}
	return result
}

func kiloAuthHome(deps authutil.Dependencies) string {
	if home := deps.Getenv("HOME"); home != "" {
		return home
	}
	platform := deps.GOOS
	if platform == "" {
		platform = runtime.GOOS
	}
	if platform == "windows" {
		return deps.Getenv("USERPROFILE")
	}
	return ""
}

func kiloDatabaseEvidence(ctx context.Context, path, provider string, now time.Time) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro&_pragma=busy_timeout(1000)"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return false
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	probeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	// Current Kilo stores typed JSON values by integration_id. NULL active is
	// normal for current rows; explicitly disabled rows are not evidence.
	rows, err := db.QueryContext(probeCtx, "SELECT integration_id, value FROM credential WHERE (active IS NULL OR active = 1) AND (? = '' OR integration_id = ?) ORDER BY time_created DESC", provider, provider)
	if err == nil {
		seen := make(map[string]bool)
		for rows.Next() {
			var id string
			var raw []byte
			if rows.Scan(&id, &raw) != nil || id == "" || seen[id] {
				continue
			}
			seen[id] = true
			var credential kiloCredential
			if json.Unmarshal(raw, &credential) == nil && credential.configured(now, true) {
				_ = rows.Close()
				return true
			}
		}
		_ = rows.Close()
	}
	if provider != "" && provider != "kilo" {
		return false
	}
	// Legacy account selection is only evidence when it joins an actual token.
	for _, query := range []string{
		"SELECT a.access_token, a.refresh_token, a.token_expiry FROM account_state s JOIN account a ON a.id = s.active_account_id",
		"SELECT access_token, refresh_token, token_expiry FROM control_account WHERE active = 1",
	} {
		rows, err := db.QueryContext(probeCtx, query)
		if err != nil {
			continue
		}
		for rows.Next() {
			var access, refresh string
			var expiry sql.NullInt64
			if rows.Scan(&access, &refresh, &expiry) != nil {
				continue
			}
			if strings.TrimSpace(refresh) != "" || (strings.TrimSpace(access) != "" && (!expiry.Valid || time.UnixMilli(expiry.Int64).After(now))) {
				_ = rows.Close()
				return true
			}
		}
		_ = rows.Close()
	}
	return false
}

var kilocodeAuthListCountRE = regexp.MustCompile(`(?m)\b([1-9][0-9]*)\s+(credentials?|environment variables?)\b`)
var kilocodeAuthListZeroRE = regexp.MustCompile(`(?m)\b0\s+credentials?\b`)

func kilocodeAuthListStatus(output string) (ports.AgentAuthStatus, bool) {
	text := strings.ToLower(output)
	if kilocodeAuthListCountRE.MatchString(text) {
		return ports.AgentAuthStatusConfigured, true
	}
	if kilocodeAuthListZeroRE.MatchString(text) {
		return ports.AgentAuthStatusUnknown, true
	}
	return ports.AgentAuthStatusUnknown, false
}
