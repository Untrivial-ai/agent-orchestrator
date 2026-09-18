package copilot

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// BinaryDiscoverySpec describes raw lookup without consulting the injected resolver.
func (p *Plugin) BinaryDiscoverySpec() ports.AgentBinarySpec {
	spec := (binaryutil.BinarySpec{Names: []string{"copilot"}, WinNames: []string{"copilot.cmd", "copilot.exe", "copilot"}}).DiscoverySpec(ResolveCopilotBinary)
	spec.Presence = spec.Lookup
	spec.Normalize = func(_ context.Context, path string, _ ports.BinaryResolvePurpose) (string, error) {
		if native := copilotNativeBinaryForLoader(path); native != "" {
			return native, nil
		}
		return path, nil
	}
	return spec
}

// ResolveBinaryPresence never waits for shell initialization or runs identity probes.
func (p *Plugin) ResolveBinaryPresence(ctx context.Context) (string, error) {
	if path, shared, err := p.DiscoveredBinary(ctx, p.Manifest().ID, ports.BinaryResolvePresence); shared {
		return path, err
	}
	return p.BinaryDiscoverySpec().Presence(ctx)
}
