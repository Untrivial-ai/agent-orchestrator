// Package codewhale implements the Codewhale terminal adapter.
package codewhale

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const adapterID = "codewhale"

var nativeSessionIDPattern = regexp.MustCompile(`^sess_[A-Za-z0-9_-]+$`)

// Plugin launches the user's Codewhale installation without replacing its
// home directory, credentials, provider configuration, hooks, or sessions.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
	runCommand     commandRunner
}

// New returns a ready-to-register Codewhale adapter.
func New() *Plugin { return &Plugin{} }

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)
var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentBinaryResolver = (*Plugin)(nil)

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          adapterID,
		Name:        "Codewhale",
		Description: "Run Codewhale interactive TUI sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec reports Codewhale's free-form model override.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	return agentbase.ModelConfigSpec(ctx, "Model override passed to `codewhale --model`.")
}

// GetLaunchCommand starts a fresh Codewhale session. --prompt prevents task
// text beginning with a hyphen from being parsed as another CLI option.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) ([]string, error) {
	cmd, err := p.baseCommand(ctx, cfg.WorkspacePath, cfg.Config, cfg.Permissions)
	if err != nil {
		return nil, err
	}
	cmd = append(cmd, "--fresh")
	if prompt := strings.TrimSpace(cfg.Prompt); prompt != "" {
		cmd = append(cmd, "--prompt", prompt)
	}
	return cmd, nil
}

// GetRestoreCommand resumes exactly the native Codewhale session captured by
// the lifecycle outbox. Missing identity permits AO's ordinary fresh fallback;
// malformed identity fails closed instead of selecting a best-match session.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) ([]string, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	nativeID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if nativeID == "" {
		return nil, false, nil
	}
	if !nativeSessionIDPattern.MatchString(nativeID) {
		return nil, false, fmt.Errorf("codewhale: invalid native session id %q", nativeID)
	}
	cmd, err := p.baseCommand(ctx, cfg.Session.WorkspacePath, cfg.Config, cfg.Permissions)
	if err != nil {
		return nil, false, err
	}
	cmd = append(cmd, "--resume", nativeID)
	return cmd, true, nil
}

func (p *Plugin) baseCommand(ctx context.Context, workspace string, cfg ports.AgentConfig, permissions ports.PermissionMode) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	binary, err := p.codewhaleBinary(ctx)
	if err != nil {
		return nil, err
	}
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil, errors.New("codewhale: workspace path is required")
	}
	cmd := []string{binary, "--workspace", workspace, "--skip-onboarding"}
	agentbase.AppendModelFlag(&cmd, cfg, "--model")
	appendPermissionFlags(&cmd, permissions)
	return cmd, nil
}

func appendPermissionFlags(cmd *[]string, mode ports.PermissionMode) {
	switch ports.NormalizePermissionMode(mode) {
	case ports.PermissionModeAuto:
		*cmd = append(*cmd, "--approval-policy", "auto")
	case ports.PermissionModeBypassPermissions:
		// v0.10.0 retains --yolo as its explicit unrestricted TUI mode. The
		// public --approval-policy parser intentionally does not accept bypass.
		*cmd = append(*cmd, "--yolo")
	case ports.PermissionModeDefault, ports.PermissionModeAcceptEdits:
		*cmd = append(*cmd, "--approval-policy", "on-request")
	}
}

// SessionInfo returns lifecycle metadata already persisted by AO.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

var codewhaleBinarySpec = binaryutil.BinarySpec{
	Label:     "codewhale",
	Names:     []string{"codewhale", "codew"},
	WinNames:  []string{"codewhale.exe", "codewhale.cmd", "codew.exe", "codew.cmd", "codewhale", "codew"},
	UnixPaths: []string{"/usr/local/bin/codewhale", "/usr/local/bin/codew", "/opt/homebrew/bin/codewhale", "/opt/homebrew/bin/codew"},
	UnixHomePaths: [][]string{
		{".local", "bin", "codewhale"},
		{".local", "bin", "codew"},
		{".cargo", "bin", "codewhale"},
		{".cargo", "bin", "codew"},
	},
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinHome, Parts: []string{".local", "bin", "codewhale.exe"}},
		{Base: binaryutil.WinHome, Parts: []string{".local", "bin", "codew.exe"}},
		{Base: binaryutil.WinHome, Parts: []string{".cargo", "bin", "codewhale.exe"}},
		{Base: binaryutil.WinHome, Parts: []string{".cargo", "bin", "codew.exe"}},
	},
}

// ResolveCodewhaleBinary finds Codewhale on PATH or in its common native
// installer locations.
func ResolveCodewhaleBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, codewhaleBinarySpec)
}

// ResolveBinary resolves the executable path for readiness and model probes.
func (p *Plugin) ResolveBinary(ctx context.Context) (string, error) {
	return p.codewhaleBinary(ctx)
}

func (p *Plugin) codewhaleBinary(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()
	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}
	binary, err := ResolveCodewhaleBinary(ctx)
	if err != nil {
		return "", err
	}
	if binary == "" {
		return "", errors.New("codewhale: resolved an empty binary path")
	}
	if runtime.GOOS != "windows" {
		binary, err = filepath.Abs(binary)
		if err != nil {
			return "", fmt.Errorf("codewhale: resolve binary path: %w", err)
		}
	}
	p.resolvedBinary = binary
	return binary, nil
}
