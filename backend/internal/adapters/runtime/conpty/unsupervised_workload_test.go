package conpty

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestUnsupervisedWorkloadPreservesInconclusiveStatus(t *testing.T) {
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
			alive, err := rt.IsSupervisedProcessAlive(context.Background(), ports.RuntimeHandle{ID: "shell"}, ports.SupervisedProcessRef{})
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

func TestUnsupervisedWorkloadPreservesUnresolvedHostAndCancellation(t *testing.T) {
	isolateRegistry(t)
	rt := New(Options{})
	handle := ports.RuntimeHandle{ID: "shell-unresolved"}
	rt.sessions[handle.ID] = &hostSession{addr: unresolvedHostAddress, pid: livePID()}
	if alive, err := rt.IsSupervisedProcessAlive(context.Background(), handle, ports.SupervisedProcessRef{}); alive || !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("unresolved host child = %v, %v; want inconclusive", alive, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if alive, err := rt.IsSupervisedProcessAlive(ctx, handle, ports.SupervisedProcessRef{}); alive || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled child probe = %v, %v; want cancellation", alive, err)
	}
}
