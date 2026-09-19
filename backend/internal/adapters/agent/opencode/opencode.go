// Package opencode implements the opencode (sst/opencode) agent adapter:
// launching new TUI sessions, resuming sessions by native id, installing a
// workspace-local activity plugin plus the using-ao skill, and reading
// plugin-derived session info.
//
// opencode differs from Claude Code and Codex in two ways AO has to bridge:
//   - It has no native command-hook config (no settings.local.json / hooks.json
//     equivalent). Its only lifecycle-extensibility surface is a JS/TS plugin
//     loaded from .opencode/plugins/, so GetAgentHooks installs an AO-owned
//     plugin file (see hooks.go) instead of merging JSON. The same install also
//     materializes using-ao under .opencode/skills/ so opencode's skill tool
//     can discover it (the data-dir skill path alone is invisible to opencode).
//   - Its CLI exposes only one approval flag (--dangerously-skip-permissions)
//     and no system-prompt flag, so AO injects standing instructions by writing
//     an AO-owned per-session config and selecting the generated agent.
//
// AO-managed sessions derive native session identity and display metadata from
// the opencode plugin's reported events, mirroring the Codex adapter.
package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"

	_ "modernc.org/sqlite" // register sqlite driver for opencode session metadata probes
)

const (
	// adapterID is the registry id and the value users pass to
	// `ao spawn --agent`. It matches domain.HarnessOpenCode.
	adapterID = "opencode"

	// opencodeAgentSessionIDMetadataKey is the session-metadata key the opencode
	// plugin persists the native session id under. GetRestoreCommand reads it back
	// to resume an existing session. SessionInfo delegates to
	// agentbase.StandardSessionInfo which reads ports.MetadataKeyAgentSessionID
	// (same value), but GetRestoreCommand reads it directly, so the const stays.
	opencodeAgentSessionIDMetadataKey = "agentSessionId"
)

var opencodeUnixPaths = []string{
	"/usr/local/bin/opencode",
	"/opt/homebrew/bin/opencode",
}

// Plugin is the opencode agent adapter. It is safe for concurrent use; the
// binary path is resolved once and cached under binaryMu.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register opencode adapter.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)
var _ ports.AgentAuthChecker = (*Plugin)(nil)

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          adapterID,
		Name:        "opencode",
		Description: "Run opencode worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec reports opencode's optional provider/model override.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	return agentbase.ModelConfigSpec(ctx, "Model override passed to `opencode --model`.")
}

// GetLaunchCommand builds the argv to start a new interactive opencode session.
// Shape:
//
//	[env OPENCODE_CONFIG=<ao-config>] opencode [--dangerously-skip-permissions] [--agent <ao-agent>] [--prompt <prompt>]
//
// The session runs in the worktree (cwd is set by the runtime, as for Claude
// Code and Codex). opencode has no CLI flag to set a system prompt, so AO writes
// an opencode config into the AO prompt artifact directory, points OPENCODE_CONFIG
// at it, and selects the generated agent with --agent. The initial task prompt
// is delivered via --prompt (its argument, so a leading "-" is not read as a flag).
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	binary, err := p.opencodeBinary(ctx)
	if err != nil {
		return nil, err
	}

	envPrefix, agentName, err := opencodeConfigEnvPrefix(cfg.SystemPrompt, cfg.SystemPromptFile, cfg.SessionID)
	if err != nil {
		return nil, err
	}
	cmd = envPrefix
	cmd = append(cmd, binary)
	appendPermissionFlags(&cmd, cfg.Permissions)
	agentbase.AppendModelFlag(&cmd, cfg.Config, "--model")
	if agentName != "" {
		cmd = append(cmd, "--agent", agentName)
	}
	if cfg.Prompt != "" {
		cmd = append(cmd, "--prompt", cfg.Prompt)
	}
	return cmd, nil
}

// GetRestoreCommand rebuilds the argv that continues an existing opencode
// session: `[env OPENCODE_CONFIG=<ao-config>] opencode [--dangerously-skip-permissions] [--agent <ao-agent>] --session <agentSessionId> [--prompt <prompt>]`.
// It re-applies the permission flag and the generated AO agent config (resume
// otherwise reverts to configured defaults). ok is false when the plugin-derived
// native session id has not landed yet, so callers fall back to fresh launch
// behavior — mirroring the Codex adapter. The optional resume-time prompt is
// applied the same way GetLaunchCommand does, so a review task submitted
// alongside a resume starts atomically with the process instead of racing a
// post-launch terminal injection.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) (cmd []string, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	agentSessionID := strings.TrimSpace(cfg.Session.Metadata[opencodeAgentSessionIDMetadataKey])
	if agentSessionID == "" {
		return nil, false, nil
	}

	binary, err := p.opencodeBinary(ctx)
	if err != nil {
		return nil, false, err
	}

	envPrefix, agentName, err := opencodeConfigEnvPrefix(cfg.SystemPrompt, cfg.SystemPromptFile, cfg.Session.ID)
	if err != nil {
		return nil, false, err
	}
	cmd = envPrefix
	cmd = append(cmd, binary)
	appendPermissionFlags(&cmd, cfg.Permissions)
	agentbase.AppendModelFlag(&cmd, cfg.Config, "--model")
	if agentName != "" {
		cmd = append(cmd, "--agent", agentName)
	}
	cmd = append(cmd, "--session", agentSessionID)
	if cfg.Prompt != "" {
		cmd = append(cmd, "--prompt", cfg.Prompt)
	}
	return cmd, true, nil
}

// SessionInfo surfaces opencode plugin-derived metadata. Metadata is
// intentionally nil for opencode: callers get the normalized fields directly,
// matching the Codex adapter.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

// AuthStatus delegates to the same provider-aware resolver as scoped launches.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.AuthStatusFor(ctx, ports.AgentAuthCheck{})
}

var _ ports.AgentScopedAuthChecker = (*Plugin)(nil)

func (p *Plugin) AuthStatusFor(ctx context.Context, in ports.AgentAuthCheck) (ports.AgentAuthStatus, error) {
	binary, err := p.opencodeBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return opencodeAuthStatusFor(ctx, binary, in, authutil.Dependencies{})
}

func opencodeAuthStatusFor(ctx context.Context, binary string, in ports.AgentAuthCheck, deps authutil.Dependencies) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	baseGetenv := deps.Getenv
	if baseGetenv == nil {
		baseGetenv = os.Getenv
	}
	launchEnv := make(map[string]string, len(in.Env))
	for name, value := range in.Env {
		launchEnv[name] = value
	}
	// AO's generated config may be carried by the native command's env prefix.
	if len(in.Args) > 0 && filepath.Base(in.Args[0]) == "env" {
		for _, arg := range in.Args[1:] {
			name, value, ok := strings.Cut(arg, "=")
			if !ok || name == "" {
				break
			}
			launchEnv[name] = value
		}
	}
	deps.Getenv = func(name string) string {
		if value, ok := launchEnv[name]; ok {
			return value
		}
		return baseGetenv(name)
	}
	if deps.Run == nil {
		deps.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := aoprocess.CommandContext(ctx, name, args...)
			cmd.Dir = in.WorkingDir
			cmd.Env = os.Environ()
			for key, value := range launchEnv {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
			out := &opencodeAuthOutput{}
			cmd.Stdout, cmd.Stderr = out, io.Discard
			cmd.WaitDelay = 100 * time.Millisecond
			err := cmd.Run()
			return out.data, err
		}
	}
	model := opencodeAuthModel(ctx, in, deps)
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
	if provider == "" && (in.WorkingDir != "" || in.DataDir != "" || !in.Config.IsZero() ||
		len(in.Args) > 0 || len(in.Env) > 0 || in.Interactive) {
		// A launch without a resolved provider cannot borrow device-wide
		// credentials or aggregate auth-list counts.
		return ports.AgentAuthStatusUnknown, ctx.Err()
	}
	if provider == "ollama" || provider == "lmstudio" ||
		(provider == "opencode" && (modelID == "big-pickle" || strings.HasSuffix(modelID, "-free"))) {
		return ports.AgentAuthStatusNotApplicable, nil
	}
	for id, names := range opencodeProviderEnv {
		if provider != "" && provider != id {
			continue
		}
		for _, name := range names {
			if strings.TrimSpace(deps.Getenv(name)) != "" {
				return ports.AgentAuthStatusConfigured, nil
			}
		}
	}
	if provider == "" || provider == "amazon-bedrock" {
		if evidence := authutil.AWSEvidence(ctx, deps); evidence.Status == ports.AgentAuthStatusConfigured {
			return evidence.Status, nil
		}
	}
	dataDir := deps.Getenv("XDG_DATA_HOME")
	if home := opencodeAuthHome(deps); dataDir == "" && home != "" {
		dataDir = filepath.Join(home, ".local", "share")
	}
	if dataDir != "" {
		data, err := authutil.ReadFile(ctx, deps, filepath.Join(dataDir, "opencode", "auth.json"))
		now := time.Now()
		if deps.Now != nil {
			now = deps.Now()
		}
		if err == nil && opencodeAuthEntries(data, provider, now) {
			return ports.AgentAuthStatusConfigured, nil
		}
	}
	// No guessed opencode.db path participates in credential discovery. The
	// documented auth-list command is the native fallback for global checks;
	// aggregate counts cannot prove the selected provider has a credential.
	if provider == "" {
		if data, err := authutil.RunCommand(ctx, deps, binary, "auth", "list"); err == nil && opencodeAuthListCountRE.Match(data) {
			return ports.AgentAuthStatusConfigured, nil
		}
	}
	return ports.AgentAuthStatusUnknown, ctx.Err()
}

func opencodeAuthHome(deps authutil.Dependencies) string {
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

// Model selection follows native config precedence, before inspecting any
// credentials: global, explicit file, project, config directories, inline.
func opencodeAuthModel(ctx context.Context, in ports.AgentAuthCheck, deps authutil.Dependencies) string {
	configHome := deps.Getenv("XDG_CONFIG_HOME")
	if home := opencodeAuthHome(deps); configHome == "" && home != "" {
		configHome = filepath.Join(home, ".config")
	}
	var paths []string
	if configHome != "" {
		for _, name := range []string{"config.json", "opencode.json", "opencode.jsonc"} {
			paths = append(paths, filepath.Join(configHome, "opencode", name))
		}
	}
	if path := deps.Getenv("OPENCODE_CONFIG"); path != "" {
		if !filepath.IsAbs(path) && in.WorkingDir != "" {
			path = filepath.Join(in.WorkingDir, path)
		}
		paths = append(paths, path)
	}
	if in.WorkingDir != "" && deps.Getenv("OPENCODE_DISABLE_PROJECT_CONFIG") != "1" &&
		!strings.EqualFold(deps.Getenv("OPENCODE_DISABLE_PROJECT_CONFIG"), "true") {
		// Reverse nearest-first results, including filename order, so JSONC
		// follows JSON and the nearest project has highest precedence.
		found, _ := authutil.FindUpward(ctx, deps, in.WorkingDir, "opencode.jsonc", "opencode.json")
		for i := len(found) - 1; i >= 0; i-- {
			paths = append(paths, found[i])
		}
		found, _ = authutil.FindUpward(ctx, deps, in.WorkingDir, filepath.Join(".opencode", "opencode.jsonc"), filepath.Join(".opencode", "opencode.json"))
		for i := len(found) - 1; i >= 0; i-- {
			paths = append(paths, found[i])
		}
	}
	if home := opencodeAuthHome(deps); home != "" {
		paths = append(paths, filepath.Join(home, ".opencode", "opencode.json"), filepath.Join(home, ".opencode", "opencode.jsonc"))
	}
	if dir := deps.Getenv("OPENCODE_CONFIG_DIR"); dir != "" {
		if !filepath.IsAbs(dir) && in.WorkingDir != "" {
			dir = filepath.Join(in.WorkingDir, dir)
		}
		paths = append(paths, filepath.Join(dir, "opencode.json"), filepath.Join(dir, "opencode.jsonc"))
	}
	model := ""
	merge := func(data []byte) {
		var config struct {
			Model *string `json:"model"`
		}
		if json.Unmarshal(opencodeConfigJSON(data), &config) == nil && config.Model != nil {
			model = *config.Model
		}
	}
	for _, path := range paths {
		if data, err := authutil.ReadFile(ctx, deps, path); err == nil {
			merge(data)
		}
	}
	if content := deps.Getenv("OPENCODE_CONFIG_CONTENT"); content != "" {
		merge([]byte(content))
	}
	return model
}

// OpenCode accepts JSONC. Strip only comments and trailing commas outside
// strings, then let encoding/json validate the complete typed config.
func opencodeConfigJSON(data []byte) []byte {
	if len(data) > authutil.MaxFileSize {
		return nil
	}
	out := append([]byte(nil), data...)
	quoted := false
	for i := 0; i < len(out); i++ {
		if quoted {
			if out[i] == '\\' {
				i++
				continue
			}
			if out[i] == '"' {
				quoted = false
			}
			continue
		}
		if out[i] == '"' {
			quoted = true
			continue
		}
		if out[i] != '/' || i+1 >= len(out) {
			continue
		}
		switch out[i+1] {
		case '/':
			for i < len(out) && out[i] != '\n' && out[i] != '\r' {
				out[i] = ' '
				i++
			}
		case '*':
			out[i], out[i+1] = ' ', ' '
			i += 2
			for i+1 < len(out) && !(out[i] == '*' && out[i+1] == '/') {
				out[i] = ' '
				i++
			}
			if i+1 >= len(out) {
				return nil
			}
			out[i], out[i+1] = ' ', ' '
			i++
		}
	}
	quoted = false
	for i := 0; i < len(out); i++ {
		if quoted {
			if out[i] == '\\' {
				i++
				continue
			}
			if out[i] == '"' {
				quoted = false
			}
			continue
		}
		if out[i] == '"' {
			quoted = true
			continue
		}
		if out[i] != ',' {
			continue
		}
		j := i + 1
		for j < len(out) && (out[j] == ' ' || out[j] == '\t' || out[j] == '\r' || out[j] == '\n') {
			j++
		}
		if j < len(out) && (out[j] == '}' || out[j] == ']') {
			out[i] = ' '
		}
	}
	return out
}

type opencodeAuthOutput struct{ data []byte }

func (o *opencodeAuthOutput) Write(p []byte) (int, error) {
	remaining := authutil.MaxFileSize + 1 - len(o.data)
	if remaining > len(p) {
		remaining = len(p)
	}
	if remaining > 0 {
		o.data = append(o.data, p[:remaining]...)
	}
	return len(p), nil
}

var opencodeAuthListCountRE = regexp.MustCompile(`(?m)\b[1-9][0-9]*\s+(credentials?|environment variables?)\b`)

var opencodeProviderEnv = map[string][]string{
	"opencode": {"OPENCODE_API_KEY"},
	"openai":   {"OPENAI_API_KEY"}, "anthropic": {"ANTHROPIC_API_KEY"},
	"google": {"GEMINI_API_KEY", "GOOGLE_API_KEY"}, "openrouter": {"OPENROUTER_API_KEY"},
	"deepseek": {"DEEPSEEK_API_KEY"}, "groq": {"GROQ_API_KEY"}, "xai": {"XAI_API_KEY"},
	"mistral": {"MISTRAL_API_KEY"}, "cohere": {"COHERE_API_KEY"},
}

var opencodeAPIKeyEnvVars = []string{
	"OPENCODE_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY",
	"OPENROUTER_API_KEY", "DEEPSEEK_API_KEY", "GROQ_API_KEY", "XAI_API_KEY", "MISTRAL_API_KEY", "COHERE_API_KEY",
}

type opencodeCredential struct {
	Type    string  `json:"type"`
	Key     string  `json:"key"`
	Access  *string `json:"access"`
	Refresh *string `json:"refresh"`
	Expires *int64  `json:"expires"`
}

func opencodeAuthEntries(data []byte, provider string, now time.Time) bool {
	var entries map[string]json.RawMessage
	if json.Unmarshal(data, &entries) != nil {
		return false
	}
	for id, raw := range entries {
		if strings.TrimSpace(id) == "" || (provider != "" && strings.TrimRight(id, "/") != provider) {
			continue
		}
		var credential opencodeCredential
		if json.Unmarshal(raw, &credential) != nil {
			continue
		}
		switch credential.Type {
		case "api":
			if strings.TrimSpace(credential.Key) != "" {
				return true
			}
		case "oauth":
			if credential.Access == nil || credential.Refresh == nil || credential.Expires == nil || *credential.Expires < 0 {
				continue
			}
			if strings.TrimSpace(*credential.Refresh) != "" || (strings.TrimSpace(*credential.Access) != "" && time.UnixMilli(*credential.Expires).After(now)) {
				return true
			}
		}
	}
	return false
}

// opencodeDBAuthStatus inspects an explicitly supplied database only. It is not
// a discovery source: native database paths are not part of OpenCode's auth contract.
func opencodeDBAuthStatus(ctx context.Context, path string) (ports.AgentAuthStatus, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro&_pragma=busy_timeout(1000)"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	probeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	known := false
	for _, query := range []string{
		"SELECT a.access_token,a.refresh_token,a.token_expiry FROM account_state s JOIN account a ON a.id = s.active_account_id",
		"SELECT access_token,refresh_token,token_expiry FROM control_account WHERE active = 1",
	} {
		rows, err := db.QueryContext(probeCtx, query)
		if err != nil {
			continue
		}
		known = true
		for rows.Next() {
			var access, refresh string
			var expiry sql.NullInt64
			if rows.Scan(&access, &refresh, &expiry) != nil {
				continue
			}
			if strings.TrimSpace(refresh) != "" || (strings.TrimSpace(access) != "" && (!expiry.Valid || time.UnixMilli(expiry.Int64).After(time.Now()))) {
				_ = rows.Close()
				return ports.AgentAuthStatusConfigured, true, nil
			}
		}
		_ = rows.Close()
	}
	return ports.AgentAuthStatusUnknown, known, ctx.Err()
}

// appendPermissionFlags maps AO's permission modes onto opencode's single
// approval flag. opencode exposes only --dangerously-skip-permissions (no
// graduated accept-edits/auto modes), so:
//   - bypass-permissions → --dangerously-skip-permissions
//   - default / accept-edits / auto → no flag. opencode resolves approvals from
//     its own `permission` config exactly as a normal launch.
func appendPermissionFlags(cmd *[]string, permissions ports.PermissionMode) {
	if ports.NormalizePermissionMode(permissions) == ports.PermissionModeBypassPermissions {
		*cmd = append(*cmd, "--dangerously-skip-permissions")
	}
}

const opencodeConfigEnvVar = "OPENCODE_CONFIG"

type opencodeInlineConfig struct {
	Schema string                           `json:"$schema,omitempty"`
	Agent  map[string]opencodeAgentSettings `json:"agent,omitempty"`
}

type opencodeAgentSettings struct {
	Mode   string `json:"mode,omitempty"`
	Prompt string `json:"prompt,omitempty"`
}

func opencodeConfigEnvPrefix(inlinePrompt, promptFile, sessionID string) ([]string, string, error) {
	if inlinePrompt == "" && promptFile == "" {
		return nil, "", nil
	}
	if promptFile == "" {
		return nil, "", fmt.Errorf("opencode: system prompt file required to build agent config")
	}
	agentName := opencodeAOAgentName(sessionID)
	prompt := inlinePrompt
	if prompt == "" {
		prompt = "{file:./" + filepath.Base(promptFile) + "}"
	}
	dir := filepath.Dir(promptFile)
	configPath := filepath.Join(dir, "opencode.json")
	config := opencodeInlineConfig{
		Schema: "https://opencode.ai/config.json",
		Agent: map[string]opencodeAgentSettings{
			agentName: {
				Mode:   "primary",
				Prompt: prompt,
			},
		},
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, "", err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", fmt.Errorf("opencode: create prompt config dir: %w", err)
	}
	if err := hookutil.AtomicWriteFile(configPath, data, 0o600); err != nil {
		return nil, "", fmt.Errorf("opencode: write prompt config: %w", err)
	}
	return []string{"env", opencodeConfigEnvVar + "=" + configPath}, agentName, nil
}

// PrepareACPConfigContent merges AO's standing instructions and any explicit
// bypass-permissions choice into OpenCode's inline runtime overlay. The user's
// OPENCODE_CONFIG path remains untouched, preserving its normal global, custom,
// project, provider, and credential configuration.
func PrepareACPConfigContent(
	existing, systemPrompt, sessionID string,
	permissions ports.PermissionMode,
) (string, error) {
	allowAll := ports.NormalizePermissionMode(permissions) == ports.PermissionModeBypassPermissions
	if strings.TrimSpace(systemPrompt) == "" && !allowAll {
		return existing, nil
	}
	config := map[string]any{}
	if strings.TrimSpace(existing) != "" {
		if err := json.Unmarshal([]byte(existing), &config); err != nil {
			return "", fmt.Errorf("opencode: decode OPENCODE_CONFIG_CONTENT: %w", err)
		}
	}
	if _, ok := config["$schema"]; !ok {
		config["$schema"] = "https://opencode.ai/config.json"
	}
	if strings.TrimSpace(systemPrompt) != "" {
		agents, ok := config["agent"].(map[string]any)
		if config["agent"] != nil && !ok {
			return "", fmt.Errorf("opencode: OPENCODE_CONFIG_CONTENT agent must be an object")
		}
		if agents == nil {
			agents = map[string]any{}
		}
		agentName := opencodeAOAgentName(sessionID)
		agents[agentName] = opencodeAgentSettings{Mode: "primary", Prompt: systemPrompt}
		config["agent"] = agents
		config["default_agent"] = agentName
	}
	if allowAll {
		// This is the native config equivalent of OpenCode's TUI auto-approval
		// flag. Other AO permission modes preserve the user's granular rules.
		config["permission"] = "allow"
	}
	data, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("opencode: encode ACP agent config: %w", err)
	}
	return string(data), nil
}

func opencodeAOAgentName(sessionID string) string {
	const fallback = "ao-system-prompt"
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return fallback
	}
	var b strings.Builder
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-',
			r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-_")
	if name == "" {
		return fallback
	}
	return "ao-" + name
}

// ResolveOpenCodeBinary returns the path to the opencode binary on this machine,
// searching PATH then a handful of well-known install locations (the install
// script's ~/.opencode/bin, Homebrew, npm global).
func ResolveOpenCodeBinary(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	if runtime.GOOS == "windows" {
		for _, name := range []string{"opencode.cmd", "opencode.exe", "opencode"} {
			if path, err := exec.LookPath(name); err == nil && path != "" {
				return path, nil
			}
		}
		candidates := []string{}
		if appData := os.Getenv("APPDATA"); appData != "" {
			candidates = append(candidates,
				filepath.Join(appData, "npm", "opencode.cmd"),
				filepath.Join(appData, "npm", "opencode.exe"),
			)
		}
		candidates = append(candidates, binaryutil.WindowsPackageManagerBinCandidates("opencode")...)
		if home, err := os.UserHomeDir(); err == nil {
			candidates = append(candidates, filepath.Join(home, ".opencode", "bin", "opencode.exe"))
		}
		for _, candidate := range candidates {
			if hookutil.IsExecutableFile(candidate) {
				return candidate, nil
			}
		}
		return "", fmt.Errorf("opencode: %w", ports.ErrAgentBinaryNotFound)
	}

	if path, err := exec.LookPath("opencode"); err == nil && path != "" {
		return path, nil
	}

	candidates := append([]string(nil), opencodeUnixPaths...)
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".npm-global", "bin", "opencode"),
			filepath.Join(home, ".npm", "bin", "opencode"),
			filepath.Join(home, ".local", "bin", "opencode"),
			filepath.Join(home, ".opencode", "bin", "opencode"),
		)
		candidates = append(candidates, binaryutil.UnixPackageManagerBinCandidates(home, "opencode")...)
		nodeManagerCandidates, err := binaryutil.UnixNodeManagerBinCandidates(ctx, home, "opencode")
		if err != nil {
			return "", err
		}
		candidates = append(candidates, nodeManagerCandidates...)
	}

	for _, candidate := range candidates {
		if hookutil.IsExecutableFile(candidate) {
			return candidate, nil
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}

	return "", fmt.Errorf("opencode: %w", ports.ErrAgentBinaryNotFound)
}

func (p *Plugin) opencodeBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}

	binary, err := ResolveOpenCodeBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}
