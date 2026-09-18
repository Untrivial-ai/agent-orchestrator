package muse

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ResolveBinary resolves the executable path for the plugin.
func (p *Plugin) ResolveBinary(ctx context.Context) (string, error) {
	return p.museBinary(ctx)
}

// ResolveBinaryPresence resolves a local Muse executable without the normal
// version-signature process. The desktop startup gate only needs to know that
// an agent binary exists; full adapter resolution still validates Muse before
// a session can launch.
func (p *Plugin) ResolveBinaryPresence(ctx context.Context) (string, error) {
	if path, shared, err := p.DiscoveredBinary(ctx, p.Manifest().ID, ports.BinaryResolvePresence); shared {
		return path, err
	}
	return binaryutil.ResolveBinary(ctx, museBinarySpec)
}
