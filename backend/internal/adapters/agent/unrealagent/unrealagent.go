// Package unrealagent exposes AO's built-in Unreal Agent harness.
package unrealagent

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const adapterID = "unreal-agent"

// Plugin represents the harness compiled into AO. Structured Chat is its only
// interface because the Unreal library does not expose an interactive terminal.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
	executable     func() (string, error)
}

// New creates the agent metadata adapter for AO's embedded Unreal harness.
func New() *Plugin { return &Plugin{executable: os.Executable} }

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)
var _ ports.AgentBinaryResolver = (*Plugin)(nil)
var _ ports.AgentBinaryResolutionInvalidator = (*Plugin)(nil)

// Manifest describes the built-in harness.
func (*Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID: adapterID, Name: "Unreal Agent",
		Description:  "Built-in async-first Unreal Agent harness.",
		Version:      "0.1.1",
		Capabilities: []adapters.Capability{adapters.CapabilityAgent},
	}
}

// GetConfigSpec exposes the model override supported by the harness.
func (*Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	return agentbase.ModelConfigSpec(ctx, "Model passed to the built-in Unreal Agent harness.")
}

// GetLaunchCommand rejects terminal launches because Unreal is Chat-only.
func (*Plugin) GetLaunchCommand(ctx context.Context, _ ports.LaunchConfig) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("unreal agent is available in chat mode only")
}

// ResolveBinary returns AO's own executable because the harness is embedded.
func (p *Plugin) ResolveBinary(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()
	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}
	resolve := p.executable
	if resolve == nil {
		resolve = os.Executable
	}
	binary, err := resolve()
	if err != nil {
		return "", err
	}
	p.resolvedBinary = strings.TrimSpace(binary)
	return p.resolvedBinary, nil
}

// InvalidateBinaryResolution clears the cached AO executable path.
func (p *Plugin) InvalidateBinaryResolution() {
	p.binaryMu.Lock()
	p.resolvedBinary = ""
	p.binaryMu.Unlock()
}
