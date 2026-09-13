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
	workloadAlive bool
	workloadErr   error
	probedRef     ports.SupervisedProcessRef
	childProbes   int
	probed        ports.RuntimeHandle
	probeCtx      context.Context
}

func (r *reviewerChildRuntime) IsChildAlive(context.Context, ports.RuntimeHandle) (bool, error) {
	r.childProbes++
	return true, nil // a preserved tmux shell is a live terminal child
}

func (r *reviewerChildRuntime) IsSupervisedProcessAlive(ctx context.Context, handle ports.RuntimeHandle, ref ports.SupervisedProcessRef) (bool, error) {
	r.probed, r.probeCtx, r.probedRef = handle, ctx, ref
	return r.workloadAlive, r.workloadErr
}

func TestCodexReviewerSnapshotUsesWorkloadLiveness(t *testing.T) {
	for _, tc := range []struct {
		name          string
		workloadAlive bool
		workloadErr   error
	}{
		{name: "live Codex reviewer", workloadAlive: true},
		{name: "exited reviewer with surviving native host"},
		{name: "exited reviewer with preserved tmux shell"},
		{name: "manual workload", workloadAlive: true},
		{name: "inconclusive child probe", workloadErr: ports.ErrRuntimeProbeInconclusive},
		{name: "canceled child probe", workloadErr: context.Canceled},
		{name: "timed out child probe", workloadErr: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			handleID := "ptyhost-v1:review-claude-worker"
			if tc.name == "exited reviewer with preserved tmux shell" || tc.name == "manual workload" {
				handleID = "review-claude-worker"
			}
			rt := &reviewerChildRuntime{
				fakeRuntime:   fakeRuntime{alive: true},
				workloadAlive: tc.workloadAlive,
				workloadErr:   tc.workloadErr,
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
			if !errors.Is(err, tc.workloadErr) || snapshot.Running != tc.workloadAlive {
				t.Fatalf("snapshot = %+v, error = %v; want running %t, error %v", snapshot, err, tc.workloadAlive, tc.workloadErr)
			}
			if snapshot.HandleID != handleID || snapshot.NativeSessionID != "codex-native-history" {
				t.Fatalf("snapshot changed reviewer identity: %+v", snapshot)
			}
			if rt.probed.ID != handleID || rt.probeCtx != ctx || rt.probedRef != (ports.SupervisedProcessRef{}) || rt.childProbes != 0 {
				t.Fatal("unsupervised workload probe did not preserve the opaque handle, empty ref and caller context")
			}
			if rt.created || rt.destroyed != "" || rt.interrupts != 0 {
				t.Fatal("read-only reviewer snapshot mutated the runtime")
			}
		})
	}
}

type reviewerHostRuntime struct {
	fakeRuntime
	probeErr error
}

func (r *reviewerHostRuntime) IsAlive(context.Context, ports.RuntimeHandle) (bool, error) {
	return r.alive, r.probeErr
}

func TestReviewerWorkloadFallbackRetainsUnknownHosts(t *testing.T) {
	for _, tc := range []struct {
		alive bool
		err   error
	}{{alive: true}, {}, {err: ports.ErrRuntimeProbeInconclusive}} {
		rt := &reviewerHostRuntime{fakeRuntime: fakeRuntime{alive: tc.alive}, probeErr: tc.err}
		l := NewLauncher(fakeReviewerResolver{}, rt, t.TempDir())
		alive, err := l.Alive(context.Background(), "custom-reviewer")
		if alive != tc.alive || !errors.Is(err, tc.err) {
			t.Fatalf("custom workload fallback = %v, %v; want %v, %v", alive, err, tc.alive, tc.err)
		}
	}
}

func TestReviewerFreshAndFallbackLaunchIsUnsupervised(t *testing.T) {
	for _, name := range []string{"fresh", "restore_without_restorer_falls_back_to_fresh"} {
		t.Run(name, func(t *testing.T) {
			rt := &fakeRuntime{}
			reviewer := &fakeReviewerWithLaunchSpec{spec: ports.ReviewCommandSpec{Argv: []string{"codex"}}}
			l := newTestLauncher(t, reviewer, rt)
			spec := launchSpec()
			spec.Harness = domain.ReviewerCodex
			var err error
			// This reviewer deliberately has no ReviewerRestorer; restoration is a fresh fallback.
			if name != "fresh" {
				_, err = l.RestoreTerminal(context.Background(), spec)
			} else {
				_, err = l.Spawn(context.Background(), spec)
			}
			if err != nil {
				t.Fatal(err)
			}
			cfg := rt.createCfg
			if len(cfg.Argv) != 1 || cfg.Argv[0] != "codex" || cfg.Env["AO_SUPERVISED_PROCESS"] != "" || cfg.Env["AO_RUNTIME_LAUNCH_ID"] != "" {
				t.Fatalf("reviewer launch needs a supervised probe identity: %+v", cfg)
			}
		})
	}
}
