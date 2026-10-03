package persistenthost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type hostIOWriter struct{}

func (hostIOWriter) Write(p []byte) (int, error) { return len(p), nil }
func (hostIOWriter) Close() error                { return nil }

type observedHostWait struct {
	sync.Locker
	waiting chan struct{}
	once    sync.Once
}

func (l *observedHostWait) Unlock() {
	l.once.Do(func() { close(l.waiting) })
	l.Locker.Unlock()
}

type observedHostConn struct {
	net.Conn
	writeStarted chan struct{}
	once         sync.Once
}

func (c *observedHostConn) Write(p []byte) (int, error) {
	if len(p) >= 32<<10 {
		c.once.Do(func() { close(c.writeStarted) })
	}
	return c.Conn.Write(p)
}

func newIOTestHost(t *testing.T) *host {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := &host{ctx: ctx, stdin: hostIOWriter{}, token: "test-capability", pendingRequests: make(map[string]*pendingRequest), shutdown: make(chan struct{})}
	h.cond = sync.NewCond(&h.mu)
	return h
}

func connectIOTestHost(t *testing.T, h *host) (net.Conn, *observedHostConn, <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan *observedHostConn, 1)
	done := make(chan struct{})
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			close(done)
			return
		}
		_ = conn.(*net.TCPConn).SetWriteBuffer(1024)
		observed := &observedHostConn{Conn: conn, writeStarted: make(chan struct{})}
		accepted <- observed
		h.handle(observed)
		close(done)
	}()
	peer, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server := <-accepted
	t.Cleanup(func() {
		_ = peer.Close()
		_ = server.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("controller handler did not exit after socket close")
		}
	})
	return peer, server, done
}

func attachIOTestHost(t *testing.T, h *host) (net.Conn, *observedHostConn, *bufio.Reader, helloResponse) {
	t.Helper()
	peer, server, _ := connectIOTestHost(t, h)
	_ = peer.SetReadDeadline(time.Now().Add(8 * time.Second))
	if err := json.NewEncoder(peer).Encode(hello{Version: ProtocolVersion, Token: h.token, Action: "attach"}); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(peer)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response helloResponse
	if err := json.Unmarshal(line, &response); err != nil || !response.OK {
		t.Fatalf("hello = %s, error = %v", line, err)
	}
	return peer, server, reader, response
}

func assertHostStateAvailable(t *testing.T, h *host) {
	t.Helper()
	available := make(chan struct{})
	go func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		close(available)
	}()
	select {
	case <-available:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("controller socket write held the shared host mutex")
	}
}

func TestHostServerExpiresIncompleteHello(t *testing.T) {
	h := newIOTestHost(t)
	peer, _, done := connectIOTestHost(t, h)
	if _, err := io.WriteString(peer, `{"version":`); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * handshakeTimeout):
		t.Fatal("incomplete unauthenticated hello outlived the server handshake budget")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.client != nil || h.clientGeneration != 0 {
		t.Fatal("incomplete hello acquired provider ownership")
	}
}

func TestHostStalledControllerReleasesStateAndReplaysReplacement(t *testing.T) {
	h := newIOTestHost(t)
	peer, server, reader, _ := attachIOTestHost(t, h)
	provider, output := io.Pipe()
	forwardDone := make(chan error, 1)
	go func() { forwardDone <- h.forwardProvider(provider) }()
	t.Cleanup(func() {
		_ = peer.Close()
		_ = output.Close()
		_ = provider.Close()
		select {
		case <-forwardDone:
		case <-time.After(2 * time.Second):
			t.Error("provider forwarding did not exit")
		}
	})
	request := []byte("{\"id\":700,\"method\":\"approval\"}\n")
	if _, err := output.Write(request); err != nil {
		t.Fatal(err)
	}
	if got := readFrame(t, reader); !bytes.Equal(got, request) {
		t.Fatal("pending provider request changed")
	}
	h.observeClientFrame([]byte(`{"id":42,"method":"work"}`))
	large := []byte("{\"method\":\"update\",\"data\":\"" + strings.Repeat("x", 2<<20) + "\"}\n")
	continued := make(chan error, 1)
	go func() {
		_, writeErr := output.Write(large)
		if writeErr == nil {
			_, writeErr = output.Write([]byte("{\"method\":\"after-stall\"}\n"))
		}
		continued <- writeErr
	}()
	<-server.writeStarted
	assertHostStateAvailable(t, h)
	// An authenticated rival should receive the ownership error promptly even
	// while the current controller has stopped consuming provider output.
	rival, _, _ := connectIOTestHost(t, h)
	_ = rival.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := json.NewEncoder(rival).Encode(hello{Version: ProtocolVersion, Token: h.token, Action: "attach"}); err != nil {
		t.Fatal(err)
	}
	var rejected helloResponse
	if err := json.NewDecoder(rival).Decode(&rejected); err != nil || rejected.Error != ErrAttached.Error() {
		t.Fatalf("rival hello = %+v, error = %v", rejected, err)
	}
	select {
	case err := <-continued:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("nonreading controller prevented provider forwarding from continuing")
	}
	h.mu.Lock()
	attached := h.client != nil
	h.mu.Unlock()
	if attached {
		t.Fatal("nonreading controller was not detached")
	}
	_, _, replacement, response := attachIOTestHost(t, h)
	if response.NextRequestID != 42 {
		t.Fatalf("replacement request id = %d, want 42", response.NextRequestID)
	}
	for i, want := range [][]byte{request, large, []byte("{\"method\":\"after-stall\"}\n")} {
		if got := readFrame(t, replacement); !bytes.Equal(got, want) {
			t.Fatalf("replacement frame %d changed or reordered", i)
		}
	}
	select {
	case <-h.shutdown:
		t.Fatal("controller timeout shut down provider ownership")
	default:
	}
}

func TestHostACPReplayReleasesStateAndSurvivesStalledController(t *testing.T) {
	h := newIOTestHost(t)
	h.acp = newTestACPRelay(t)
	prompt := relayClientFrame(t, h.acp, []byte(`{"id":1,"method":"session/prompt","params":{}}`), 0)
	update := []byte("{\"method\":\"session/update\",\"params\":{\"data\":\"" + strings.Repeat("x", 2<<20) + "\"}}\n")
	_, _ = relayProviderFrame(t, h.acp, update, 0, false)
	_, _ = relayProviderFrame(t, h.acp, []byte(`{"id":`+frameID(t, prompt)+`,"result":{"stopReason":"end_turn"}}`), 0, false)
	expected := bytes.Join(relayReplayFrames(t, h.acp), nil)
	peer, server, _, response := attachIOTestHost(t, h)
	if response.ACPState == nil || response.ACPState.PendingResultEventID == "" {
		t.Fatal("handshake lost durable prompt state")
	}
	<-server.writeStarted
	assertHostStateAvailable(t, h)
	_ = peer.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		h.mu.Lock()
		detached := h.client == nil
		h.mu.Unlock()
		if detached {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("failed ACP replay retained the closed controller")
		}
		time.Sleep(time.Millisecond)
	}
	_, _, reader, replacement := attachIOTestHost(t, h)
	if replacement.ACPState.PendingResultEventID != response.ACPState.PendingResultEventID {
		t.Fatal("replacement changed the durable result identity")
	}
	actual := append(readFrame(t, reader), readFrame(t, reader)...)
	if !bytes.Equal(actual, expected) {
		t.Fatal("replacement ACP replay changed journal contents or order")
	}
	if errors.Is(h.ctx.Err(), context.Canceled) {
		t.Fatal("controller disconnection canceled host ownership")
	}
}

func TestHostServerClearsHelloDeadlineForIdleController(t *testing.T) {
	h := newIOTestHost(t)
	_, _, reader, _ := attachIOTestHost(t, h)
	time.Sleep(handshakeTimeout + 50*time.Millisecond)
	frame := "{\"method\":\"after-idle\"}\n"
	if err := h.forwardProvider(strings.NewReader(frame)); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if got := readFrame(t, reader); string(got) != frame {
		t.Fatal("idle controller did not receive the later provider frame")
	}
}

func TestHostCancellationReleasesDetachedCapacityWait(t *testing.T) {
	h := newIOTestHost(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.ctx = ctx
	waiting := make(chan struct{})
	h.cond = sync.NewCond(&observedHostWait{Locker: &h.mu, waiting: waiting})
	// Exercise a full replay's capacity accounting without retaining 32 MiB in
	// this cancellation-only test. No replay or buffer content is consumed.
	h.detachedBytes = maxDetachedBytes
	done := make(chan error, 1)
	go func() { done <- h.forwardProvider(strings.NewReader("next\n")) }()
	// Cond.Wait releases its locker after registering the waiter, so this signal
	// proves cancellation reaches the capacity wait rather than an earlier check.
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("provider forwarding did not enter the capacity wait")
	}
	assertHostStateAvailable(t, h)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("capacity wait error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled host remained blocked on detached capacity")
	}
}

func TestHostOldGenerationCannotForwardAfterReplacement(t *testing.T) {
	h := newIOTestHost(t)
	_, first, _, _ := attachIOTestHost(t, h)
	h.detach(first)
	_, _, _, _ = attachIOTestHost(t, h)
	if err := h.forwardClientFrame(first, 1, []byte(`{"id":99,"method":"stale"}`)); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("stale controller error = %v", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.maxRequestID != 0 || h.clientGeneration != 2 {
		t.Fatal("stale controller changed replacement request state")
	}
}
