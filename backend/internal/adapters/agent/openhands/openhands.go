// Package openhands integrates the user's OpenHands CLI (binary "openhands")
// with AO's terminal sessions.
//
// OpenHands is launched in its interactive Textual UI. AO seeds the first task
// with `--task=<prompt>`, which queues the text into the TUI rather than
// running headless, so the terminal stays input-ready after the task finishes.
// Native identity and activity come from OpenHands' Claude-compatible hook
// system (.openhands/hooks.json): the hook payload's session_id is the
// conversation id that `openhands --resume <id>` accepts.
//
// OpenHands has no system-prompt flag, and its only model override path
// (--override-with-envs) requires LLM_API_KEY and LLM_MODEL together, so this
// adapter exposes no model config key: the model stays in the user's own
// ~/.openhands/agent_settings.json. AO instructions are delivered through the
// UserPromptSubmit hook's additionalContext instead.
package openhands

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const adapterID = "openhands"

// Plugin supplies OpenHands CLI commands and hook integration. It is safe for
// concurrent use.
type Plugin struct {
	agentbase.Base

	binaryMu       sync.Mutex
	resolvedBinary string

	// versionMu guards verifiedBinary, the binary path whose version probe
	// already passed. The probe starts a Python process (several seconds), so
	// it runs once per resolved binary at launch rather than on every
	// readiness check.
	versionMu      sync.Mutex
	verifiedBinary string
}

// New returns an OpenHands CLI adapter.
func New() *Plugin { return &Plugin{} }

var _ ports.Agent = (*Plugin)(nil)
var _ adapters.Adapter = (*Plugin)(nil)

// Manifest describes the OpenHands CLI adapter.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:           adapterID,
		Name:         "OpenHands",
		Description:  "Run OpenHands CLI worker sessions.",
		Version:      "0.0.1",
		Capabilities: []adapters.Capability{adapters.CapabilityAgent},
	}
}

// GetLaunchCommand starts an interactive OpenHands conversation:
//
//	openhands [--llm-approve|--always-approve] [--task=<prompt>]
//
// OpenHands assigns its own conversation id and reports it through hooks, so
// cfg.NativeSessionID is ignored.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) ([]string, error) {
	cmd, err := p.command(ctx, cfg.Permissions, cfg.AllowedTools, cfg.DisallowedTools)
	if err != nil {
		return nil, err
	}
	appendTask(&cmd, cfg.Prompt)
	return cmd, nil
}

// GetRestoreCommand resumes the conversation id recorded by OpenHands hooks:
//
//	openhands [approval flag] --resume <id> [--task=<prompt>]
//
// ok is false until a hook has reported the native id, so callers fall back to
// a fresh launch.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) ([]string, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	id := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if id == "" {
		return nil, false, nil
	}
	cmd, err := p.command(ctx, cfg.Permissions, cfg.AllowedTools, cfg.DisallowedTools)
	if err != nil {
		return nil, false, err
	}
	cmd = append(cmd, "--resume", id)
	appendTask(&cmd, cfg.Prompt)
	return cmd, true, nil
}

// SessionInfo returns hook-derived native session metadata.
func (p *Plugin) SessionInfo(ctx context.Context, ref ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(ref)
	return info, ok, nil
}

func (p *Plugin) command(ctx context.Context, mode ports.PermissionMode, allow, deny []string) ([]string, error) {
	if len(allow) != 0 || len(deny) != 0 {
		return nil, fmt.Errorf("openhands: tool restrictions are not supported by this adapter")
	}
	bin, err := p.ResolveBinary(ctx)
	if err != nil {
		return nil, err
	}
	if err := p.verifyVersion(ctx, bin); err != nil {
		return nil, err
	}
	cmd := []string{bin}
	if flag := approvalFlag(mode); flag != "" {
		cmd = append(cmd, flag)
	}
	return cmd, nil
}

// approvalFlag maps an AO permission mode onto OpenHands' confirmation flags.
//
//   - default      → none: OpenHands confirms every action.
//   - accept-edits → none: OpenHands has no edits-only policy, and its
//     risk-based analyzer would also auto-run low-risk shell commands, so AO
//     keeps the stricter always-confirm policy instead of escalating.
//   - auto         → --llm-approve: an LLM security analyzer confirms only
//     predicted high-risk actions, the nearest match to a classifier mode.
//   - bypass       → --always-approve.
func approvalFlag(mode ports.PermissionMode) string {
	switch ports.NormalizePermissionMode(mode) {
	case ports.PermissionModeAuto:
		return "--llm-approve"
	case ports.PermissionModeBypassPermissions:
		return "--always-approve"
	default:
		return ""
	}
}

// appendTask seeds the TUI with the initial task. The `--task=` form keeps a
// prompt that starts with "-" from being parsed as an option.
func appendTask(cmd *[]string, prompt string) {
	if prompt != "" {
		*cmd = append(*cmd, "--task="+prompt)
	}
}

// binarySpec locates the openhands binary. Both `uv tool install openhands`
// and the standalone install script place it in ~/.local/bin (the script
// prefers /usr/local/bin when writable).
var binarySpec = binaryutil.BinarySpec{
	Label:         "openhands",
	Names:         []string{"openhands"},
	WinNames:      []string{"openhands.exe"},
	UnixPaths:     []string{"/usr/local/bin/openhands", "/opt/homebrew/bin/openhands"},
	UnixHomePaths: [][]string{{".local", "bin", "openhands"}},
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinHome, Parts: []string{".local", "bin", "openhands.exe"}},
	},
}

// ResolveBinary finds the openhands binary. It deliberately does not start the
// CLI: readiness checks bound resolution to a couple of seconds, and a Python
// CLI cold start exceeds that. The version is verified at launch instead.
func (p *Plugin) ResolveBinary(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()
	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}
	bin, err := binaryutil.ResolveBinary(ctx, binarySpec)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = bin
	return bin, nil
}

// ResolveBinaryPresence keeps initial inventory discovery free of child processes.
func (p *Plugin) ResolveBinaryPresence(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, binarySpec)
}

func (p *Plugin) verifyVersion(ctx context.Context, bin string) error {
	p.versionMu.Lock()
	defer p.versionMu.Unlock()
	if p.verifiedBinary == bin {
		return nil
	}
	if err := checkVersion(ctx, bin); err != nil {
		return err
	}
	p.verifiedBinary = bin
	return nil
}
