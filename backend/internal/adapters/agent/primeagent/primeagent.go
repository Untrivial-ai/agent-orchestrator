// Package primeagent implements the Prime Agent terminal adapter.
package primeagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const adapterID = "prime-agent"

// Plugin launches Prime Agent as an AO-managed persistent terminal process.
// Binary resolution is cached because a registered adapter is shared across
// concurrent session operations.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
	runCommand     primeCommandRunner
}

// New returns a ready-to-register Prime Agent adapter.
func New() *Plugin {
	return &Plugin{}
}

// AugmentRuntimeEnv points Prime Agent at AO's isolated profile so persistent
// daemon/config state stays under AO_DATA_DIR instead of the user's normal
// ~/.prime/agent profile.
func (p *Plugin) AugmentRuntimeEnv(env map[string]string, dataDir string) {
	dir, err := primeDataDir(dataDir)
	if err != nil {
		return
	}
	env[primeAgentCodingAgentDirEnv] = dir
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)
var _ ports.AgentBinaryResolver = (*Plugin)(nil)
var _ ports.AgentNativeSessionTerminator = (*Plugin)(nil)
var _ ports.ActiveTurnSteerer = (*Plugin)(nil)
var _ ports.SubmitActivitySignaler = (*Plugin)(nil)
var _ ports.BlockedActivitySignaler = (*Plugin)(nil)

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          adapterID,
		Name:        "Prime Agent",
		Description: "Run Prime Agent worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetConfigSpec reports the per-project Prime Agent configuration AO exposes.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ConfigSpec{}, err
	}
	return ports.ConfigSpec{Fields: []ports.ConfigField{{
		Key:         "model",
		Type:        ports.ConfigFieldString,
		Description: "Model override passed to `prime-agent --model`.",
	}}}, nil
}

// GetLaunchCommand builds a persistent Prime Agent process:
//
//	prime-agent --session-dir <ao-sessions> --extension <managed-extension> [--append-system-prompt <text>] [--model <model>] [-- <task>]
//
// The explicit separator ensures task text beginning with a hyphen cannot be
// parsed as an option. Prime Agent has no safe CLI permission-mode equivalent,
// so AO intentionally emits no permission arguments for any requested mode.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd, err := p.baseCommand(ctx, cfg.DataDir, cfg.SystemPrompt, cfg.SystemPromptFile, cfg.Config)
	if err != nil {
		return nil, err
	}
	if cfg.Prompt != "" {
		cmd = append(cmd, "--", cfg.Prompt)
	}
	return cmd, nil
}

func (p *Plugin) baseCommand(ctx context.Context, dataDir, systemPrompt, systemPromptFile string, cfg ports.AgentConfig) ([]string, error) {
	binary, err := p.primeAgentBinary(ctx)
	if err != nil {
		return nil, err
	}
	sessions, err := sessionDir(dataDir)
	if err != nil {
		return nil, err
	}
	extensionPath, err := extensionPath(dataDir)
	if err != nil {
		return nil, err
	}

	cmd := []string{binary, "--session-dir", sessions, "--extension", extensionPath}
	resolvedPrompt, err := resolveSystemPrompt(ctx, systemPrompt, systemPromptFile)
	if err != nil {
		return nil, err
	}
	if resolvedPrompt != "" {
		cmd = append(cmd, "--append-system-prompt", resolvedPrompt)
	}
	agentbase.AppendModelFlag(&cmd, cfg, "--model")
	return cmd, nil
}

func resolveSystemPrompt(ctx context.Context, inline, file string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(inline) != "" {
		return inline, nil
	}
	if strings.TrimSpace(file) == "" {
		return "", nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	data, err := os.ReadFile(file) //nolint:gosec // path comes from AO-owned launch configuration
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if err != nil {
		return "", fmt.Errorf("prime-agent: read system prompt file: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", nil
	}
	return string(data), nil
}

// GetRestoreCommand rebuilds the persistent Prime command and resumes the
// native transcript captured by the managed extension.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) ([]string, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	agentSessionID := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if agentSessionID == "" {
		return nil, false, nil
	}
	cmd, err := p.baseCommand(ctx, cfg.DataDir, cfg.SystemPrompt, cfg.SystemPromptFile, cfg.Config)
	if err != nil {
		return nil, false, err
	}
	return append(cmd, "--resume", agentSessionID), true, nil
}

// SteersActiveTurn reports that Prime queues input submitted during a turn as
// steering for that active turn.
func (p *Plugin) SteersActiveTurn() bool { return true }

// EmitsSubmitActivity reports that the managed extension emits an active
// signal when Prime starts processing a submitted prompt.
func (p *Plugin) EmitsSubmitActivity() bool { return true }

// EmitsBlockedActivity is false because Prime has no lifecycle signal with the
// safe pre/post correlation AO requires for automated permission handling.
func (p *Plugin) EmitsBlockedActivity() bool { return false }

var primeAgentBinarySpec = binaryutil.BinarySpec{
	Label:         "prime-agent",
	Names:         []string{"prime-agent"},
	WinNames:      []string{"prime-agent.cmd", "prime-agent.exe", "prime-agent"},
	UnixPaths:     []string{"/usr/local/bin/prime-agent", "/opt/homebrew/bin/prime-agent"},
	UnixHomePaths: binaryutil.NodeManagedUnixHomePaths("prime-agent"),
	NodeManaged:   true,
	WinPaths: []binaryutil.WinPath{
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "prime-agent.cmd"}},
		{Base: binaryutil.WinAppData, Parts: []string{"npm", "prime-agent.exe"}},
	},
}

// ResolvePrimeAgentBinary finds Prime Agent on PATH or in the existing set of
// supported Node-managed global binary locations.
func ResolvePrimeAgentBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, primeAgentBinarySpec)
}

// ResolveBinary resolves the executable path for the optional registry probe.
func (p *Plugin) ResolveBinary(ctx context.Context) (string, error) {
	return p.primeAgentBinary(ctx)
}

func (p *Plugin) primeAgentBinary(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}
	binary, err := ResolvePrimeAgentBinary(ctx)
	if err != nil {
		return "", err
	}
	if binary == "" {
		return "", errors.New("prime-agent: resolved an empty binary path")
	}
	p.resolvedBinary = binary
	return binary, nil
}
