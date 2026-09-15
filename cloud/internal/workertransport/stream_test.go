package workertransport

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

// wsDialer dials a test server for every terminal.
type wsDialer struct {
	url   string
	dials atomic.Int64
}

func (d *wsDialer) DialTerminalStream(
	ctx context.Context,
	terminalID string,
) (*websocket.Conn, error) {
	d.dials.Add(1)
	conn, _, err := websocket.Dial(ctx, d.url, nil)
	return conn, err
}

func streamFrame(t *testing.T, conn *websocket.Conn, frame worker.TerminalStreamFrame) {
	t.Helper()
	encoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("encode frame: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, encoded); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

func readFrame(t *testing.T, conn *websocket.Conn) worker.TerminalStreamFrame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, message, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	var frame worker.TerminalStreamFrame
	if err := json.Unmarshal(message, &frame); err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	return frame
}

func TestRunTerminalStreamWritesPushedInputToPTY(t *testing.T) {
	accepted := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		accepted <- conn
		<-r.Context().Done()
	}))
	defer server.Close()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer reader.Close()
	defer writer.Close()

	terminal := &terminalProcess{pty: writer, cancel: func() {}, cleanup: func() {}}
	supervisor := &Supervisor{
		Streams: &wsDialer{url: server.URL},
		Logger:  slog.Default(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go supervisor.runTerminalStream(ctx, "terminal-1", terminal)

	var conn *websocket.Conn
	select {
	case conn = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never dialed the stream")
	}
	streamFrame(t, conn, worker.TerminalStreamFrame{Type: "input", Data: []byte("ls\r")})

	buffer := make([]byte, 16)
	_ = reader.SetReadDeadline(time.Now().Add(5 * time.Second))
	count, err := reader.Read(buffer)
	if err != nil {
		t.Fatalf("read pty: %v", err)
	}
	if string(buffer[:count]) != "ls\r" {
		t.Fatalf("pty got %q, want %q", buffer[:count], "ls\r")
	}
}

func TestSendOutputPrefersStreamAndFallsBackWhenBroken(t *testing.T) {
	received := make(chan worker.TerminalStreamFrame, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		received <- readFrame(t, conn)
		_ = conn.Close(websocket.StatusNormalClosure, "done")
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, _, err := websocket.Dial(ctx, server.URL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	stream := &terminalStream{conn: conn, ctx: ctx}
	if !stream.sendOutput([]byte("hello")) {
		t.Fatal("healthy stream refused output")
	}
	frame := <-received
	if frame.Type != "output" || string(frame.Data) != "hello" {
		t.Fatalf("server got %+v", frame)
	}
	// Once the socket is gone (in production the read loop notices first and
	// unsets the terminal's stream pointer), sends must fail so the copy loop
	// falls back to the HTTP publish path — and stay failed thereafter.
	_ = conn.CloseNow()
	if stream.sendOutput([]byte("again")) {
		t.Fatal("closed stream accepted output")
	}
	if stream.sendOutput([]byte("still")) {
		t.Fatal("retired stream accepted output")
	}
}

func TestRunTerminalStreamStopsOnPermanentRejection(t *testing.T) {
	dialer := &wsDialer{}
	hold := make(chan struct{})
	defer close(hold)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		streamFrame(t, conn, worker.TerminalStreamFrame{
			Type: "error", Code: "STALE_WORKER_TOKEN",
		})
		<-hold
	}))
	defer server.Close()
	dialer.url = server.URL

	reader, writer, _ := os.Pipe()
	defer reader.Close()
	defer writer.Close()
	terminal := &terminalProcess{pty: writer, cancel: func() {}, cleanup: func() {}}
	supervisor := &Supervisor{Streams: dialer, Logger: slog.Default()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		supervisor.runTerminalStream(ctx, "terminal-1", terminal)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stream kept redialing after a permanent rejection")
	}
	if dials := dialer.dials.Load(); dials != 1 {
		t.Fatalf("expected a single dial, got %d", dials)
	}
	if terminal.stream.Load() != nil {
		t.Fatal("retired stream still installed on the terminal")
	}
}
