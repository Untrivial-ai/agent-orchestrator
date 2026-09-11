package conpty

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
)

func TestIsChildAlivePreservesInconclusiveStatus(t *testing.T) {
	for _, payload := range []string{`{`, `{}`, `{"alive":false}`} {
		t.Run(payload, func(t *testing.T) {
			isolateRegistry(t)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				if _, err := io.ReadFull(conn, make([]byte, 5)); err != nil {
					return
				}
				frame, _ := EncodeMessage(MsgStatusRes, []byte(payload))
				_, _ = conn.Write(frame)
			}()
			rt := New(Options{})
			rt.sessions["shell"] = &hostSession{addr: listener.Addr().String(), pid: deadPID()}
			alive, err := rt.IsChildAlive(context.Background(), ports.RuntimeHandle{ID: "shell"})
			if alive || err == nil {
				t.Fatalf("incomplete child status %q = %v, %v; want probe error", payload, alive, err)
			}
			if payload != "{" && !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
				t.Fatalf("incomplete child status %q = %v; want inconclusive exit evidence", payload, err)
			}
			<-done
		})
	}
}

func TestIsChildAlivePreservesUnresolvedHostAndCancellation(t *testing.T) {
	isolateRegistry(t)
	rt := New(Options{})
	handle := ports.RuntimeHandle{ID: "shell-unresolved"}
	rt.sessions[handle.ID] = &hostSession{addr: unresolvedHostAddress, pid: livePID()}
	if alive, err := rt.IsChildAlive(context.Background(), handle); alive || !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("unresolved host child = %v, %v; want inconclusive", alive, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if alive, err := rt.IsChildAlive(ctx, handle); alive || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled child probe = %v, %v; want cancellation", alive, err)
	}
}

// Only reconciliation's persistence boundary is faked: status and teardown use
// the real detached-host protocol, whose listener survives its child's exit.
type shellReconcileStore struct {
	shellterm.Store
	records []shellterm.ShellTerminalRecord
}

func (s *shellReconcileStore) SelectRestorableShellTerminals(context.Context, string) ([]shellterm.ShellTerminalRecord, error) {
	return s.records, nil
}

func (s *shellReconcileStore) SelectShellTerminalsFromPreviousAppRuns(context.Context, string) ([]shellterm.ShellTerminalRecord, error) {
	return s.records, nil
}

func (s *shellReconcileStore) DeleteShellTerminalByHandleID(_ context.Context, handleID string) (bool, error) {
	if len(s.records) == 1 && s.records[0].HandleID == handleID {
		s.records = nil
		return true, nil
	}
	return false, nil
}

func TestShellReconciliationClosesHostAfterChildExit(t *testing.T) {
	for _, path := range []string{"list", "desktop-relaunch"} {
		t.Run(path, func(t *testing.T) {
			isolateRegistry(t)
			hosts := map[string]*inProcHost{}
			// This host runs inside the test. Never give Destroy a real PID.
			rt := New(Options{Spawner: fakeSpawnerFor(t, hosts, deadPID())})
			ctx := context.Background()
			handle, err := rt.Create(ctx, ports.RuntimeConfig{
				SessionID: "shellterm-exit", WorkspacePath: t.TempDir(), Argv: []string{"sh"},
			})
			if err != nil {
				t.Fatal(err)
			}
			h := hosts[handle.ID]
			stopped := false
			t.Cleanup(func() {
				if !stopped {
					h.cleanup(t)
				}
			})
			store := &shellReconcileStore{records: []shellterm.ShellTerminalRecord{{
				HandleID: handle.ID, AppRunID: "old-desktop", Title: "Terminal 1",
			}}}
			svc := shellterm.NewService(rt, store, nil, nil, t.TempDir(), "new-desktop", nil)
			listed, err := svc.ListShellTerminalsForCurrentAppRun(ctx)
			if err != nil || len(listed) != 1 {
				t.Fatalf("live child list = %v, %v", listed, err)
			}
			h.pty.signalExit(42)
			if alive, err := rt.IsAlive(ctx, handle); err != nil || !alive {
				t.Fatalf("host after child exit = %v, %v; want reachable host", alive, err)
			}
			if path == "list" {
				listed, err = svc.ListShellTerminalsForCurrentAppRun(ctx)
				if err != nil || len(listed) != 0 {
					t.Fatalf("exited child list = %v, %v; want no dead tab", listed, err)
				}
			} else if count, err := svc.ReapShellTerminalsFromPreviousAppRuns(ctx); err != nil || count != 1 {
				t.Fatalf("relaunch reap = %d, %v; want one exited shell", count, err)
			}
			if len(store.records) != 0 {
				t.Fatalf("exited shell rows = %v", store.records)
			}
			select {
			case <-h.done:
				stopped = true
			case <-time.After(2 * time.Second):
				t.Fatal("exited shell's retained host was not stopped")
			}
		})
	}
}
