package httpd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

func startShutdownTestLAN(t *testing.T, handler http.Handler) (*LANManager, string) {
	t.Helper()
	m := NewMobileLAN(handler, 0, nil, nil)
	m.SetPasswordHash(mobilebridge.HashPassword("secret12"))
	port, err := m.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = m.Stop(ctx)
	})
	return m, fmt.Sprintf("http://127.0.0.1:%d", port)
}

func openLANShutdownStream(t *testing.T, url string) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret12")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	if res.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", res.StatusCode)
	}
	return res
}

func assertLANStreamClosed(t *testing.T, res *http.Response) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(res.Body)
		done <- err
	}()
	select {
	case err := <-done:
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("stream ended only because the client timed out: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("LAN stream remained open after Stop returned")
	}
}

func TestLANManagerStopCancelsStreamAndAllowsRestart(t *testing.T) {
	ended := make(chan struct{})
	m, base := startShutdownTestLAN(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = io.WriteString(w, "ready")
			return
		}
		defer close(ended)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	res := openLANShutdownStream(t, base+"/events")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Stop(ctx); err != nil {
		t.Fatalf("cooperative stream did not stop gracefully: %v", err)
	}
	assertLANStreamClosed(t, res)
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("handler context was not cancelled")
	}
	if m.Running() || m.BoundPort() != 0 {
		t.Fatal("stopped listener still advertised")
	}
	port, err := m.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	health := openLANShutdownStream(t, fmt.Sprintf("http://127.0.0.1:%d/health", port))
	body, err := io.ReadAll(health.Body)
	if err != nil || string(body) != "ready" {
		t.Fatalf("restarted listener: body=%q err=%v", body, err)
	}
}

func TestLANManagerStopClosesConnectionAfterCancelledGrace(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	m, base := startShutdownTestLAN(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		// Deliberately ignore cancellation to exercise forced connection close.
		<-release
	}))
	res := openLANShutdownStream(t, base+"/events")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Stop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop error = %v, want cancelled grace context", err)
	}
	assertLANStreamClosed(t, res)
}

func TestLANManagerStartWaitsForPreviousStop(t *testing.T) {
	stopping := make(chan struct{})
	release := make(chan struct{})
	releaseHandler := sync.OnceFunc(func() { close(release) })
	defer releaseHandler()
	m, base := startShutdownTestLAN(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(stopping)
		<-release
	}))
	res := openLANShutdownStream(t, base+"/events")
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	stopDone := make(chan error, 1)
	go func() { stopDone <- m.Stop(stopCtx) }()
	select {
	case <-stopping:
	case <-time.After(time.Second):
		releaseHandler()
		t.Fatal("stop did not cancel the old request")
	}
	statusDone := make(chan bool, 1)
	go func() { statusDone <- m.Running() && m.BoundPort() != 0 }()
	select {
	case running := <-statusDone:
		if !running {
			t.Fatal("listener advertised stopped before cleanup finished")
		}
	case <-time.After(time.Second):
		t.Fatal("status reads blocked behind graceful shutdown")
	}
	startDone := make(chan error, 1)
	go func() {
		_, err := m.Start(0)
		startDone <- err
	}()
	select {
	case err := <-startDone:
		releaseHandler()
		t.Fatalf("Start returned before the old listener finished stopping: %v", err)
	case <-time.After(50 * time.Millisecond):
		// The old handler is held open, so a completed restart is premature.
	}
	releaseHandler()
	if err := <-stopDone; err != nil {
		t.Fatal(err)
	}
	if err := <-startDone; err != nil {
		t.Fatal(err)
	}
	assertLANStreamClosed(t, res)
	if !m.Running() || m.BoundPort() == 0 {
		t.Fatal("old Stop cleared the new listener state")
	}
}

func TestLANManagerStopClosesHijackedWebSocket(t *testing.T) {
	ended := make(chan struct{})
	m, base := startShutdownTestLAN(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(ended)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			// A hijacked connection is no longer owned by http.Server. Even a
			// handler that does not use the request context must lose access.
			kind, data, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			if err := conn.Write(context.Background(), kind, data); err != nil {
				return
			}
		}
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, base+"/mux", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer secret12"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageText, []byte("before")); err != nil {
		t.Fatal(err)
	}
	if _, data, err := conn.Read(ctx); err != nil || string(data) != "before" {
		t.Fatalf("echo before stop: data=%q err=%v", data, err)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := m.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("hijacked connection remained open after Stop returned")
	}
	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatal("WebSocket remained readable after Stop")
	}
}

func TestLANManagerStopBeforeServeRegistersListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	streamCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewMobileLAN(http.NotFoundHandler(), 0, nil, nil)
	// This is Start's published state before its Serve goroutine is scheduled.
	// Delay Serve explicitly so this ownership boundary is deterministic.
	m.ln = &lanListener{Listener: ln, conns: make(map[*lanConn]struct{})}
	m.cancel = cancel
	m.bound = ln.Addr().(*net.TCPAddr).Port
	m.srv = &http.Server{
		Handler:     m.handler,
		BaseContext: func(net.Listener) context.Context { return streamCtx },
	}
	srv, tracked := m.srv, m.ln
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	rebound, err := net.Listen("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("Stop left the unregistered listener bound: %v", err)
	}
	defer rebound.Close()
	if err := srv.Serve(tracked); !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("late Serve returned %v, want ErrServerClosed", err)
	}
}

// Keeping already-closed hijacked sockets in the registry would grow memory
// with every mobile reconnect until the listener is finally disabled.
func TestLANManagerReleasesClosedWebSocket(t *testing.T) {
	ended := make(chan struct{})
	m, base := startShutdownTestLAN(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(ended)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		_, _, _ = conn.Read(context.Background())
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, base+"/mux", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer secret12"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	m.mu.Lock()
	listener := m.ln
	m.mu.Unlock()
	listener.mu.Lock()
	active := len(listener.conns)
	listener.mu.Unlock()
	if active != 1 {
		t.Fatalf("tracked active connections = %d, want 1", active)
	}
	if err := conn.CloseNow(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("WebSocket handler did not finish after client close")
	}
	listener.mu.Lock()
	retained := len(listener.conns)
	listener.mu.Unlock()
	if retained != 0 {
		t.Fatalf("retained closed connections = %d, want 0", retained)
	}
	if !m.Running() {
		t.Fatal("closing one client stopped the listener")
	}
}

func TestLANConnPreservesTCPHalfClose(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	listener := &lanListener{Listener: ln, conns: make(map[*lanConn]struct{})}
	peer, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := peer.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	closeWriter, ok := conn.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("tracked TCP connection lost CloseWrite")
	}
	if err := closeWriter.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	if n, err := peer.Read(buf); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("peer read after half-close = %d, %v; want EOF", n, err)
	}
	if _, err := peer.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if n, err := conn.Read(buf); err != nil || n != 1 || buf[0] != 'x' {
		t.Fatalf("read side after half-close = %q, %v", buf[:n], err)
	}
	if len(listener.conns) != 1 {
		t.Fatal("half-closed connection lost shutdown ownership")
	}
}

// net/http uses the optional ReaderFrom method for efficient response copies.
// A connection wrapper must keep that path available to the server.
func TestLANConnPreservesReadFrom(t *testing.T) {
	underlying := &readFromLANTestConn{}
	conn := &lanConn{Conn: underlying}
	readerFrom, ok := any(conn).(io.ReaderFrom)
	if !ok {
		t.Fatal("tracked connection lost ReaderFrom")
	}
	src := struct{ io.Reader }{strings.NewReader("response body")}
	n, err := readerFrom.ReadFrom(src)
	if err != nil || n != 13 || underlying.received.String() != "response body" {
		t.Fatalf("ReadFrom = %d, %v; body=%q", n, err, underlying.received.String())
	}
	if !underlying.usedReadFrom {
		t.Fatal("response copy bypassed the underlying ReaderFrom")
	}
}

type readFromLANTestConn struct {
	net.Conn
	received     bytes.Buffer
	usedReadFrom bool
}

func (c *readFromLANTestConn) ReadFrom(r io.Reader) (int64, error) {
	c.usedReadFrom = true
	return c.received.ReadFrom(r)
}

func (c *readFromLANTestConn) Write(p []byte) (int, error) { return c.received.Write(p) }
