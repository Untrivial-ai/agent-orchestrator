package conpty

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
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
