// Package fx implements AO's adapter for Vercel Labs' fx coding agent.
//
// Interactive fx has no per-session system-prompt flag. TUI launches therefore
// have degraded standing-instruction semantics: AO does not replace either the
// repository's AGENTS.md or the user's ~/.fx/AGENTS.md to compensate.
package fx

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const adapterID = "fx"

// Plugin launches fx as an AO-managed interactive terminal process.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
	statusRunner   commandRunner
	statusTimeout  time.Duration
}

// New returns a ready-to-register fx adapter.
func New() *Plugin { return &Plugin{} }

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)
var _ ports.AgentBinaryResolver = (*Plugin)(nil)
var _ ports.AgentPromptReadinessProvider = (*Plugin)(nil)

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          adapterID,
		Name:        "fx",
		Description: "Run fx worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec exposes fx's optional free-form model override.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	return agentbase.ModelConfigSpec(ctx, "Model override passed through `FX_MODEL`.")
}

// GetLaunchCommand starts a new interactive fx session. The task is omitted
// because AO delivers it to the terminal after fx has started.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	binary, err := p.fxBinary(ctx)
	if err != nil {
		return nil, err
	}
	return commandWithOverrides(binary, cfg.Config, cfg.Permissions), nil
}

// GetPromptDeliveryStrategy tells AO to inject the initial task after startup.
func (p *Plugin) GetPromptDeliveryStrategy(ctx context.Context, _ ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return ports.PromptDeliveryAfterStart, nil
}

// PromptReadinessHints gives fx a short, bounded startup delay without using
// terminal text as lifecycle or readiness evidence.
func (p *Plugin) PromptReadinessHints(ctx context.Context, _ ports.LaunchConfig) (ports.PromptReadinessHints, error) {
	if err := ctx.Err(); err != nil {
		return ports.PromptReadinessHints{}, err
	}
	return ports.PromptReadinessHints{InitialDelay: 750 * time.Millisecond}, nil
}

// GetAgentHooks is intentionally a no-op. fx lifecycle integration uses its
// socket protocol and must not write either repository or user AGENTS.md files.
func (p *Plugin) GetAgentHooks(ctx context.Context, _ ports.WorkspaceHookConfig) error {
	return ctx.Err()
}

// GetRestoreCommand resumes the specific native fx session captured by AO.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) ([]string, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	agentSessionID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if agentSessionID == "" {
		return nil, false, nil
	}
	binary, err := p.fxBinary(ctx)
	if err != nil {
		return nil, false, err
	}
	cmd := commandWithOverrides(binary, cfg.Config, cfg.Permissions)
	return append(cmd, "resume", agentSessionID), true, nil
}

// SessionInfo surfaces metadata reported by fx's lifecycle integration.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

func commandWithOverrides(binary string, cfg ports.AgentConfig, permissions ports.PermissionMode) []string {
	env := make([]string, 0, 2)
	if model := strings.TrimSpace(cfg.Model); model != "" {
		env = append(env, "FX_MODEL="+model)
	}
	if mode := permissionMode(permissions); mode != "" {
		env = append(env, "FX_PERMISSION_MODE="+mode)
	}
	if len(env) == 0 {
		return []string{binary}
	}
	cmd := append([]string{"env"}, env...)
	return append(cmd, binary)
}

func permissionMode(mode ports.PermissionMode) string {
	switch ports.NormalizePermissionMode(mode) {
	case ports.PermissionModeAcceptEdits:
		return "ask"
	case ports.PermissionModeAuto:
		return "auto"
	case ports.PermissionModeBypassPermissions:
		return "full-access"
	default:
		return ""
	}
}
