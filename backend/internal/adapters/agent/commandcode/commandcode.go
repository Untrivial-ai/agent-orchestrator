// Package commandcode implements the Command Code agent adapter.
//
// Command Code is a terminal coding agent whose CLI binary is `cmd` (Windows
// alias `cmdc`, because `cmd` is the built-in shell there; the full name
// `command-code` works everywhere). It exposes Claude-Code-shaped project
// settings under .commandcode/, reads AGENTS.md memory tiers, and supports
// sessions, permission modes, model selection, and project hooks.
//
// Launch starts Command Code's interactive TUI and lets AO deliver the initial
// task through the terminal after startup. Command Code accepts a positional
// initial message, but AO injects after startup so a prompt that begins with
// "-" is never parsed as a flag. Its `-p`/`--print` mode runs a single headless
// turn and exits, so supervised AO sessions do not use it.
//
// AO installs workspace-local Command Code hooks to observe activity, capture
// the native session id for restore, and inject AO's standing instructions as
// SessionStart context without modifying the project's AGENTS.md.
package commandcode

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const adapterID = "command-code"

var commandCodeBinarySpec = binaryutil.BinarySpec{
	Label:         "command-code",
	Names:         []string{"cmd", "command-code"},
	WinNames:      []string{"cmdc.cmd", "cmdc.exe", "cmdc", "command-code.cmd", "command-code.exe", "command-code"},
	UnixPaths:     []string{"/usr/local/bin/cmd", "/opt/homebrew/bin/cmd", "/usr/local/bin/command-code", "/opt/homebrew/bin/command-code"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("cmd", []string{".commandcode", "bin", "cmd"}),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "cmdc.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "cmdc.exe"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "command-code.cmd"}},
		{Base: binaryutil.WinHome, Parts: []string{".commandcode", "bin", "cmdc.exe"}},
	},
}

// Plugin is the Command Code agent adapter.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register Command Code adapter.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          adapterID,
		Name:        "Command Code",
		Description: "Run Command Code interactive TUI sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec reports Command Code's optional model override.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	return agentbase.ModelConfigSpec(ctx, "Model override passed to `cmd --model`.")
}

// GetLaunchCommand builds `cmd --skip-onboarding --no-auto-update --trust
// [--permission-mode <mode> | --yolo] [--model <model>]` and leaves the prompt
// for AO's after-start terminal delivery.
//
// --skip-onboarding and --no-auto-update keep AO-managed runs non-interactive
// (no taste onboarding, no mid-session self-update); --trust skips the initial
// project-trust prompt, which would otherwise block every fresh AO worktree.
// Permissions map onto `--permission-mode`; bypass uses `--yolo`. Default omits
// the flag so Command Code uses its configured default.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	binary, err := p.binary(ctx)
	if err != nil {
		return nil, err
	}

	cmd = []string{binary, "--skip-onboarding", "--no-auto-update", "--trust"}
	appendPermissionFlags(&cmd, cfg.Permissions)
	agentbase.AppendModelFlag(&cmd, cfg.Config, "--model")
	return cmd, nil
}

// GetPromptDeliveryStrategy reports that AO should inject prompted tasks into
// the interactive terminal after startup.
func (p *Plugin) GetPromptDeliveryStrategy(ctx context.Context, _ ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return ports.PromptDeliveryAfterStart, nil
}

// PromptReadinessHints waits for Command Code's interactive UI before AO
// injects the worker's first task. Timeout falls back to delivery so a changed
// startup banner cannot permanently block spawning.
func (p *Plugin) PromptReadinessHints(ctx context.Context, _ ports.LaunchConfig) (ports.PromptReadinessHints, error) {
	if err := ctx.Err(); err != nil {
		return ports.PromptReadinessHints{}, err
	}
	return ports.PromptReadinessHints{
		InitialDelay: 750 * time.Millisecond,
		Patterns:     []string{"Command Code"},
		PollInterval: 200 * time.Millisecond,
		Timeout:      8 * time.Second,
		Lines:        80,
	}, nil
}

// GetRestoreCommand continues a Command Code session when AO has captured its
// native session id. ok=false otherwise, so the restore manager falls back to a
// fresh launch with AO's saved prompt.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) (cmd []string, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	agentSessionID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if agentSessionID == "" {
		return nil, false, nil
	}

	binary, err := p.binary(ctx)
	if err != nil {
		return nil, false, err
	}

	cmd = make([]string, 0, 8)
	cmd = append(cmd, binary, "--skip-onboarding", "--no-auto-update", "--trust")
	appendPermissionFlags(&cmd, cfg.Permissions)
	agentbase.AppendModelFlag(&cmd, cfg.Config, "--model")
	cmd = append(cmd, "--resume", agentSessionID)
	return cmd, true, nil
}

// SessionInfo surfaces metadata captured by AO's generic session machinery.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

// ResolveBinary resolves the `cmd`/`command-code` executable.
func (p *Plugin) ResolveBinary(ctx context.Context) (string, error) {
	return p.binary(ctx)
}

func (p *Plugin) binary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}
	binary, err := binaryutil.ResolveBinary(ctx, commandCodeBinarySpec)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}

// appendPermissionFlags maps AO permission modes onto Command Code's flags.
// Command Code has no separate "auto" tier, so accept-edits and auto both use
// `--permission-mode accept-edits`; bypass uses `--yolo`.
func appendPermissionFlags(cmd *[]string, permissions ports.PermissionMode) {
	switch ports.NormalizePermissionMode(permissions) {
	case ports.PermissionModeDefault:
		// No flag: defer to Command Code's own default (or user config).
	case ports.PermissionModeAcceptEdits, ports.PermissionModeAuto:
		*cmd = append(*cmd, "--permission-mode", "accept-edits")
	case ports.PermissionModeBypassPermissions:
		*cmd = append(*cmd, "--yolo")
	}
}
