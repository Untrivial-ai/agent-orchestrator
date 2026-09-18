// Package grok implements the Grok Build (xAI) agent adapter.
//
// Grok Build is xAI's terminal coding agent (binary "grok"). It supports
// Claude Code compatibility for hooks, skills, etc., so we write Claude-shaped
// hooks into .claude/settings.local.json with Grok-specific AO hook commands.
// Grok will pick them up via its compat layer.
//
// Launch starts Grok's interactive TUI without a prompt. AO delivers the
// initial task through the terminal after the TUI is ready because Grok's
// `-p`/`--single` mode is headless and bare positional arguments are parsed as
// subcommands.
// AO's standing instructions are appended with `--rules` so Grok's built-in
// coding-agent system prompt is preserved. Permission handling uses
// `--permission-mode`. We also pass `--no-auto-update` for AO-managed sessions
// (parity with Codex no-update).
// Restore prefers the hook-captured native session id via `-r <id>`.
//
// SessionInfo and title/summary flow through the shared hook metadata path.
package grok

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hooksjson"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var grokBinarySpec = binaryutil.BinarySpec{
	Label:         "grok",
	Names:         []string{"grok"},
	WinNames:      []string{"grok.cmd", "grok.exe", "grok"},
	UnixPaths:     []string{"/usr/local/bin/grok", "/opt/homebrew/bin/grok"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("grok", []string{".grok", "bin", "grok"}),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "grok.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "grok.exe"}},
		{Base: binaryutil.WinHome, Parts: []string{".grok", "bin", "grok.exe"}},
	},
}

const (
	grokClaudeSettingsDirName  = ".claude"
	grokClaudeSettingsFileName = "settings.local.json"
	grokHookCommandPrefix      = "ao hooks grok "
	grokHookTimeout            = 30
)

var grokStartupMatcher = "startup"

// grokManagedHooks is Claude Code's hook event shape with Grok-specific AO
// hook commands. Grok reads this file through its Claude compatibility layer,
// while `ao hooks grok` keeps activity and session metadata attributed to the
// Grok harness.
var grokManagedHooks = []hooksjson.HookSpec{
	{Event: "SessionStart", Matcher: &grokStartupMatcher, Command: grokHookCommandPrefix + "session-start"},
	{Event: "UserPromptSubmit", Command: grokHookCommandPrefix + "user-prompt-submit"},
	{Event: "PreToolUse", Command: grokHookCommandPrefix + "pre-tool-use"},
	{Event: "PostToolUse", Command: grokHookCommandPrefix + "post-tool-use"},
	{Event: "PostToolUseFailure", Command: grokHookCommandPrefix + "post-tool-use-failure"},
	{Event: "PermissionRequest", Command: grokHookCommandPrefix + "permission-request"},
	{Event: "Stop", Command: grokHookCommandPrefix + "stop"},
	{Event: "Notification", Command: grokHookCommandPrefix + "notification"},
	{Event: "SessionEnd", Command: grokHookCommandPrefix + "session-end"},
}

var grokHooks = hooksjson.Manager{
	Label:         "grok",
	CommandPrefix: grokHookCommandPrefix,
	Timeout:       grokHookTimeout,
	Path:          grokClaudeSettingsPath,
	Managed:       grokManagedHooks,
}

// Plugin is the Grok Build agent adapter.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register Grok adapter.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          "grok",
		Name:        "Grok Build",
		Description: "Run xAI Grok Build worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec reports Grok Build's optional model override.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	return agentbase.ModelConfigSpec(ctx, "Model override passed to `grok --model`.")
}

// GetLaunchCommand builds `grok --no-auto-update [--permission-mode <mode>]`
// and leaves the prompt for AO's after-start terminal delivery. Grok's
// `-p`/`--single` flag runs one headless turn and exits, while a bare prompt is
// parsed as a subcommand by current Grok versions.
//
// Uses --permission-mode (acceptEdits / auto / bypassPermissions) to match
// `grok -h` output. Default omits the flag so Grok uses its config.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	binary, err := p.grokBinary(ctx)
	if err != nil {
		return nil, err
	}

	cmd = []string{binary, "--no-auto-update"}
	appendApprovalFlags(&cmd, cfg.Permissions)
	agentbase.AppendModelFlag(&cmd, cfg.Config, "--model")

	systemPrompt, err := launchSystemPromptText(cfg)
	if err != nil {
		return nil, err
	}
	if systemPrompt != "" {
		cmd = append(cmd, "--rules", systemPrompt)
	}

	return cmd, nil
}

// GetPromptDeliveryStrategy reports that AO should inject prompted Grok tasks
// into the interactive terminal after startup.
func (p *Plugin) GetPromptDeliveryStrategy(ctx context.Context, _ ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return ports.PromptDeliveryAfterStart, nil
}

// PromptReadinessHints waits for Grok's interactive UI before AO injects the
// worker's first task. Timeout falls back to delivery so a changed startup
// banner cannot permanently block spawning.
func (p *Plugin) PromptReadinessHints(ctx context.Context, _ ports.LaunchConfig) (ports.PromptReadinessHints, error) {
	if err := ctx.Err(); err != nil {
		return ports.PromptReadinessHints{}, err
	}
	return ports.PromptReadinessHints{
		InitialDelay: 750 * time.Millisecond,
		Patterns:     []string{"Grok Build"},
		PollInterval: 200 * time.Millisecond,
		Timeout:      8 * time.Second,
		Lines:        80,
	}, nil
}

// GetAgentHooks installs Claude Code-shaped hooks because Grok Build has a
// Claude Code compatibility layer.
//
// Official docs (https://docs.x.ai/build/features/skills-plugins-marketplaces#claude-code-compatibility:~:text=tasks%20in%20parallel.-,Claude%20Code%20compatibility,-Grok%20is%20fully):
//
//	"Grok is fully compatible with Claude Code with zero configuration needed.
//	 Grok automatically reads Claude Code ... hooks ... alongside .grok/."
//
// This means Grok will pick up the .claude/settings.local.json in the
// worktree. The hook payloads for SessionStart / UserPromptSubmit / Stop etc.
// are compatible, and the installed commands use the Grok harness token so AO
// persists Grok's native session id under the Grok session.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	return grokHooks.Install(ctx, cfg.WorkspacePath)
}

// UninstallHooks removes the Claude Code-compatible AO hooks Grok uses.
func (p *Plugin) UninstallHooks(ctx context.Context, workspacePath string) error {
	return grokHooks.Uninstall(ctx, workspacePath)
}

// AreHooksInstalled reports whether the Grok Claude Code-compatible AO
// hooks are present for this Grok workspace.
func (p *Plugin) AreHooksInstalled(ctx context.Context, workspacePath string) (bool, error) {
	return grokHooks.AreInstalled(ctx, workspacePath)
}

// GetRestoreCommand resumes a prior grok session by its captured id, building
// `grok --no-auto-update [--permission-mode <mode>] -r <agentSessionId>`
// when we have a hook-captured native id. ok=false otherwise, so the restore
// manager falls back to a fresh launch with AO's saved system prompt.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) (cmd []string, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	agentSessionID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if agentSessionID == "" {
		return nil, false, nil
	}

	binary, err := p.grokBinary(ctx)
	if err != nil {
		return nil, false, err
	}

	cmd = make([]string, 0, 4)
	cmd = append(cmd, binary, "--no-auto-update")
	appendApprovalFlags(&cmd, cfg.Permissions)
	agentbase.AppendModelFlag(&cmd, cfg.Config, "--model")
	systemPrompt, err := restoreSystemPromptText(cfg)
	if err != nil {
		return nil, false, err
	}
	if systemPrompt != "" {
		cmd = append(cmd, "--rules", systemPrompt)
	}
	cmd = append(cmd, "-r", agentSessionID)
	return cmd, true, nil
}

// SessionInfo reads hook-derived metadata under AO's normalized keys ("title",
// "summary", "agentSessionId").
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

// ResolveGrokBinary finds the `grok` binary (xAI Grok Build CLI).
func ResolveGrokBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, grokBinarySpec)
}

func (p *Plugin) grokBinary(ctx context.Context) (string, error) {
	if path, shared, err := p.DiscoveredBinary(ctx, p.Manifest().ID, ports.BinaryResolveLaunch); shared {
		return path, err
	}

	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}

	binary, err := ResolveGrokBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}

func grokClaudeSettingsPath(workspacePath string) string {
	return filepath.Join(workspacePath, grokClaudeSettingsDirName, grokClaudeSettingsFileName)
}

func appendApprovalFlags(cmd *[]string, permissions ports.PermissionMode) {
	switch ports.NormalizePermissionMode(permissions) {
	case ports.PermissionModeDefault:
		// No flag: defer to the user's ~/.grok/config.toml (or default behavior).
	case ports.PermissionModeAcceptEdits:
		*cmd = append(*cmd, "--permission-mode", "acceptEdits")
	case ports.PermissionModeAuto:
		*cmd = append(*cmd, "--permission-mode", "auto")
	case ports.PermissionModeBypassPermissions:
		*cmd = append(*cmd, "--permission-mode", "bypassPermissions")
	}
}

// Grok's --rules flag accepts inline text only. AO usually supplies both inline
// text and an AO-owned file; read the file only when inline instructions are not
// available.
func launchSystemPromptText(cfg ports.LaunchConfig) (string, error) {
	return systemPromptTextFrom(cfg.SystemPrompt, cfg.SystemPromptFile)
}

func restoreSystemPromptText(cfg ports.RestoreConfig) (string, error) {
	return systemPromptTextFrom(cfg.SystemPrompt, cfg.SystemPromptFile)
}

func systemPromptTextFrom(inline, file string) (string, error) {
	if inline != "" {
		return inline, nil
	}
	if file == "" {
		return "", nil
	}
	data, err := os.ReadFile(file) //nolint:gosec // path is AO-owned launch config
	if err != nil {
		return "", fmt.Errorf("grok: read system prompt file: %w", err)
	}
	return strings.TrimRight(string(data), "\n"), nil
}
