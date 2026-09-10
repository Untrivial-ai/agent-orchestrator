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
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
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
// session: `[env OPENCODE_CONFIG=<ao-config>] opencode [--dangerously-skip-permissions] [--agent <ao-agent>] --session <agentSessionId>`.
// It re-applies the permission flag and the generated AO agent config (resume
// otherwise reverts to configured defaults). ok is false when the plugin-derived
// native session id has not landed yet, so callers fall back to fresh launch
// behavior — mirroring the Codex adapter.
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

// AuthStatus checks whether opencode has a configured provider credential.
// Missing credentials remain unknown because opencode can still run its public
// free models without a provider login.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	binary, err := p.opencodeBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if status, ok, err := opencodeLocalAuthStatus(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	} else if ok {
		return status, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	out, err := aoprocess.CommandContext(probeCtx, binary, "auth", "list").CombinedOutput()
	if probeCtx.Err() != nil {
		if probeCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			return ports.AgentAuthStatusUnknown, nil
		}
		return ports.AgentAuthStatusUnknown, probeCtx.Err()
	}
	text := strings.ToLower(string(out))
	if strings.Contains(text, "0 credentials") || strings.Contains(text, "no credentials") || strings.Contains(text, "not authenticated") {
		return ports.AgentAuthStatusUnknown, nil
	}
	if strings.Contains(text, "credential") && err == nil {
		return ports.AgentAuthStatusAuthorized, nil
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

var opencodeAPIKeyEnvVars = []string{
	"OPENCODE_API_KEY",
	"OPENAI_API_KEY",
	"ANTHROPIC_API_KEY",
	"GEMINI_API_KEY",
	"GOOGLE_API_KEY",
	"OPENROUTER_API_KEY",
	"DEEPSEEK_API_KEY",
	"GROQ_API_KEY",
	"XAI_API_KEY",
	"MISTRAL_API_KEY",
	"COHERE_API_KEY",
}

func opencodeLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	for _, name := range opencodeAPIKeyEnvVars {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return ports.AgentAuthStatusAuthorized, true, nil
		}
	}

	dataDir, ok := opencodeDataDir()
	if !ok {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	jsonStatus, jsonOK, err := opencodeAuthJSONStatus(filepath.Join(dataDir, "auth.json"))
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if jsonOK && jsonStatus == ports.AgentAuthStatusAuthorized {
		return jsonStatus, true, nil
	}
	if status, ok, err := opencodeDBAuthStatus(ctx, filepath.Join(dataDir, "opencode.db")); err != nil || ok {
		return status, ok, err
	}
	if jsonOK {
		return jsonStatus, true, nil
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

func opencodeDataDir() (string, bool) {
	if dataDir := strings.TrimSpace(os.Getenv("OPENCODE_DATA_DIR")); dataDir != "" {
		return dataDir, true
	}
	if dataHome := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); dataHome != "" {
		return filepath.Join(dataHome, "opencode"), true
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	return filepath.Join(home, ".local", "share", "opencode"), true
}

func opencodeAuthJSONStatus(path string) (ports.AgentAuthStatus, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return ports.AgentAuthStatusUnknown, true, nil
	}

	var entries map[string]json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if len(entries) == 0 {
		return ports.AgentAuthStatusUnknown, true, nil
	}
	for key, value := range entries {
		if strings.TrimSpace(key) == "" {
			continue
		}
		trimmed := strings.TrimSpace(string(value))
		if trimmed != "" && trimmed != "null" && trimmed != "{}" {
			return ports.AgentAuthStatusAuthorized, true, nil
		}
	}
	return ports.AgentAuthStatusUnknown, true, nil
}

func opencodeDBAuthStatus(ctx context.Context, path string) (ports.AgentAuthStatus, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return ports.AgentAuthStatusUnknown, false, nil
	} else if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=busy_timeout(1000)")
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	defer func() {
		_ = db.Close()
	}()

	authorized, known, err := opencodeDBHasAuthorizedAccount(ctx, db)
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if !known {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	if authorized {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	return ports.AgentAuthStatusUnknown, true, nil
}

func opencodeDBHasAuthorizedAccount(ctx context.Context, db *sql.DB) (authorized, known bool, err error) {
	for _, query := range []string{
		`SELECT COUNT(*) FROM account_state WHERE active_account_id IS NOT NULL AND trim(active_account_id) != ''`,
		`SELECT COUNT(*) FROM account WHERE trim(access_token) != ''`,
		`SELECT COUNT(*) FROM control_account WHERE active = 1 AND trim(access_token) != ''`,
	} {
		count, err := opencodeDBCount(ctx, db, query)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "no such table") {
				continue
			}
			return false, false, err
		}
		known = true
		if count > 0 {
			return true, true, nil
		}
	}
	return false, known, nil
}

func opencodeDBCount(ctx context.Context, db *sql.DB, query string) (int, error) {
	var count int
	if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
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
