package droid

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// BinaryDiscoverySpec describes raw lookup without consulting the injected resolver.
func (p *Plugin) BinaryDiscoverySpec() ports.AgentBinarySpec {
	spec := droidBinarySpec.DiscoverySpec(ResolveDroidBinary)
	return spec
}

// ResolveBinaryPresence never waits for shell initialization or runs identity probes.
func (p *Plugin) ResolveBinaryPresence(ctx context.Context) (string, error) {
	if path, shared, err := p.DiscoveredBinary(ctx, p.Manifest().ID, ports.BinaryResolvePresence); shared {
		return path, err
	}
	return p.BinaryDiscoverySpec().Presence(ctx)
}
