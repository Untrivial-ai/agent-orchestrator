// Package gemini integrates the user's Gemini CLI with AO's terminal sessions.
package gemini

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

// Plugin supplies Gemini CLI commands and hook integration.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a Gemini CLI adapter.
func New() *Plugin { return &Plugin{} }

var _ ports.Agent = (*Plugin)(nil)
var _ adapters.Adapter = (*Plugin)(nil)

// Manifest describes the Gemini CLI adapter.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{ID: "gemini", Name: "Gemini CLI", Description: "Run Google Gemini CLI worker sessions.", Version: "0.0.1", Capabilities: []adapters.Capability{adapters.CapabilityAgent}}
}

// GetConfigSpec exposes the native model override.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	return agentbase.ModelConfigSpec(ctx, "Model override passed to gemini --model.")
}

// GetLaunchCommand starts an interactive Gemini conversation.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) ([]string, error) {
	cmd, err := p.command(ctx, cfg.Permissions, cfg.Config, cfg.AllowedTools, cfg.DisallowedTools)
	if err != nil {
		return nil, err
	}
	if id := strings.TrimSpace(cfg.NativeSessionID); id != "" {
		cmd = append(cmd, "--session-id", id)
	}
	if cfg.Prompt != "" {
		cmd = append(cmd, "--prompt-interactive", cfg.Prompt)
	}
	return cmd, nil
}

// GetRestoreCommand resumes the native identity recorded by Gemini hooks.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) ([]string, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	id := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if id == "" {
		return nil, false, nil
	}
	cmd, err := p.command(ctx, cfg.Permissions, cfg.Config, cfg.AllowedTools, cfg.DisallowedTools)
	if err != nil {
		return nil, false, err
	}
	cmd = append(cmd, "--resume", id)
	if cfg.Prompt != "" {
		cmd = append(cmd, "--prompt-interactive", cfg.Prompt)
	}
	return cmd, true, nil
}

func (p *Plugin) command(ctx context.Context, mode ports.PermissionMode, cfg ports.AgentConfig, allow, deny []string) ([]string, error) {
	if len(allow) != 0 || len(deny) != 0 {
		return nil, fmt.Errorf("gemini: tool restrictions require a native policy and are not supported by this adapter")
	}
	bin, err := p.ResolveBinary(ctx)
	if err != nil {
		return nil, err
	}
	// AO's workspace is explicitly selected by the user. Trust is scoped to
	// this process; it does not alter the user's trusted-folders configuration.
	cmd := []string{bin, "--skip-trust"}
	switch ports.NormalizePermissionMode(mode) {
	case ports.PermissionModeAcceptEdits, ports.PermissionModeAuto:
		// Gemini has no auto mode. Keep shell/tool approvals instead of
		// silently escalating this mode to yolo.
		cmd = append(cmd, "--approval-mode", "auto_edit")
	case ports.PermissionModeBypassPermissions:
		cmd = append(cmd, "--approval-mode", "yolo")
	}
	agentbase.AppendModelFlag(&cmd, cfg, "--model")
	return cmd, nil
}

// SessionInfo returns hook-derived native session metadata.
func (p *Plugin) SessionInfo(ctx context.Context, ref ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(ref)
	return info, ok, nil
}

var binarySpec = binaryutil.BinarySpec{
	Label: "gemini", Names: []string{"gemini"}, WinNames: []string{"gemini.cmd", "gemini.exe", "gemini"},
	UnixPaths:     []string{"/opt/homebrew/bin/gemini", "/usr/local/bin/gemini"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("gemini"), NodeManaged: true,
	WinPaths: []binaryutil.WinPath{{Base: binaryutil.WinAppData, Parts: []string{"npm", "gemini.cmd"}}},
}

// ResolveBinary finds Gemini and verifies its minimum supported version.
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
	if err := checkVersion(ctx, bin); err != nil {
		return "", err
	}
	p.resolvedBinary = bin
	return bin, nil
}

// AuthStatus remains unknown because Gemini has no cheap credential-validation command.
// Key or OAuth-file presence alone cannot establish that an account works.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	_, err := p.ResolveBinary(ctx)
	return ports.AgentAuthStatusUnknown, err
}

// ResolveBinaryPresence keeps initial inventory discovery free of child processes.
func (p *Plugin) ResolveBinaryPresence(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, binarySpec)
}
