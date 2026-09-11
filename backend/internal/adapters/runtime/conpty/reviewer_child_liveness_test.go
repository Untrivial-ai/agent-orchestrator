package conpty

import (
	"context"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/testutil/reviewerupdate"
)

func TestReviewerChildExitLeavesHostAvailable(t *testing.T) {
	isolateRegistry(t)
	hosts := map[string]*inProcHost{}
	// The protocol host and fake PTY live inside this test; no Codex is started.
	rt := New(Options{Spawner: fakeSpawnerFor(t, hosts, deadPID())})
	ctx := context.Background()
	handle, err := rt.Create(ctx, ports.RuntimeConfig{
		SessionID: "review-worker", WorkspacePath: t.TempDir(), Argv: []string{"codex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := hosts[handle.ID]
	t.Cleanup(func() { h.cleanup(t) })
	if alive, err := rt.IsSupervisedProcessAlive(ctx, handle, ports.SupervisedProcessRef{}); err != nil || !alive {
		t.Fatalf("reviewer before exit = %v, %v; want live child", alive, err)
	}
	h.pty.signalExit(42)
	if alive, err := rt.IsSupervisedProcessAlive(ctx, handle, ports.SupervisedProcessRef{}); err != nil || alive {
		t.Fatalf("reviewer after exit = %v, %v; want confirmed child exit", alive, err)
	}
	if alive, err := rt.IsAlive(ctx, handle); err != nil || !alive {
		t.Fatalf("host after child probe = %v, %v; want retained host", alive, err)
	}
	if alive, err := rt.IsSupervisedProcessAlive(ctx, ports.RuntimeHandle{ID: "absent-reviewer"}, ports.SupervisedProcessRef{}); err != nil || alive {
		t.Fatalf("absent reviewer = %v, %v; want confirmed absence", alive, err)
	}
}

func TestReviewerWorkloadUpdateTransition(t *testing.T) {
	for _, mode := range []string{"fresh", "resume", "restore"} {
		t.Run(mode, func(t *testing.T) {
			isolateRegistry(t)
			hosts := map[string]*inProcHost{}
			var argv []string
			spawn := fakeSpawnerFor(t, hosts, deadPID())
			rt := New(Options{Spawner: func(ctx context.Context, id, cwd string, args []string, env map[string]string) (string, int, error) {
				argv = append([]string(nil), args...)
				if env["AO_SUPERVISED_PROCESS"] != "" || env["AO_RUNTIME_LAUNCH_ID"] != "" {
					t.Fatal("reviewer unexpectedly supervised")
				}
				return spawn(ctx, id, cwd, args, env)
			}})
			ctx := context.Background()
			f := reviewerupdate.New(ctx, t, rt, mode, "/fixture/codex")
			h := hosts[f.Result.HandleID]
			t.Cleanup(func() { h.cleanup(t) })
			want := []string{"/fixture/codex"}
			if mode != "fresh" {
				want = append(want, "resume", "native-history")
			}
			if !reflect.DeepEqual(argv, want) {
				t.Fatalf("review argv=%v want=%v", argv, want)
			}
			f.Blocked(ctx, t, nil) // a live process waiting for work still owns Codex
			saved := rt.sessions[f.Result.HandleID]
			rt.sessions[f.Result.HandleID] = &hostSession{addr: unresolvedHostAddress, pid: 0}
			f.Blocked(ctx, t, ports.ErrRuntimeProbeInconclusive)
			rt.sessions[f.Result.HandleID] = saved
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			f.Blocked(canceled, t, context.Canceled)
			h.pty.signalExit(42)
			f.Blocked(ctx, t, nil)
			if alive, err := rt.IsAlive(ctx, ports.RuntimeHandle{ID: f.Result.HandleID}); err != nil || !alive {
				t.Fatalf("update terminated retained host: %v %v", alive, err)
			}
			f.Close(ctx, t)
			f.Ready(ctx, t)
		})
	}
}
