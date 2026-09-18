package binaryutil

import (
	"context"
	"errors"
	"os"
	"os/exec"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// DiscoverySpec exports an adapter's existing name priorities and raw lookup.
// The presence callback strips identity probes; launch normalization retains them.
func (spec BinarySpec) DiscoverySpec(lookup func(context.Context) (string, error)) ports.AgentBinarySpec {
	presenceSpec := spec
	presenceSpec.ValidateIdentity = nil
	return ports.AgentBinarySpec{
		Names:  append([]string(nil), namesForPlatform(spec)...),
		Lookup: PreserveLookupErrors(lookup, namesForPlatform(spec)),
		Presence: func(ctx context.Context) (string, error) {
			path, err := ResolveBinary(ctx, presenceSpec)
			if err == nil && spec.ValidateIdentity != nil {
				return path, ports.ErrAgentBinaryIdentityUnknown
			}
			return path, err
		},
		Normalize: func(ctx context.Context, path string, purpose ports.BinaryResolvePurpose) (string, error) {
			if spec.ValidateIdentity != nil {
				if purpose == ports.BinaryResolvePresence {
					return path, ports.ErrAgentBinaryIdentityUnknown
				}
				if !spec.ValidateIdentity(ctx, path) {
					if err := ctx.Err(); err != nil {
						return "", err
					}
					return "", ports.ErrAgentBinaryNotFound
				}
			}
			return path, ctx.Err()
		},
	}
}

// PreserveLookupErrors prevents raw legacy callbacks from turning an inaccessible
// PATH executable into a definite miss eligible for shell discovery.
func PreserveLookupErrors(lookup func(context.Context) (string, error), names []string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		path, err := lookup(ctx)
		if !errors.Is(err, ports.ErrAgentBinaryNotFound) {
			return path, err
		}
		for _, name := range names {
			if _, pathErr := LookPath(name); pathErr != nil && !errors.Is(pathErr, exec.ErrNotFound) && !errors.Is(pathErr, os.ErrNotExist) {
				return "", pathErr
			}
		}
		return path, err
	}
}
