// Package amp implements the Amp agent adapter: launching new interactive Amp
// sessions and resuming sessions when a native Amp thread id is known.
//
// AO injects standing session instructions through a workspace-local Amp
// TypeScript plugin. Activity hooks and SessionInfo derivation will likely
// require more Amp-specific plugin work, so SessionInfo remains a no-op.
package amp

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

const adapterID = "amp"

// Plugin is the Amp agent adapter. It is safe for concurrent use; the binary
// path is resolved once and cached under binaryMu.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register Amp adapter.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          adapterID,
		Name:        "Amp",
		Description: "Run Amp worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec reports Amp's built-in operating modes. Amp deliberately
// chooses the underlying models for a mode, so AO exposes mode rather than a
// misleading raw-model field.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ConfigSpec{}, err
	}
	return ports.ConfigSpec{Fields: []ports.ConfigField{{
		Key:         "mode",
		Type:        ports.ConfigFieldEnum,
		Description: "Amp agent mode.",
		Enum:        []string{"low", "medium", "high", "ultra"},
	}}}, nil
}

// GetLaunchCommand builds the argv to start a new interactive Amp session:
//
//	amp
//
// Prompted worker tasks are delivered after startup by the session manager so
// Amp opens its normal interactive TUI instead of an execute-mode transcript.
// Amp has no documented --permission-mode flag: it runs tools without approval
// by default and configures permissions via settings and the Plugin API, not
// CLI flags. So cfg.Permissions is intentionally not translated to argv because
// Amp does not document that flag, and relying on hidden or permissively parsed
// options would make launches version-fragile. Amp also has no documented
// per-run system-prompt flag, so standing instructions are installed by
// GetAgentHooks as a workspace-local plugin.
//
// cfg.Config.Model is likewise intentionally not forwarded: Amp has no
// --model flag, model env var, or config key for a specific model string.
// It abstracts model choice behind an interactive "mode" system (low/medium/
// high/ultra, switched via Ctrl+O -> mode in the TUI), which is not a 1:1
// mapping onto a model name. Until Amp's CLI exposes a real per-model
// override, agentConfig.model has no effect when agent is "amp" — see
// https://github.com/AgentWrapper/agent-orchestrator/issues/2902.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	binary, err := p.ampBinary(ctx)
	if err != nil {
		return nil, err
	}

	cmd = []string{binary}
	if mode := strings.TrimSpace(cfg.Config.Mode); mode != "" {
		cmd = append(cmd, "--mode", mode)
	}
	return cmd, nil
}

// GetPromptDeliveryStrategy reports that AO should inject prompted Amp tasks
// into the interactive terminal after startup.
func (p *Plugin) GetPromptDeliveryStrategy(ctx context.Context, _ ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return ports.PromptDeliveryAfterStart, nil
}

// PromptReadinessHints waits briefly for Amp's interactive prompt before AO
// injects the worker's first task. Timeout falls back to delivery so startup
// copy changes do not permanently block a session.
func (p *Plugin) PromptReadinessHints(ctx context.Context, _ ports.LaunchConfig) (ports.PromptReadinessHints, error) {
	if err := ctx.Err(); err != nil {
		return ports.PromptReadinessHints{}, err
	}
	return ports.PromptReadinessHints{
		InitialDelay: 750 * time.Millisecond,
		Patterns: []string{
			"Type a message",
			"What can I help",
			">",
		},
		PollInterval: 200 * time.Millisecond,
		Timeout:      8 * time.Second,
		Lines:        80,
	}, nil
}

// GetRestoreCommand rebuilds the argv that continues an existing Amp session
// when plugin-derived native session metadata is available. Until that metadata
// exists, ok is false and callers fall back to fresh launch behavior.
//
// cfg.Config.Model is intentionally not forwarded here either, for the same
// reason it is dropped in GetLaunchCommand above.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) (cmd []string, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	agentSessionID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if agentSessionID == "" {
		return nil, false, nil
	}

	binary, err := p.ampBinary(ctx)
	if err != nil {
		return nil, false, err
	}
	// Capacity fits binary + --resume + sessionID.
	cmd = make([]string, 0, 3)
	cmd = append(cmd, binary)
	if mode := strings.TrimSpace(cfg.Config.Mode); mode != "" {
		cmd = append(cmd, "--mode", mode)
	}
	cmd = append(cmd, "--resume", agentSessionID)
	return cmd, true, nil
}

var ampBinarySpec = binaryutil.BinarySpec{
	Label:         "amp",
	Names:         []string{"amp"},
	WinNames:      []string{"amp.cmd", "amp.exe", "amp"},
	UnixPaths:     []string{"/usr/local/bin/amp", "/opt/homebrew/bin/amp"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("amp"),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "amp.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "amp.exe"}},
	},
}

// ResolveAmpBinary finds the `amp` binary, searching PATH then common install
// locations. It returns a wrapped ports.ErrAgentBinaryNotFound when Amp is absent.
func ResolveAmpBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, ampBinarySpec)
}

func (p *Plugin) ampBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}

	binary, err := ResolveAmpBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}
