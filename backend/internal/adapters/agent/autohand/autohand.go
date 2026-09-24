// Package autohand implements the Autohand Code agent adapter: launching new
// command-mode sessions, resuming native sessions by id, installing AO's
// lifecycle hooks into Autohand's config, and reading hook-derived session info.
//
// Autohand ("autohand") is an autonomous coding agent with a non-interactive
// command mode (`autohand -p <prompt>` / positional prompt), native session
// resume (`autohand resume <sessionId>`), and a native hook/lifecycle system
// whose events (session-start, stop, permission-request, ...) AO maps onto
// activity states. See hooks.go for hook installation.
package autohand

import (
	"context"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const adapterID = "autohand"

const autohandNoBrowserEnv = "AUTOHAND_NO_BROWSER"

// Plugin is the Autohand agent adapter. It is safe for concurrent use; the
// binary path is resolved once and cached under binaryMu.
type Plugin struct {
	agentbase.Base

	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register Autohand adapter.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)

// AugmentRuntimeEnv keeps ordinary AO-managed sessions from opening an
// external browser when Autohand discovers that account authentication is
// required. The explicit agent-auth action still runs outside this session
// environment and retains Autohand's native browser login flow.
func (p *Plugin) AugmentRuntimeEnv(env map[string]string, _ string) {
	env[autohandNoBrowserEnv] = "1"
}

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          adapterID,
		Name:        "Autohand",
		Description: "Run Autohand worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec reports the per-project agent config keys Autohand understands.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	return agentbase.ModelConfigSpec(ctx, "Model override passed to `autohand --model`.")
}

// GetLaunchCommand builds the argv to start a new Autohand command-mode session,
// scoping the run to the workspace, applying the approval-mode flags, an
// optional model override, and optional system-prompt override, and passing
// the initial prompt as a positional argument after `--` so a prompt beginning
// with "-" is not read as a flag.
//
//	autohand [--path <workspace>] [<approval flags>] [--model <value>] [--sys-prompt <value>] [-- <prompt>]
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	binary, err := p.autohandBinary(ctx)
	if err != nil {
		return nil, err
	}

	cmd = []string{binary}
	appendWorkspaceFlag(&cmd, cfg.WorkspacePath)
	appendApprovalFlags(&cmd, cfg.Permissions)
	agentbase.AppendModelFlag(&cmd, cfg.Config, "--model")

	// Autohand's --sys-prompt accepts either an inline string or a file path,
	// auto-detected by the CLI; prefer inline instructions when AO has them.
	if cfg.SystemPrompt != "" {
		cmd = append(cmd, "--sys-prompt", cfg.SystemPrompt)
	} else if cfg.SystemPromptFile != "" {
		cmd = append(cmd, "--sys-prompt", cfg.SystemPromptFile)
	}

	if cfg.Prompt != "" {
		cmd = append(cmd, "--", cfg.Prompt)
	}

	return cmd, nil
}

// GetRestoreCommand rebuilds the argv that continues an existing Autohand
// session: `autohand resume [--path <workspace>] [--model <value>] <sessionId>`.
// ok is false when the hook-derived native session id has not landed yet, so
// callers can fall back to fresh launch behavior. Autohand's resume
// sub-command only accepts the workspace path, model, and session id, so
// approval and system-prompt flags are not re-applied here.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) (cmd []string, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	agentSessionID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if agentSessionID == "" {
		return nil, false, nil
	}

	binary, err := p.autohandBinary(ctx)
	if err != nil {
		return nil, false, err
	}

	cmd = make([]string, 0, 7)
	cmd = append(cmd, binary, "resume")
	appendWorkspaceFlag(&cmd, cfg.Session.WorkspacePath)
	agentbase.AppendModelFlag(&cmd, cfg.Config, "--model")
	cmd = append(cmd, agentSessionID)
	return cmd, true, nil
}

// SessionInfo surfaces Autohand hook-derived metadata. Metadata is intentionally
// nil: callers get the normalized fields directly.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

// appendWorkspaceFlag scopes the run to the given workspace path via --path.
func appendWorkspaceFlag(cmd *[]string, workspacePath string) {
	if strings.TrimSpace(workspacePath) != "" {
		*cmd = append(*cmd, "--path", workspacePath)
	}
}

// appendApprovalFlags maps AO's four permission modes onto Autohand's approval
// flags. Default emits no flag so Autohand resolves its starting mode from the
// user's own config (permissions.mode). Autohand has no distinct "accept-edits"
// mode, so it maps to --yes (auto-confirm risky actions) -- the least-privileged
// non-interactive option -- while auto/bypass map to --unrestricted.
func appendApprovalFlags(cmd *[]string, permissions ports.PermissionMode) {
	switch ports.NormalizePermissionMode(permissions) {
	case ports.PermissionModeDefault:
		// No flag: defer to the user's Autohand config/default behavior.
	case ports.PermissionModeAcceptEdits:
		*cmd = append(*cmd, "--yes")
	case ports.PermissionModeAuto:
		*cmd = append(*cmd, "--unrestricted")
	case ports.PermissionModeBypassPermissions:
		*cmd = append(*cmd, "--unrestricted")
	}
}

var autohandBinarySpec = binaryutil.BinarySpec{
	Label:         "autohand",
	Names:         []string{"autohand"},
	WinNames:      []string{"autohand.cmd", "autohand.exe", "autohand"},
	UnixPaths:     []string{"/usr/local/bin/autohand", "/opt/homebrew/bin/autohand"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("autohand"),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "autohand.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "autohand.exe"}},
		{Base: binaryutil.WinHome, Parts: []string{".local", "bin", "autohand.exe"}},
	},
}

// ResolveAutohandBinary returns the path to the autohand binary, or a wrapped
// ports.ErrAgentBinaryNotFound when it is absent.
func ResolveAutohandBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, autohandBinarySpec)
}

func (p *Plugin) autohandBinary(ctx context.Context) (string, error) {
	// Honor cancellation even on the cached path, where ResolveAutohandBinary
	// (which has its own ctx.Err() guard) is never reached.
	if err := ctx.Err(); err != nil {
		return "", err
	}

	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}

	binary, err := ResolveAutohandBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}
