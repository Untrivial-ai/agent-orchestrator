// Package claudecode implements the Claude Code agent adapter.
//
// It builds the argv to launch `claude` as an interactive session inside a
// session's worktree, installs worktree-local hooks that report normalized
// session metadata (native id, title, summary) back into AO's store,
// and supports resume: GetLaunchCommand pins either AO's requested native UUID
// or a stable AO-session-derived fallback so GetRestoreCommand can rebuild
// `claude --resume <uuid>`. SessionInfo reads the
// hook-captured metadata from the store — it does not parse transcripts.
// GetConfigSpec remains a no-op (no agent-specific config keys yet).
//
// Claude Code starts an interactive session by default (no -p/--print), which
// is exactly what AO wants: a live agent the user can attach to in the
// browser terminal or via `tmux attach`. The initial task prompt is passed
// as the positional argument; the orchestrator system prompt (if any) is
// appended to Claude's default system prompt so its built-in coding
// instructions are preserved.
package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
	"github.com/aoagents/agent-orchestrator/backend/pkg/agentruntime"
)

const (
	// adapterID is the registry id and the value users pass to
	// `ao spawn --agent`.
	adapterID = "claude-code"
)

// Plugin is the Claude Code agent adapter. It is safe for concurrent use; the
// binary path is resolved once and cached under binaryMu.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register Claude Code adapter.
func New() *Plugin {
	return &Plugin{}
}

// EmitsSubmitActivity signals that Claude Code fires a user-prompt-submit hook
// under AO's launch, so Activity.State can flip to active after a prompt is
// accepted. See ports.SubmitActivitySignaler.
func (p *Plugin) EmitsSubmitActivity() bool { return true }

// EmitsBlockedActivity signals that Claude Code fires both pre- and post-tool
// hooks, so Activity.State can flip to blocked mid-turn on a permission dialog
// and the guarded send loop can clear it once the tool completes. Only
// claude-code (and its hook-delegators) carry this trio; see
// ports.BlockedActivitySignaler.
func (p *Plugin) EmitsBlockedActivity() bool { return true }

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)
var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.EmptyComposerDetector = (*Plugin)(nil)
var _ ports.AgentInterfaceHandoff = (*Plugin)(nil)
var _ ports.AgentInterfaceHandoffHistoryProbe = (*Plugin)(nil)
var _ ports.TerminalSurfaceInspector = (*Plugin)(nil)

// ComposerIsEmpty recognizes Claude Code's blank composer or its dim
// placeholder. Claude renders normal, non-dim status chrome below a bordered
// composer, so inspect that bounded region before using the footer-free fallback.
// A permission-menu selection and normal typed text are rejected.
func (p *Plugin) ComposerIsEmpty(output string) bool {
	return p.InspectTerminalSurface(output).Composer == ports.TerminalComposerEmpty
}

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          adapterID,
		Name:        "Claude Code",
		Description: "Run Claude Code worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// permissionConfigEnum lists the permission modes the "permissions" config key
// accepts. It mirrors the ports.PermissionMode constants so a project's stored
// config validates against the same vocabulary the launch command maps.
var permissionConfigEnum = []string{
	string(ports.PermissionModeDefault),
	string(ports.PermissionModeAcceptEdits),
	string(ports.PermissionModeAuto),
	string(ports.PermissionModeBypassPermissions),
}

// GetConfigSpec reports the per-project agent config keys Claude Code
// understands: a model override and a starting permission mode.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ConfigSpec{}, err
	}
	return ports.ConfigSpec{
		Fields: []ports.ConfigField{
			{
				Key:         "model",
				Type:        ports.ConfigFieldString,
				Description: "Model override passed to `claude --model` (e.g. claude-opus-4-5).",
			},
			{
				Key:         "permissions",
				Type:        ports.ConfigFieldEnum,
				Description: "Starting permission mode.",
				Enum:        permissionConfigEnum,
			},
		},
	}, nil
}

// GetLaunchCommand builds the argv to start an interactive Claude Code
// session. Shape:
//
//	claude [--session-id <uuid>] \
//	       [--permission-mode <mode>] \
//	       [--append-system-prompt-file <path> | --append-system-prompt <text>] \
//	       [-- <prompt>]
//
// --session-id pins Claude's native session UUID to LaunchConfig.NativeSessionID
// when AO requests a distinct provider conversation, otherwise to a value
// derived from the AO session id for the legacy one-conversation path. This
// makes the session resumable later (see
// GetRestoreCommand) and its transcript is locatable (see SessionInfo) without
// a separate capture step.
//
// <mode> is acceptEdits, auto, or bypassPermissions. AO's "default"
// mode emits no --permission-mode flag, so Claude's TUI resolves the starting
// mode from ~/.claude/settings.json exactly as a normal launch.
//
// The prompt is passed after `--` so a prompt beginning with "-" is not
// mistaken for a flag.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	// Defense-in-depth: the project service validates on write, but re-check
	// here so a config written by any other path can't launch a bad command.
	if err := cfg.Config.Validate(); err != nil {
		return nil, fmt.Errorf("claude-code: %w", err)
	}

	binary, err := p.claudeBinary(ctx)
	if err != nil {
		return nil, err
	}

	// A project's configured permissions drive the starting mode; the explicit
	// LaunchConfig.Permissions wins when set so a per-spawn override still takes
	// precedence over the stored project default.
	permissions := cfg.Permissions
	if permissions == "" {
		permissions = cfg.Config.Permissions
	}
	return agentruntime.BuildLaunchCommand(agentruntime.LaunchConfig{
		Harness:          agentruntime.HarnessClaudeCode,
		Binary:           binary,
		SessionID:        cfg.SessionID,
		NativeSessionID:  cfg.NativeSessionID,
		Model:            cfg.Config.Model,
		Prompt:           cfg.Prompt,
		SystemPrompt:     cfg.SystemPrompt,
		SystemPromptFile: cfg.SystemPromptFile,
		Permission:       agentruntime.PermissionPolicy(permissions),
		AllowedTools:     cfg.AllowedTools,
		DisallowedTools:  cfg.DisallowedTools,
	})
}

// PreLaunch is an optional capability the spawn engine invokes (via type
// assertion) immediately before creating the session. Claude Code shows a
// blocking "do you trust this folder?" dialog the first time it runs in any
// directory. Every AO worktree is a fresh path, so without this the
// agent would hang at that prompt with no one to answer it.
//
// An AO worktree is derived from the repo the user is already running
// AO in, so it is inherently trusted. PreLaunch records that trust in
// ~/.claude.json before launch, additively and atomically, so it cannot
// clobber a concurrently-running Claude instance's config.
func (p *Plugin) PreLaunch(ctx context.Context, cfg ports.LaunchConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cfg.WorkspacePath == "" {
		return nil
	}
	cfgPath, err := claudeConfigPath()
	if err != nil {
		return err
	}
	return ensureWorkspaceTrusted(cfgPath, cfg.WorkspacePath)
}

// GetRestoreCommand rebuilds the argv that continues an existing Claude Code
// session: `claude [--permission-mode <mode>] --resume <agentSessionId>`. It
// prefers the hook-captured native session id from
// cfg.Session.Metadata["agentSessionId"]; for sessions created before hooks
// captured it, it falls back to the deterministic UUID AO pins via
// --session-id at launch. ok is false only when neither is available, so the
// caller fresh-spawns. The command re-applies the permission mode and current
// standing system instructions. When Prompt is present it is passed as the
// resume-time user turn, avoiding a fragile terminal paste into Claude's TUI.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) (cmd []string, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if _, ok := agentruntime.RestoreIdentity(
		agentruntime.HarnessClaudeCode,
		cfg.Session.ID,
		cfg.Session.Metadata,
	); !ok {
		return nil, false, nil
	}

	binary, err := p.claudeBinary(ctx)
	if err != nil {
		return nil, false, err
	}
	return agentruntime.BuildRestoreCommand(agentruntime.RestoreConfig{
		Harness:          agentruntime.HarnessClaudeCode,
		Binary:           binary,
		SessionID:        cfg.Session.ID,
		Metadata:         cfg.Session.Metadata,
		Prompt:           cfg.Prompt,
		SystemPrompt:     cfg.SystemPrompt,
		SystemPromptFile: cfg.SystemPromptFile,
		Permission:       agentruntime.PermissionPolicy(cfg.Permissions),
		AllowedTools:     cfg.AllowedTools,
		DisallowedTools:  cfg.DisallowedTools,
	})
}

// SessionInfo surfaces the normalized session metadata that the Claude Code
// hooks persisted into AO's store: the native session id, the title (the
// first user prompt), and the summary (the final assistant message). It reads
// only from session.Metadata — never from transcript files — and returns
// ok=false when none of those fields are present. Metadata is intentionally nil:
// there is no Claude-specific field callers need beyond the normalized ones.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

// NativeConversationID bridges Claude Code's terminal and ACP surfaces. Both
// use the same native Claude session UUID. AO terminal sessions pin a
// deterministic UUID, while Chat persists the id returned by claude-agent-acp.
func (p *Plugin) NativeConversationID(
	ctx context.Context,
	session ports.SessionRef,
	currentMode domain.SessionMode,
	providerConversationID string,
) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if currentMode == domain.SessionModeChat {
		id := strings.TrimSpace(providerConversationID)
		return id, id != "", nil
	}
	id := strings.TrimSpace(session.Metadata[ports.MetadataKeyAgentSessionID])
	if id == "" && session.ID != "" {
		id = claudeSessionUUID(session.ID)
	}
	return id, id != "", nil
}

// NativeConversationExists distinguishes Claude's reserved session UUID from
// a conversation that Claude has actually persisted. Claude Code accepts
// --session-id before the first prompt, but does not create its JSONL transcript
// until the conversation has content. Passing that reserved-but-empty UUID to
// either `claude --resume` or ACP session/load returns "No conversation found".
//
// The Agent SDK documents local transcripts at
// ~/.claude/projects/<project-key>/<session-id>.jsonl (or beneath
// CLAUDE_CONFIG_DIR). We only test for a non-empty top-level transcript; AO does
// not parse or project provider files here.
func (p *Plugin) NativeConversationExists(
	ctx context.Context,
	_ ports.SessionRef,
	nativeConversationID string,
	env map[string]string,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	id := strings.TrimSpace(nativeConversationID)
	if !isUUID(id) {
		return false, nil
	}
	configDir := strings.TrimSpace(env["CLAUDE_CONFIG_DIR"])
	if configDir == "" {
		configDir = strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	}
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false, fmt.Errorf("claude-code: resolve transcript root: %w", err)
		}
		configDir = filepath.Join(home, ".claude")
	}
	projectsDir := filepath.Join(configDir, "projects")
	projects, err := os.ReadDir(projectsDir)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claude-code: read transcript root %s: %w", projectsDir, err)
	}
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if !project.IsDir() {
			continue
		}
		info, err := os.Stat(filepath.Join(projectsDir, project.Name(), id+".jsonl"))
		switch {
		case err == nil && info.Mode().IsRegular() && info.Size() > 0:
			return true, nil
		case err == nil, os.IsNotExist(err):
			continue
		default:
			return false, fmt.Errorf("claude-code: inspect transcript for %s: %w", id, err)
		}
	}
	return false, nil
}

func isUUID(value string) bool {
	_, err := uuid.Parse(value)
	return err == nil
}

// AuthStatus checks Claude Code's local authentication state without starting a
// session.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	binary, err := p.claudeBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if status, ok, err := claudeLocalAuthStatus(ctx); err != nil {
		return ports.AgentAuthStatusUnknown, err
	} else if ok {
		return status, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	out, err := aoprocess.CommandContext(probeCtx, binary, "auth", "status").CombinedOutput()
	if probeCtx.Err() != nil {
		return ports.AgentAuthStatusUnknown, probeCtx.Err()
	}
	if status, ok := claudeAuthStatusFromOutput(out); ok {
		return status, nil
	}
	// An unfamiliar non-zero result is not affirmative evidence of missing
	// credentials. Keep this advisory probe unknown and let launch report the
	// authoritative failure.
	_ = err
	return ports.AgentAuthStatusUnknown, nil
}

func claudeAuthStatusFromOutput(out []byte) (ports.AgentAuthStatus, bool) {
	start := bytes.IndexByte(out, '{')
	end := bytes.LastIndexByte(out, '}')
	if start < 0 || end < start {
		return ports.AgentAuthStatusUnknown, false
	}
	var status struct {
		LoggedIn bool `json:"loggedIn"`
	}
	if json.Unmarshal(out[start:end+1], &status) != nil {
		return ports.AgentAuthStatusUnknown, false
	}
	if status.LoggedIn {
		return ports.AgentAuthStatusAuthorized, true
	}
	return ports.AgentAuthStatusUnauthorized, true
}

func claudeLocalAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) != "" {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	cfgPath, err := claudeConfigPath()
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	return claudeConfigAuthStatus(cfgPath)
}

func claudeConfigAuthStatus(path string) (ports.AgentAuthStatus, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return ports.AgentAuthStatusUnknown, false, err
	}
	var hasSubscription bool
	if raw := root["hasAvailableSubscription"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &hasSubscription)
	}
	var userID string
	if raw := root["userID"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &userID)
	}
	if strings.TrimSpace(userID) != "" {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	var oauthAccount map[string]any
	if raw := root["oauthAccount"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &oauthAccount); err != nil {
			return ports.AgentAuthStatusUnknown, false, err
		}
	}
	if len(oauthAccount) == 0 {
		return ports.AgentAuthStatusUnknown, false, nil
	}
	if hasSubscription {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	if accountUUID, ok := oauthAccount["accountUuid"].(string); ok && strings.TrimSpace(accountUUID) != "" {
		return ports.AgentAuthStatusAuthorized, true, nil
	}
	return ports.AgentAuthStatusUnknown, false, nil
}

// claudeSessionUUID maps an AO session id onto a stable Claude Code
// session UUID via UUIDv5 over a fixed namespace, so the same AO session
// always resolves to the same Claude session.
func claudeSessionUUID(aoSessionID string) string {
	return agentruntime.ClaudeSessionID(aoSessionID)
}

// SessionUUID maps an AO session id onto the native Claude Code session UUID
// used by --session-id and --resume.
func SessionUUID(aoSessionID string) string {
	return claudeSessionUUID(aoSessionID)
}

// claudeBinarySpec locates the claude binary: PATH first, then the native
// installer's locations, npm global, Homebrew, and the claude-managed dir.
var claudeBinarySpec = binaryutil.BinarySpec{
	Label:         "claude",
	Names:         []string{"claude"},
	WinNames:      []string{"claude.exe", "claude.cmd", "claude"},
	UnixPaths:     []string{"/usr/local/bin/claude", "/opt/homebrew/bin/claude"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("claude", []string{".claude", "local", "claude"}),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "node_modules", "@anthropic-ai", "claude-code", "bin", "claude.exe"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "claude.exe"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "claude.cmd"}},
	},
}

// ResolveClaudeBinary returns the path to the claude binary, or a wrapped
// ports.ErrAgentBinaryNotFound when it is absent.
func ResolveClaudeBinary(ctx context.Context) (string, error) {
	binary, err := binaryutil.ResolveBinary(ctx, claudeBinarySpec)
	if err != nil {
		return "", err
	}
	return resolveNativeWindowsClaude(binary, runtime.GOOS), nil
}

// resolveNativeWindowsClaude unwraps the npm-generated command shim to the
// native executable shipped by Claude Code. The ACP bridge passes this path to
// Node child_process.spawn, which cannot execute .cmd files directly on current
// Node releases.
func resolveNativeWindowsClaude(path, goos string) string {
	if goos != "windows" || !strings.EqualFold(filepath.Ext(path), ".cmd") {
		return path
	}
	for _, candidate := range windowsNativeClaudeCandidatesForShim(path) {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return path
}

func windowsNativeClaudeCandidatesForShim(shim string) []string {
	dir := filepath.Dir(shim)
	return []string{
		filepath.Join(dir, "claude.exe"),
		filepath.Join(dir, "node_modules", "@anthropic-ai", "claude-code", "bin", "claude.exe"),
	}
}

func (p *Plugin) claudeBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}

	binary, err := ResolveClaudeBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}

// claudeConfigPath returns the path to Claude Code's global config file,
// ~/.claude.json.
func claudeConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("claude-code: resolve home directory: %w", err)
	}
	return filepath.Join(home, ".claude.json"), nil
}

// ensureWorkspaceTrusted records workspacePath as trusted in Claude Code's
// config so the interactive trust dialog does not block a spawned session.
//
// It is additive and concurrency-safe: it reads the existing config, sets
// only projects[workspacePath].hasTrustDialogAccepted = true (preserving the
// rest of the entry and every other project), and writes back via a
// temp-file + atomic rename. If the path is already trusted, it makes no
// write at all. A missing config file is treated as an empty one.
// claudeTrustMu serializes ensureWorkspaceTrusted within the process. Concurrent
// spawns to different workspaces otherwise read the same ~/.claude.json snapshot
// and the last rename drops the other's trust entry.
var claudeTrustMu sync.Mutex

func ensureWorkspaceTrusted(configPath, workspacePath string) error {
	claudeTrustMu.Lock()
	defer claudeTrustMu.Unlock()

	root := map[string]any{}
	data, err := os.ReadFile(configPath)
	switch {
	case err == nil:
		if len(data) > 0 {
			if err := json.Unmarshal(data, &root); err != nil {
				return fmt.Errorf("claude-code: parse %s: %w", configPath, err)
			}
		}
	case os.IsNotExist(err):
		// Treat as empty config; we'll create it.
	default:
		return fmt.Errorf("claude-code: read %s: %w", configPath, err)
	}

	projects, _ := root["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
		root["projects"] = projects
	}

	entry, _ := projects[workspacePath].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
		projects[workspacePath] = entry
	}

	if trusted, ok := entry["hasTrustDialogAccepted"].(bool); ok && trusted {
		// Already trusted — no write needed, so no race window at all.
		return nil
	}
	entry["hasTrustDialogAccepted"] = true

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("claude-code: encode %s: %w", configPath, err)
	}

	// Atomic write: temp file in the same directory, then rename. Matches
	// how Claude Code itself updates this file, so concurrent updates are
	// last-writer-wins rather than corrupting.
	dir := filepath.Dir(configPath)
	tmp, err := os.CreateTemp(dir, ".claude.json.tmp-*")
	if err != nil {
		return fmt.Errorf("claude-code: create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed

	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("claude-code: write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("claude-code: close temp config: %w", err)
	}
	if err := os.Rename(tmpName, configPath); err != nil {
		return fmt.Errorf("claude-code: replace config: %w", err)
	}
	return nil
}
