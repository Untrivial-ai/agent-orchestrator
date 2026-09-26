// Package mimocode implements the MiMo Code terminal-agent adapter.
package mimocode

import (
	"context"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const adapterID = "mimo-code"

// Plugin adapts the MiMo Code CLI to AO's terminal-agent contract.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New constructs a MiMo Code adapter.
func New() *Plugin { return &Plugin{} }

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)
var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentBinaryResolver = (*Plugin)(nil)
var _ ports.AgentBinaryResolutionInvalidator = (*Plugin)(nil)
var _ ports.SemanticMessageAcceptanceSignaler = (*Plugin)(nil)

// EmitsSemanticMessageAcceptance reports that MiMo emits accepted prompt events.
func (p *Plugin) EmitsSemanticMessageAcceptance() bool { return true }

// Manifest describes the MiMo Code adapter and its capabilities.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID: adapterID, Name: "MiMo Code", Description: "Run MiMo Code worker sessions.", Version: "0.0.1",
		Capabilities: []adapters.Capability{adapters.CapabilityAgent},
	}
}

// GetConfigSpec describes MiMo Code's model override setting.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	return agentbase.ModelConfigSpec(ctx, "Model override passed to `mimo --model`.")
}

// GetLaunchCommand builds the argv for a fresh MiMo Code session.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) ([]string, error) {
	binary, err := p.mimoBinary(ctx)
	if err != nil {
		return nil, err
	}
	cmd := []string{binary, "--trust"}
	appendPermissionFlag(&cmd, cfg.Permissions)
	agentbase.AppendModelFlag(&cmd, cfg.Config, "--model")
	if usesAOAgent(cfg.Permissions, cfg.SystemPrompt, cfg.SystemPromptFile) {
		cmd = append(cmd, "--agent", mimoAgentName(cfg.SessionID))
	}
	if cfg.Prompt != "" {
		cmd = append(cmd, "--prompt", cfg.Prompt)
	}
	return cmd, nil
}

// GetRestoreCommand builds the argv for restoring an exact MiMo Code session.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) ([]string, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	nativeID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if !validNativeSessionID(nativeID) {
		return nil, false, nil
	}
	binary, err := p.mimoBinary(ctx)
	if err != nil {
		return nil, false, err
	}
	cmd := []string{binary, "--trust"}
	appendPermissionFlag(&cmd, cfg.Permissions)
	agentbase.AppendModelFlag(&cmd, cfg.Config, "--model")
	if usesAOAgent(cfg.Permissions, cfg.SystemPrompt, cfg.SystemPromptFile) {
		cmd = append(cmd, "--agent", mimoAgentName(cfg.Session.ID))
	}
	cmd = append(cmd, "--session", nativeID)
	if cfg.Prompt != "" {
		cmd = append(cmd, "--prompt", cfg.Prompt)
	}
	return cmd, true, nil
}

// SessionInfo returns provider session metadata captured by AO's hook events.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

func validNativeSessionID(id string) bool {
	return strings.HasPrefix(id, "ses_") && len(id) > len("ses_")
}

func usesAOAgent(mode ports.PermissionMode, inlinePrompt, promptFile string) bool {
	return ports.NormalizePermissionMode(mode) == ports.PermissionModeAcceptEdits || inlinePrompt != "" || promptFile != ""
}

func appendPermissionFlag(cmd *[]string, mode ports.PermissionMode) {
	switch ports.NormalizePermissionMode(mode) {
	case ports.PermissionModeAuto, ports.PermissionModeBypassPermissions:
		*cmd = append(*cmd, "--dangerously-skip-permissions")
	}
}

func mimoAgentName(sessionID string) string {
	const fallback = "ao-system-prompt"
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return fallback
	}
	var b strings.Builder
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
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

var mimoBinarySpec = binaryutil.BinarySpec{
	Label:         "mimo",
	Names:         []string{"mimo"},
	WinNames:      []string{"mimo.cmd", "mimo.exe", "mimo"},
	UnixPaths:     []string{"/usr/local/bin/mimo", "/opt/homebrew/bin/mimo"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("mimo"),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "mimo.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "mimo.exe"}},
	},
}

// ResolveMiMoCodeBinary locates the user-installed MiMo Code executable.
func ResolveMiMoCodeBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, mimoBinarySpec)
}

// ResolveBinary locates and caches the MiMo Code executable.
func (p *Plugin) ResolveBinary(ctx context.Context) (string, error) { return p.mimoBinary(ctx) }

// InvalidateBinaryResolution clears the cached MiMo Code executable path.
func (p *Plugin) InvalidateBinaryResolution() {
	p.binaryMu.Lock()
	p.resolvedBinary = ""
	p.binaryMu.Unlock()
}

func (p *Plugin) mimoBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()
	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}
	binary, err := ResolveMiMoCodeBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}
