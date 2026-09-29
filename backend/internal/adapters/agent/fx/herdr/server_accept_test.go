package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// flakyListener fails the first accepts with a transient error, then hands
// out queued connections until closed.
type flakyListener struct {
	failures int
	conns    chan net.Conn
	closed   chan struct{}
	once     sync.Once
}

func (l *flakyListener) Accept() (net.Conn, error) {
	if l.failures > 0 {
		l.failures--
		return nil, &net.OpError{Op: "accept", Net: "unix", Err: os.NewSyscallError("accept", syscall.EMFILE)}
	}
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *flakyListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *flakyListener) Addr() net.Addr { return &net.UnixAddr{Name: "flaky", Net: "unix"} }

func TestServerRetriesTransientAcceptErrors(t *testing.T) {
	listener := &flakyListener{failures: 3, conns: make(chan net.Conn, 1), closed: make(chan struct{})}
	sink := &captureSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := &Server{sink: sink, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), listener: listener,
		ctx: ctx, cancel: cancel, done: make(chan struct{}), clients: make(map[net.Conn]struct{})}
	go server.serve()
	defer server.Stop()

	client, peer := net.Pipe()
	defer client.Close()
	listener.conns <- peer
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := fmt.Fprintln(client, `{"id":"7","method":"pane.report_agent","params":{"pane_id":"ao:1:bWVyLTE:bGF1bmNoLTE","source":"custom:fx","agent":"fx","state":"working"}}`); err != nil {
		t.Fatal(err)
	}
	var reply response
	if err := json.NewDecoder(client).Decode(&reply); err != nil || !reply.OK {
		t.Fatalf("server stopped after transient accept errors: %+v %v", reply, err)
	}
	// The reply is written after the sink call, so the record is visible here.
	if len(sink.records) != 1 || sink.records[0].signal.State != domain.ActivityActive {
		t.Fatalf("signals = %+v", sink.records)
	}
}
