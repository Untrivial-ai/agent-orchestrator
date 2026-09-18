package agentbase

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/agentlaunch"
)

// AugmentBinaryRuntimeEnv supplies only directories needed by the selected
// executable and its interpreter. Shell environment values are never imported.
func (b *Base) AugmentBinaryRuntimeEnv(ctx context.Context, env map[string]string, argv []string, pinnedDir string) {
	snapshot, ok := b.BinaryDiscovery.(interface{ ShellPATH() string })
	if !ok {
		return
	}
	agentlaunch.AugmentDiscoveredRuntimeEnv(ctx, env, argv, pinnedDir, snapshot.ShellPATH())
}
