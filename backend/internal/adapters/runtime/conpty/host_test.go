package conpty

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// fakePTY implements ptyConn using in-memory pipes. Used only in tests;
// the real ConPTY impl is Windows-only.
// ---------------------------------------------------------------------------

type fakePTY struct {
	// output is what the fake "terminal" writes to the host (PTY -> host reader)
	outR *io.PipeReader
	outW *io.PipeWriter

	// input is what the host writes to the fake terminal (keystrokes)
	inR *io.PipeReader
	inW *io.PipeWriter

	resizeMu sync.Mutex
	resizes  []ResizePayload

	doneOnce sync.Once
	doneC    chan struct{}
	exitCode int
	closed   bool
	closeMu  sync.Mutex

	pid int
}

func newFakePTY(pid int) *fakePTY {
	outR, outW := io.Pipe()
	inR, inW := io.Pipe()
	return &fakePTY{
		outR:  outR,
		outW:  outW,
		inR:   inR,
		inW:   inW,
		doneC: make(chan struct{}),
		pid:   pid,
	}
}

// WriteOutput simulates the PTY producing output (e.g. shell printing text).
func (f *fakePTY) WriteOutput(data []byte) (int, error) { return f.outW.Write(data) }

// CloseOutput simulates the PTY process exiting (closes the read side).
func (f *fakePTY) CloseOutput(code int) {
	f.exitCode = code
	f.outW.Close()
}

// ReadInput lets tests inspect what the host forwarded to the PTY.
func (f *fakePTY) ReadInput(buf []byte) (int, error) { return f.inR.Read(buf) }

// ptyConn interface implementation.
func (f *fakePTY) Read(b []byte) (int, error)  { return f.outR.Read(b) }
func (f *fakePTY) Write(b []byte) (int, error) { return f.inW.Write(b) }

func (f *fakePTY) Resize(cols, rows int) error {
	f.resizeMu.Lock()
	defer f.resizeMu.Unlock()
	f.resizes = append(f.resizes, ResizePayload{Cols: cols, Rows: rows})
	return nil
}

func (f *fakePTY) Close() error {
	f.closeMu.Lock()
	defer f.closeMu.Unlock()
	f.closed = true
	// Close both pipes so pumpPTY and any Read calls unblock.
	_ = f.outW.Close()
	_ = f.inW.Close()
	f.doneOnce.Do(func() { close(f.doneC) })
	return nil
}

func (f *fakePTY) Done() <-chan struct{} { return f.doneC }

func (f *fakePTY) ExitCode() (int, bool) {
	select {
	case <-f.doneC:
		return f.exitCode, true
	default:
		return 0, false
	}
}

func (f *fakePTY) PID() int { return f.pid }

// signalExit simulates the child process exiting, triggering the Done channel
// and ExitCode returning true.
func (f *fakePTY) signalExit(code int) {
	f.exitCode = code
	f.doneOnce.Do(func() { close(f.doneC) })
	_ = f.outW.Close() // unblocks pumpPTY's Read
}

// ---------------------------------------------------------------------------
// testClient wraps a net.Conn and a MessageParser for easy frame reading.
// ---------------------------------------------------------------------------

type testClient struct {
	conn   net.Conn
	frameC chan struct {
		typ     byte
		payload []byte
	}
	parser *MessageParser
}

func newTestClient(t *testing.T, addr string) *testClient {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	tc := &testClient{
		conn: conn,
		frameC: make(chan struct {
			typ     byte
			payload []byte
		}, 64),
	}
	tc.parser = NewMessageParser(func(msgType byte, payload []byte) {
		tc.frameC <- struct {
			typ     byte
			payload []byte
		}{msgType, payload}
	})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				tc.parser.Feed(buf[:n])
			}
			if err != nil {
				close(tc.frameC)
				return
			}
		}
	}()
	return tc
}

// readFrame blocks until a frame arrives or 2s times out.
func (tc *testClient) readFrame(t *testing.T) (typ byte, payload []byte) {
	t.Helper()
	select {
	case f, ok := <-tc.frameC:
		if !ok {
			t.Fatal("client frame channel closed (connection dropped)")
		}
		return f.typ, f.payload
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for frame")
		return 0, nil
	}
}

// send writes a framed message to the server.
func (tc *testClient) send(msgType byte, payload []byte) error {
	frame, err := EncodeMessage(msgType, payload)
	if err != nil {
		return err
	}
	_, err = tc.conn.Write(frame)
	return err
}

func (tc *testClient) close() { _ = tc.conn.Close() }

// ---------------------------------------------------------------------------
// Helper: start a Serve with a freshly created listener + fakePTY.
// ---------------------------------------------------------------------------

type serveFixture struct {
	pty    *fakePTY
	ring   *Ring
	ln     net.Listener
	addr   string
	cancel context.CancelFunc
	done   chan error
}

func startServe(t *testing.T, pid int) *serveFixture {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	pty := newFakePTY(pid)
	ring := NewRing()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ServeConfig{
			SessionID: fmt.Sprintf("test-%d", pid),
			Listener:  ln,
			PTY:       pty,
			Ring:      ring,
		})
	}()
	return &serveFixture{
		pty:    pty,
		ring:   ring,
		ln:     ln,
		addr:   ln.Addr().String(),
		cancel: cancel,
		done:   done,
	}
}

// waitDone waits for Serve to return (up to 2s).
func (f *serveFixture) waitDone(t *testing.T) {
	t.Helper()
	select {
	case <-f.done:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return in time")
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestScrollbackReplay: seed the ring, connect a client; first frame must be
// MsgTerminalData containing the ring snapshot.
func TestScrollbackReplay(t *testing.T) {
	f := startServe(t, 100)
	defer f.cancel()

	// Seed ring directly before the client connects.
	f.ring.Append([]byte("line1\nline2\n"))
	snap := f.ring.Snapshot()

	c := newTestClient(t, f.addr)
	defer c.close()

	typ, payload := c.readFrame(t)
	if typ != MsgTerminalData {
		t.Fatalf("got type 0x%02x, want MsgTerminalData", typ)
	}
	if string(payload) != string(snap) {
		t.Fatalf("scrollback payload = %q, want %q", payload, snap)
	}
}

// TestScrollbackLiveOrdering_NoDrop is a regression test for the bug where the
// new-client handler took the ring Snapshot and registered the client in two
// separate h.mu acquisitions. A PTY chunk arriving in that gap landed in
// neither the snapshot nor that client's broadcast and was silently dropped,
// producing a hole in the middle of the client's stream.
//
// The PTY emits a long stream of numbered chunks continuously while a client
// connects. The fakePTY's WriteOutput blocks until pumpPTY consumes each chunk,
// so output is interleaved with the connect, exercising the race window. The
// guaranteed invariant is: the client's received byte stream must be a
// CONTIGUOUS suffix of the full PTY sequence, i.e. it may legitimately start
// late (the snapshot only captures whatever was written before the connect),
// but once it starts there must be NO internal gap. The old two-step code
// dropped a chunk between snapshot and registration, leaving an internal hole;
// this test detects that hole. Reliable under -race -count=20: the
// continuous-emit setup reliably lands a chunk in the race window, and it
// reliably fails against the old code.
func TestScrollbackLiveOrdering_NoDrop(t *testing.T) {
	f := startServe(t, 200)
	defer f.cancel()

	// Build the full byte sequence the PTY will ever have produced. Each chunk
	// is a complete line ("[NNNN]\n") so it lands in the ring's snapshot (the
	// ring only stores completed lines), exercising the snapshot-write path as
	// well as the live-broadcast boundary where the drop bug lived.
	const nChunks = 300
	chunk := func(i int) []byte { return []byte(fmt.Sprintf("[%04d]\n", i)) }

	// Emit continuously; each WriteOutput blocks until pumpPTY reads it.
	emitDone := make(chan struct{})
	go func() {
		defer close(emitDone)
		for i := 0; i < nChunks; i++ {
			if _, err := f.pty.WriteOutput(chunk(i)); err != nil {
				return
			}
		}
	}()

	// Connect mid-stream so the snapshot is taken while chunks are in flight.
	c := newTestClient(t, f.addr)
	defer c.close()

	// Collect frames until the client's stream contains the final chunk (the
	// last line the PTY emits), or until the overall deadline. The sentinel is
	// the last line's bytes; once we have seen it, the whole tail has arrived.
	sentinel := string(chunk(nChunks - 1))
	var got []byte
	deadline := time.After(5 * time.Second)
collect:
	for !strings.Contains(string(got), sentinel) {
		select {
		case fr, ok := <-c.frameC:
			if !ok {
				break collect
			}
			if fr.typ != MsgTerminalData {
				t.Fatalf("unexpected frame type 0x%02x", fr.typ)
			}
			got = append(got, fr.payload...)
		case <-deadline:
			break collect
		}
	}
	// Emitter must have finished (it produces the sentinel last).
	select {
	case <-emitDone:
	case <-time.After(time.Second):
		t.Fatal("emitter did not finish")
	}
	if !strings.Contains(string(got), sentinel) {
		t.Fatalf("client never received the final chunk %q; got %d bytes", sentinel, len(got))
	}

	// Parse the received bytes back into the ordered list of line indices.
	// Each line is "[NNNN]\n". The client may legitimately start late (the
	// snapshot only captures lines written before the connect), and a line may
	// appear twice at the snapshot/live seam (a chunk landing in the ring just
	// before this client registers can be both snapshotted and broadcast). The
	// DROP bug instead produced a MISSING index in the middle. So the invariant
	// is: the indices, in order, are non-decreasing, advance by 0 or 1 each
	// step (no jump that skips an index), and reach the final index nChunks-1.
	lines := strings.Split(string(got), "\n")
	// Trailing "" after the final \n.
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	prev := -1
	for li, line := range lines {
		var idx int
		if _, err := fmt.Sscanf(line, "[%04d]", &idx); err != nil {
			t.Fatalf("unparseable line %d %q in client stream: %v", li, line, err)
		}
		if li == 0 {
			prev = idx
			continue
		}
		if idx != prev && idx != prev+1 {
			t.Fatalf("non-contiguous line indices (dropped chunk): %d followed by %d", prev, idx)
		}
		prev = idx
	}
	if prev != nChunks-1 {
		t.Fatalf("client stream did not reach the final chunk: last index %d, want %d", prev, nChunks-1)
	}
}

// TestFanOut: two clients receive the same PTY output.
func TestFanOut(t *testing.T) {
	f := startServe(t, 101)
	defer f.cancel()

	c1 := newTestClient(t, f.addr)
	defer c1.close()
	c2 := newTestClient(t, f.addr)
	defer c2.close()

	// Write PTY output after both clients have connected.
	// We need to give the server a moment to register both clients; use a
	// brief sync by sending a status req from each and waiting for responses.
	// ponytail: channel-based sync via status round-trip avoids sleeps.
	_ = c1.send(MsgStatusReq, nil)
	_ = c2.send(MsgStatusReq, nil)
	// Drain status responses.
	c1.readFrame(t)
	c2.readFrame(t)

	msg := []byte("hello from pty\n")
	if _, err := f.pty.WriteOutput(msg); err != nil {
		t.Fatalf("WriteOutput: %v", err)
	}

	// Both clients should receive a MsgTerminalData with msg.
	for _, c := range []*testClient{c1, c2} {
		typ, payload := c.readFrame(t)
		if typ != MsgTerminalData {
			t.Fatalf("got type 0x%02x, want MsgTerminalData", typ)
		}
		if string(payload) != string(msg) {
			t.Fatalf("payload = %q, want %q", payload, msg)
		}
	}
}

// TestSlowClientDoesNotBlockFanOut reproduces the macOS preview failure where
// one terminal connection stopped reading. The old host wrote to every socket
// while holding h.mu, so that one blocked Write froze status probes, attaches,
// and output for all clients; the UI stayed at a blinking empty terminal and
// timed-out probes accumulated in CLOSE_WAIT. Per-client writers must isolate
// that back-pressure and keep every fast viewer moving.
func TestSlowClientDoesNotBlockFanOut(t *testing.T) {
	slowHost, slowPeer := net.Pipe()
	fastHost, fastPeer := net.Pipe()

	h := &host{clients: make(map[net.Conn]*clientState)}
	slow := newClientState()
	fast := newClientState()
	h.clients[slowHost] = slow
	h.clients[fastHost] = fast
	go h.writeClient(slowHost, slow)
	go h.writeClient(fastHost, fast)
	t.Cleanup(func() {
		h.removeClient(slowHost, slow)
		h.removeClient(fastHost, fast)
		_ = slowPeer.Close()
		_ = fastPeer.Close()
	})

	frame, err := EncodeMessage(MsgTerminalData, []byte("still moving"))
	if err != nil {
		t.Fatalf("encode terminal data: %v", err)
	}
	broadcastDone := make(chan struct{})
	go func() {
		h.broadcast(frame)
		close(broadcastDone)
	}()

	select {
	case <-broadcastDone:
	case <-time.After(time.Second):
		t.Fatal("broadcast blocked behind a client that was not reading")
	}

	if err := fastPeer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set fast-client deadline: %v", err)
	}
	got := make([]byte, len(frame))
	if _, err := io.ReadFull(fastPeer, got); err != nil {
		t.Fatalf("fast client did not receive broadcast: %v", err)
	}
	if string(got) != string(frame) {
		t.Fatalf("fast client frame = %x, want %x", got, frame)
	}
}

// TestTerminalInput: MsgTerminalInput from a client reaches the fakePTY's input.
func TestTerminalInput(t *testing.T) {
	f := startServe(t, 102)
	defer f.cancel()

	c := newTestClient(t, f.addr)
	defer c.close()

	keystrokes := []byte("ls -la\r")
	if err := c.send(MsgTerminalInput, keystrokes); err != nil {
		t.Fatalf("send: %v", err)
	}

	buf := make([]byte, len(keystrokes))
	if _, err := io.ReadFull(f.pty.inR, buf); err != nil {
		t.Fatalf("read from pty input: %v", err)
	}
	if string(buf) != string(keystrokes) {
		t.Fatalf("pty input = %q, want %q", buf, keystrokes)
	}
}

// TestResize: MsgResize calls fakePTY.Resize with the right cols/rows.
func TestResize(t *testing.T) {
	f := startServe(t, 103)
	defer f.cancel()

	c := newTestClient(t, f.addr)
	defer c.close()

	payload, _ := json.Marshal(ResizePayload{Cols: 132, Rows: 40})
	if err := c.send(MsgResize, payload); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Poll for the resize to arrive (it's async). Channel-based: send a
	// status req and wait for its reply, which guarantees the resize was
	// processed (single goroutine handles all messages per connection).
	_ = c.send(MsgStatusReq, nil)
	c.readFrame(t) // discard status response

	f.pty.resizeMu.Lock()
	resizes := f.pty.resizes
	f.pty.resizeMu.Unlock()

	if len(resizes) != 1 {
		t.Fatalf("got %d resize calls, want 1", len(resizes))
	}
	if resizes[0].Cols != 132 || resizes[0].Rows != 40 {
		t.Fatalf("resize = %+v, want {132 40}", resizes[0])
	}
}

// resizeSnapshot returns a copy of the fakePTY's recorded resizes.
func (f *fakePTY) resizeSnapshot() []ResizePayload {
	f.resizeMu.Lock()
	defer f.resizeMu.Unlock()
	return append([]ResizePayload(nil), f.resizes...)
}

// waitResizes polls until the fakePTY has recorded at least n resizes (or 2s).
func (f *fakePTY) waitResizes(t *testing.T, n int) []ResizePayload {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if got := f.resizeSnapshot(); len(got) >= n {
			return got
		}
		select {
		case <-deadline:
			t.Fatalf("timeout: got %d resizes, want >= %d", len(f.resizeSnapshot()), n)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// syncResize sends a resize then a status round-trip on the same conn. Because a
// connection's messages are handled by one goroutine in order, draining the
// status response guarantees the resize has already been processed.
func syncResize(t *testing.T, c *testClient, cols, rows int) {
	t.Helper()
	payload, _ := json.Marshal(ResizePayload{Cols: cols, Rows: rows})
	if err := c.send(MsgResize, payload); err != nil {
		t.Fatalf("send resize: %v", err)
	}
	if err := c.send(MsgStatusReq, nil); err != nil {
		t.Fatalf("send status: %v", err)
	}
	c.readFrame(t) // status response — proves the resize was processed
}

// TestResizeLargestWins: with two clients on one PTY, the shared grid follows
// the LARGEST client — a smaller client attaching or resizing must not shrink
// the PTY — and when the large client leaves, the grid falls back to the
// remaining largest client. This is the fix for the phone stripping down the
// desktop's terminal (both attach to the same session's single PTY).
func TestResizeLargestWins(t *testing.T) {
	f := startServe(t, 110)
	defer f.cancel()

	big := newTestClient(t, f.addr)
	defer big.close()
	small := newTestClient(t, f.addr)
	defer small.close()

	// Big client sizes the PTY up.
	syncResize(t, big, 200, 50)
	got := f.pty.waitResizes(t, 1)
	if got[0].Cols != 200 || got[0].Rows != 50 {
		t.Fatalf("first resize = %+v, want {200 50}", got[0])
	}

	// Small client asks for a smaller grid: the shared PTY must NOT shrink, so no
	// additional resize is issued (the max is unchanged).
	syncResize(t, small, 45, 22)
	if got := f.pty.resizeSnapshot(); len(got) != 1 {
		t.Fatalf("small client changed the grid: resizes = %+v, want just [{200 50}]", got)
	}

	// The big client leaves: the grid falls back to the remaining (small) client.
	big.close()
	got = f.pty.waitResizes(t, 2)
	last := got[len(got)-1]
	if last.Cols != 45 || last.Rows != 22 {
		t.Fatalf("after big client left, resize = %+v, want {45 22}", last)
	}
}

// TestResizeMatchesSingleLargestClient: with a wide-but-short client and a
// narrow-but-tall one, the PTY must match ONE client's grid exactly (the larger
// by area) — never an independent per-axis max. A synthesized wide-AND-tall grid
// (120x48 here) has no client behind it and mis-renders for both: the desktop
// grows phantom rows, the phone phantom columns. This is the regression that made
// the desktop terminal "also get weird" while co-viewing.
func TestResizeMatchesSingleLargestClient(t *testing.T) {
	f := startServe(t, 111)
	defer f.cancel()

	wide := newTestClient(t, f.addr) // desktop-like: wide, short (area 3600)
	defer wide.close()
	tall := newTestClient(t, f.addr) // phone-like: narrow, tall (area 2640)
	defer tall.close()

	syncResize(t, wide, 120, 30)
	f.pty.waitResizes(t, 1)
	// The smaller-area client asks for MORE rows than the wide one; a per-axis max
	// would bump the grid to 120x48. Area-based selection must keep it at 120x30.
	syncResize(t, tall, 55, 48)

	got := f.pty.resizeSnapshot()
	last := got[len(got)-1]
	if last.Cols != 120 || last.Rows != 30 {
		t.Fatalf("grid = %+v, want the larger single client {120 30}", last)
	}
	for _, r := range got {
		if r.Cols == 120 && r.Rows == 48 {
			t.Fatal("PTY was sized to a synthesized per-axis-max grid {120 48}; it must match one client exactly")
		}
	}
}

// TestGetOutputReq: MsgGetOutputReq returns MsgGetOutputRes with ring.Tail(n).
func TestGetOutputReq(t *testing.T) {
	f := startServe(t, 104)
	defer f.cancel()

	f.ring.Append([]byte("alpha\nbeta\ngamma\n"))

	c := newTestClient(t, f.addr)
	defer c.close()

	// Drain scrollback frame.
	c.readFrame(t)

	reqPayload, _ := json.Marshal(GetOutputReq{Lines: 2})
	if err := c.send(MsgGetOutputReq, reqPayload); err != nil {
		t.Fatalf("send: %v", err)
	}

	typ, payload := c.readFrame(t)
	if typ != MsgGetOutputRes {
		t.Fatalf("got type 0x%02x, want MsgGetOutputRes", typ)
	}
	want := f.ring.Tail(2)
	if string(payload) != want {
		t.Fatalf("GetOutputRes = %q, want %q", payload, want)
	}
}

// TestStatusReq_AliveAndExited: MsgStatusReq returns alive:true while running;
// after the PTY exits, returns alive:false with exitCode. Listener stays open.
func TestStatusReq_AliveAndExited(t *testing.T) {
	f := startServe(t, 105)
	defer f.cancel()

	c := newTestClient(t, f.addr)
	defer c.close()

	// While running: expect alive:true.
	if err := c.send(MsgStatusReq, nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	typ, payload := c.readFrame(t)
	if typ != MsgStatusRes {
		t.Fatalf("got type 0x%02x, want MsgStatusRes", typ)
	}
	var sp StatusPayload
	if err := json.Unmarshal(payload, &sp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !sp.Alive {
		t.Fatalf("expected alive=true, got false")
	}
	if sp.PID != 105 {
		t.Fatalf("expected pid=105, got %d", sp.PID)
	}
	if sp.ProtocolVersion != conPTYHostProtocolVersion {
		t.Fatalf("protocol version = %d, want %d", sp.ProtocolVersion, conPTYHostProtocolVersion)
	}

	// Simulate PTY exit.
	f.pty.signalExit(42)

	// Drain the broadcast status-res that pumpPTY sends on exit.
	exitBcast, _ := c.readFrame(t)
	if exitBcast != MsgStatusRes {
		t.Fatalf("exit broadcast type = 0x%02x, want MsgStatusRes", exitBcast)
	}

	// Now a new status req should report alive:false.
	if err := c.send(MsgStatusReq, nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	typ2, payload2 := c.readFrame(t)
	if typ2 != MsgStatusRes {
		t.Fatalf("got type 0x%02x, want MsgStatusRes", typ2)
	}
	var sp2 StatusPayload
	if err := json.Unmarshal(payload2, &sp2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if sp2.Alive {
		t.Fatalf("expected alive=false after exit")
	}
	if sp2.ExitCode == nil || *sp2.ExitCode != 42 {
		t.Fatalf("expected exitCode=42, got %v", sp2.ExitCode)
	}

	// Keep-alive: the listener must still accept new connections.
	c2 := newTestClient(t, f.addr)
	defer c2.close()
	if err := c2.send(MsgStatusReq, nil); err != nil {
		t.Fatalf("keep-alive send: %v", err)
	}
	_, _ = c2.readFrame(t) // just verify it didn't crash
}

// TestKillReq: MsgKillReq disposes the fakePTY, drops clients, closes
// listener, and Serve returns.
func TestKillReq(t *testing.T) {
	f := startServe(t, 106)

	c := newTestClient(t, f.addr)

	if err := c.send(MsgKillReq, nil); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Serve should return within 2s (includes the 50ms grace sleep).
	f.waitDone(t)

	// PTY Close must have been called.
	f.pty.closeMu.Lock()
	closed := f.pty.closed
	f.pty.closeMu.Unlock()
	if !closed {
		t.Fatal("expected pty.Close() to be called on kill")
	}

	// Listener should be closed: new dial must fail.
	conn, err := net.DialTimeout("tcp", f.addr, 200*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatal("expected listener to be closed after kill, but Dial succeeded")
	}

	c.close()
}

// TestShutdownViaCtxCancel: cancelling the context triggers graceful shutdown.
func TestShutdownViaCtxCancel(t *testing.T) {
	f := startServe(t, 107)

	c := newTestClient(t, f.addr)
	defer c.close()

	// Cancel the context.
	f.cancel()

	f.waitDone(t)

	// PTY Close must have been called.
	f.pty.closeMu.Lock()
	closed := f.pty.closed
	f.pty.closeMu.Unlock()
	if !closed {
		t.Fatal("expected pty.Close() on ctx cancel")
	}
}
