package review

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type reviewerChildRuntime struct {
	fakeRuntime
	childAlive bool
	childErr   error
	probed     ports.RuntimeHandle
	probeCtx   context.Context
}

func (r *reviewerChildRuntime) IsChildAlive(ctx context.Context, handle ports.RuntimeHandle) (bool, error) {
	r.probed, r.probeCtx = handle, ctx
	return r.childAlive, r.childErr
}

func TestCodexReviewerSnapshotUsesChildLiveness(t *testing.T) {
	for _, tc := range []struct {
		name       string
		childAlive bool
		childErr   error
	}{
		{name: "live Codex reviewer", childAlive: true},
		{name: "exited reviewer with surviving host"},
		{name: "inconclusive child probe", childErr: ports.ErrRuntimeProbeInconclusive},
		{name: "canceled child probe", childErr: context.Canceled},
		{name: "timed out child probe", childErr: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			const handleID = "ptyhost-v1:review-claude-worker"
			rt := &reviewerChildRuntime{
				fakeRuntime: fakeRuntime{alive: true},
				childAlive:  tc.childAlive,
				childErr:    tc.childErr,
			}
			store := &fakeStore{review: &domain.Review{
				SessionID: "claude-worker", Harness: domain.ReviewerCodex,
				ReviewerHandleID: handleID, AgentSessionID: "codex-native-history",
			}}
			engine := New(Deps{
				Store:    store,
				Launcher: NewLauncher(fakeReviewerResolver{}, rt, t.TempDir()),
			})

			snapshot, err := engine.SnapshotCodexReviewer(ctx, "claude-worker")
			if !errors.Is(err, tc.childErr) || snapshot.Running != tc.childAlive {
				t.Fatalf("snapshot = %+v, error = %v; want running %t, error %v", snapshot, err, tc.childAlive, tc.childErr)
			}
			if snapshot.HandleID != handleID || snapshot.NativeSessionID != "codex-native-history" {
				t.Fatalf("snapshot changed reviewer identity: %+v", snapshot)
			}
			if rt.probed.ID != handleID || rt.probeCtx != ctx {
				t.Fatal("child probe did not preserve the opaque handle and caller context")
			}
			if rt.created || rt.destroyed != "" || rt.interrupts != 0 {
				t.Fatal("read-only reviewer snapshot mutated the runtime")
			}
		})
	}
}
