// Package omp implements AO's OMP agent adapter. OMP is a terminal-first
// coding harness, so AO launches it interactively inside the session terminal
// pane and keeps protocol-level RPC/ACP integration out of this adapter.
package omp

import (
	"context"
	"os"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const adapterID = "omp"

// Plugin is the OMP agent adapter. It is safe for concurrent use; binary
// resolution is cached after the first successful lookup.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register OMP adapter.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)
var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentBinaryResolver = (*Plugin)(nil)

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          adapterID,
		Name:        "OMP",
		Description: "Run OMP interactive TUI sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec reports the per-project agent config keys OMP understands.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	return agentbase.ModelConfigSpec(ctx, "Model override passed to `omp --model`.")
}

// GetLaunchCommand builds the argv to start a fresh interactive OMP session:
//
//	omp [--append-system-prompt <system prompt>] [--model <model>] [<prompt>]
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) ([]string, error) {
	binary, err := p.ompBinary(ctx)
	if err != nil {
		return nil, err
	}

	cmd := make([]string, 0, 5)
	cmd = append(cmd, binary)
	appendOMPExtensionFlag(&cmd, cfg.WorkspacePath)
	if err := appendSystemPrompt(&cmd, cfg.SystemPrompt, cfg.SystemPromptFile); err != nil {
		return nil, err
	}
	agentbase.AppendModelFlag(&cmd, cfg.Config, "--model")
	if prompt := strings.TrimSpace(cfg.Prompt); prompt != "" {
		cmd = append(cmd, prompt)
	}
	return cmd, nil
}

// GetRestoreCommand continues an OMP session when AO has captured its native
// session id. ok=false means the session manager should fall back to a fresh
// launch.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) ([]string, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	agentSessionID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if agentSessionID == "" {
		return nil, false, nil
	}

	binary, err := p.ompBinary(ctx)
	if err != nil {
		return nil, false, err
	}
	cmd := make([]string, 0, 5)
	cmd = append(cmd, binary)
	appendOMPExtensionFlag(&cmd, cfg.Session.WorkspacePath)
	if err := appendSystemPrompt(&cmd, cfg.SystemPrompt, cfg.SystemPromptFile); err != nil {
		return nil, false, err
	}
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

func appendSystemPrompt(cmd *[]string, inline, file string) error {
	if inline != "" {
		*cmd = append(*cmd, "--append-system-prompt", inline)
		return nil
	}
	if file == "" {
		return nil
	}
	data, err := os.ReadFile(file) //nolint:gosec // path is AO-owned launch config
	if err != nil {
		return err
	}
	*cmd = append(*cmd, "--append-system-prompt", string(data))
	return nil
}

var ompBinarySpec = binaryutil.BinarySpec{
	Label:         "omp",
	Names:         []string{"omp"},
	WinNames:      []string{"omp.cmd", "omp.exe", "omp"},
	UnixPaths:     []string{"/usr/local/bin/omp", "/opt/homebrew/bin/omp"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("omp", []string{".omp", "bin", "omp"}),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "omp.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "omp.exe"}},
	},
}

// ResolveOMPBinary finds the `omp` binary, searching PATH then common install
// locations. It returns a wrapped ports.ErrAgentBinaryNotFound when OMP is
// absent.
func ResolveOMPBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, ompBinarySpec)
}

func (p *Plugin) ompBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}
	binary, err := ResolveOMPBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}
