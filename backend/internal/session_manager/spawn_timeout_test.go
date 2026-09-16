package sessionmanager

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/stretchr/testify/require"
)

type blockingCreateRuntime struct {
	*fakeRuntime
	block chan struct{}
}

func (r *blockingCreateRuntime) Create(ctx context.Context, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	select {
	case <-r.block:
		return ports.RuntimeHandle{}, context.Canceled
	case <-ctx.Done():
		return ports.RuntimeHandle{}, ctx.Err()
	}
}

func TestSpawn_ContextCancellationCleansUpWorkspace(t *testing.T) {
	m, _, _, ws := newManager()

	// Use a fake runtime adapter that hangs until the context is canceled
	rt := &blockingCreateRuntime{
		fakeRuntime: &fakeRuntime{},
		block:       make(chan struct{}),
	}
	m.runtime = rt

	ctx, cancel := context.WithCancel(context.Background())

	// Start spawn in a goroutine so we can cancel its context midway through
	errCh := make(chan error, 1)
	go func() {
		_, _, _, err := m.Spawn(ctx, ports.SpawnConfig{
			ProjectID: "mer",
			Kind:      domain.KindWorker,
			Harness:   domain.HarnessClaudeCode,
		})
		errCh <- err
	}()

	// Cancel the context - this triggers the timeout/client disconnect error
	cancel()

	err := <-errCh
	require.ErrorIs(t, err, context.Canceled)

	// Verify that the workspace was properly cleaned up (rollback SeedSpawnWorkspace shouldn't skip due to canceled ctx)
	// Check that the fake workspace was destroyed
	require.Equal(t, 1, ws.destroyed)
}
