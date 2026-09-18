package goose

import (
	"context"
	"runtime"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// BinaryDiscoverySpec describes raw lookup without consulting the injected resolver.
func (p *Plugin) BinaryDiscoverySpec() ports.AgentBinarySpec {
	spec := gooseBinarySpec.DiscoverySpec(ResolveGooseBinary)
	normalize := spec.Normalize
	spec.Normalize = func(ctx context.Context, path string, purpose ports.BinaryResolvePurpose) (string, error) {
		if runtime.GOOS == "windows" && !isNativelyLaunchableWindowsGoose(path) {
			return "", ports.ErrAgentBinaryNotFound
		}
		return normalize(ctx, path, purpose)
	}
	return spec
}
