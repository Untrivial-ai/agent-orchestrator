package muse

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// BinaryDiscoverySpec describes raw lookup without consulting the injected resolver.
func (p *Plugin) BinaryDiscoverySpec() ports.AgentBinarySpec {
	spec := museBinarySpec.DiscoverySpec(ResolveMuseBinary)
	spec.Normalize = func(ctx context.Context, path string, purpose ports.BinaryResolvePurpose) (string, error) {
		if purpose == ports.BinaryResolvePresence {
			return path, nil
		}
		valid, err := officialMuseIdentity(ctx, path)
		if err != nil {
			return "", err
		}
		if valid {
			return path, nil
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return "", ports.ErrAgentBinaryNotFound
	}
	return spec
}
