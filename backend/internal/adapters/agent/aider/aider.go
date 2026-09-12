// Package aider implements the Aider agent adapter: launching interactive Aider
// worker sessions.
//
// Aider is a Tier C adapter: it has no full lifecycle hook surface, native
// session id, or resume-by-id mechanism, so hook installation, restore, and
// SessionInfo are intentionally no-ops. It does expose a completion notification
// command, which AO uses to report that Aider has returned to user input. The
// permission mapping is lossy because Aider lacks a graduated approval ladder or
// sandbox (see the comments on appendApprovalFlags).
package aider

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	adapterID                = "aider"
	aiderNotificationCommand = "ao hooks aider notification"
)

// Plugin is the Aider agent adapter. It is safe for concurrent use; the binary
// path is resolved once and cached under binaryMu.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register Aider adapter.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)
var _ ports.WaitingInputComposerReadiness = (*Plugin)(nil)

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          adapterID,
		Name:        "Aider",
		Description: "Run Aider worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec reports Aider's optional model override.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	return agentbase.ModelConfigSpec(ctx, "Model override passed to `aider --model`.")
}

// GetLaunchCommand builds the argv to start an interactive Aider session:
//
//	aider [permission flags] --no-check-update --no-stream --no-pretty --notifications --notifications-command <ao callback> [--read <context file>]
//
// Prompted tasks are delivered after startup by the session manager rather than
// via `-m`. Aider's `-m <prompt>` mode is one-shot: it runs the message and then
// exits, which makes AO workers disappear as soon as the answer is printed.
//
// Aider has no native system-prompt injection mechanism. AO's prompt file is
// supplied with --read as read-only context so the agent can see the standing
// instructions, but this is context fallback rather than system-message
// replacement. The --no-check-update --no-stream --no-pretty flags keep the
// terminal output stable in AO's captured-output context.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	binary, err := p.aiderBinary(ctx)
	if err != nil {
		return nil, err
	}

	cmd = []string{binary}
	appendApprovalFlags(&cmd, cfg.Permissions)
	agentbase.AppendModelFlag(&cmd, cfg.Config, "--model")
	cmd = append(cmd,
		"--no-check-update", "--no-stream", "--no-pretty",
		"--notifications", "--notifications-command", aiderNotificationCommand,
	)
	if cfg.SystemPromptFile != "" {
		cmd = append(cmd, "--read", cfg.SystemPromptFile)
	}
	// aider has no inline system-prompt mechanism. A cfg.SystemPrompt with no
	// file is intentionally dropped here rather than written to disk.
	return cmd, nil
}

// GetPromptDeliveryStrategy reports that AO should inject prompted Aider tasks
// into the interactive terminal after startup. Aider's `-m` mode exits after
// the single message completes.
func (p *Plugin) GetPromptDeliveryStrategy(ctx context.Context, _ ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return ports.PromptDeliveryAfterStart, nil
}

// normalizePermissionMode collapses an empty mode onto PermissionModeDefault so
// callers can switch over a stable set of values.
func normalizePermissionMode(mode ports.PermissionMode) ports.PermissionMode {
	if mode == "" {
		return ports.PermissionModeDefault
	}
	return mode
}

// appendApprovalFlags maps AO's permission modes onto Aider's flags. The mapping
// is lossy: Aider has no graduated approval ladder and no sandbox, so multiple
// AO modes collapse onto the same Aider behavior.
func appendApprovalFlags(cmd *[]string, mode ports.PermissionMode) {
	switch normalizePermissionMode(mode) {
	case ports.PermissionModeDefault:
		// No flags: Aider's interactive confirmation prompts apply. In headless
		// -m mode an unanswered confirm can hang; this is acceptable and
		// documented, deferring the choice to the user's own Aider config.
	case ports.PermissionModeAcceptEdits:
		// Apply edits without prompting but leave them uncommitted.
		*cmd = append(*cmd, "--yes-always", "--no-auto-commits")
	case ports.PermissionModeAuto:
		// Apply edits without prompting and keep Aider's default auto-commit.
		*cmd = append(*cmd, "--yes-always")
	case ports.PermissionModeBypassPermissions:
		// Lossy: Aider has no sandbox/bypass, so this is identical to auto.
		*cmd = append(*cmd, "--yes-always")
	default:
		// Unhandled/future modes: no flags, deferring to the user's Aider config.
	}
}

// ResolveAiderBinary finds the `aider` binary, searching PATH then common
// install locations. It returns "aider" as a last resort so callers get the
// shell's normal command-not-found behavior if Aider is absent.
func ResolveAiderBinary(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	if runtime.GOOS == "windows" {
		for _, name := range []string{"aider.exe", "aider.cmd", "aider"} {
			if path, err := exec.LookPath(name); err == nil && path != "" {
				return path, nil
			}
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}
		for _, candidate := range binaryutil.WindowsPackageManagerBinCandidates("aider") {
			if hookutil.IsExecutableFile(candidate) {
				return candidate, nil
			}
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}
		return "", fmt.Errorf("aider: %w", ports.ErrAgentBinaryNotFound)
	}

	if path, err := exec.LookPath("aider"); err == nil && path != "" {
		return path, nil
	}

	candidates := []string{
		"/usr/local/bin/aider",
		"/opt/homebrew/bin/aider",
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append([]string{filepath.Join(home, ".local", "bin", "aider")}, candidates...)
		candidates = append(candidates, binaryutil.UnixPackageManagerBinCandidates(home, "aider")...)
	}

	for _, candidate := range candidates {
		if hookutil.IsExecutableFile(candidate) {
			return candidate, nil
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}

	return "", fmt.Errorf("aider: %w", ports.ErrAgentBinaryNotFound)
}

func (p *Plugin) aiderBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}

	binary, err := ResolveAiderBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}
