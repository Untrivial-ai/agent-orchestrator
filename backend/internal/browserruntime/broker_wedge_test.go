package browserruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

// Tests for a browser runtime that is attached but unresponsive: it accepts
// the command frame and never answers. This is the wedge behind the
// connected-but-timed-out 503 bursts tracked in #5369 — the desktop main
// process can be wedged (busy event loop, system sleep, wedged network stack)
// while its socket to the broker stays open.

type wedgeHarness struct {
	broker *Broker
	addr   string
	conn   net.Conn
	enc    *json.Encoder
	dec    *json.Decoder
}

func newWedgeHarness(t *testing.T) *wedgeHarness {
	return newTunedWedgeHarness(t, nil)
}

// newTunedWedgeHarness builds the standard harness while letting liveness
// tests tune broker knobs before Serve starts.
func newTunedWedgeHarness(t *testing.T, tune func(*Broker)) *wedgeHarness {
	t.Helper()
	broker := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if tune != nil {
		tune(broker)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = broker.Serve(ctx, ln) }()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	enc := json.NewEncoder(conn)
	if err := enc.Encode(wireMessage{Type: "hello", Version: ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	waitConnected(t, broker)
	return &wedgeHarness{
		broker: broker,
		addr:   ln.Addr().String(),
		conn:   conn,
		enc:    enc,
		dec:    json.NewDecoder(conn),
	}
}

type executeOutcome struct {
	result Result
	err    error
}

// startWedgeCommand issues one command whose context expires after timeout
// while the harness runtime stays silent, and returns its outcome channel plus
// the command frame the broker wrote.
func startWedgeCommand(t *testing.T, h *wedgeHarness, action string, timeout time.Duration) (<-chan executeOutcome, wireMessage, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	outcomes := make(chan executeOutcome, 1)
	go func() {
		result, err := h.broker.Execute(ctx, "session-1", action, nil)
		outcomes <- executeOutcome{result: result, err: err}
	}()
	_ = h.conn.SetReadDeadline(time.Now().Add(browserRuntimeTestTimeout()))
	var command wireMessage
	if err := h.dec.Decode(&command); err != nil {
		t.Fatal(err)
	}
	waitPendingRequest(t, h.broker, command.RequestID)
	return outcomes, command, cancel
}

// expectWedgeTimeout asserts the bounded failure for a silent runtime: the
// broker answers the expired request with a cancel frame on the same socket,
// returns context.DeadlineExceeded to the caller, and cleans the registry.
func expectWedgeTimeout(t *testing.T, h *wedgeHarness, outcomes <-chan executeOutcome, command wireMessage) {
	t.Helper()
	var cancelMessage wireMessage
	if err := h.dec.Decode(&cancelMessage); err != nil {
		t.Fatal(err)
	}
	if cancelMessage.Type != "cancel" || cancelMessage.RequestID != command.RequestID {
		t.Fatalf("cancel message = %#v, command = %#v", cancelMessage, command)
	}
	select {
	case outcome := <-outcomes:
		if !errors.Is(outcome.err, context.DeadlineExceeded) {
			t.Fatalf("Execute error = %v, want context deadline", outcome.err)
		}
	case <-time.After(browserRuntimeTestTimeout()):
		t.Fatal("Execute did not return after the request deadline")
	}
	assertPendingEmpty(t, h.broker)
}

func assertPendingEmpty(t *testing.T, broker *Broker) {
	t.Helper()
	broker.mu.Lock()
	pending := len(broker.pending)
	broker.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending registry holds %d entries, want 0", pending)
	}
}

func TestBrokerExecuteDeadlineWithSilentRuntime(t *testing.T) {
	h := newWedgeHarness(t)
	outcomes, command, cancel := startWedgeCommand(t, h, "open", 100*time.Millisecond)
	defer cancel()
	expectWedgeTimeout(t, h, outcomes, command)
}

func TestBrokerNextCommandBoundedAfterWedge(t *testing.T) {
	h := newWedgeHarness(t)

	// The first command wedges and times out.
	outcomes1, command1, cancel1 := startWedgeCommand(t, h, "open", 100*time.Millisecond)
	defer cancel1()
	expectWedgeTimeout(t, h, outcomes1, command1)

	// The next command on the same connection is bounded the same way and gets
	// a fresh request id.
	outcomes2, command2, cancel2 := startWedgeCommand(t, h, "snapshot", 100*time.Millisecond)
	defer cancel2()
	if command2.RequestID == command1.RequestID {
		t.Fatal("broker reused the wedged request id")
	}
	expectWedgeTimeout(t, h, outcomes2, command2)

	// A late result for the expired request must be ignored without disturbing
	// the registry, and the broker keeps serving commands afterwards.
	if err := h.enc.Encode(wireMessage{
		Type:      "result",
		RequestID: command1.RequestID,
		OK:        true,
		Result:    json.RawMessage(`{"late":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	outcomes3, command3, cancel3 := startWedgeCommand(t, h, "snapshot", 2*browserRuntimeTestTimeout())
	defer cancel3()
	if err := h.enc.Encode(wireMessage{
		Type:      "result",
		RequestID: command3.RequestID,
		OK:        true,
		Result:    json.RawMessage(`{"ok":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-outcomes3:
		if outcome.err != nil {
			t.Fatalf("command after wedge failed: %v", outcome.err)
		}
	case <-time.After(browserRuntimeTestTimeout()):
		t.Fatal("command after wedge did not complete")
	}
	assertPendingEmpty(t, h.broker)
}

func TestBrokerStatusReportsConnectedDuringWedge(t *testing.T) {
	h := newWedgeHarness(t)
	outcomes, command, cancel := startWedgeCommand(t, h, "open", 2*browserRuntimeTestTimeout())
	defer cancel()

	// #5369: Status() reports transport connectivity only (conn != nil), so the
	// desktop keeps showing "connected" while a command is wedged inside the
	// runtime. Pin that contract here; surfacing degradation is left to the fix.
	status := h.broker.Status()
	if !status.Connected {
		t.Fatal("status dropped connected while the runtime socket is attached")
	}
	if status.ConnectedAt.IsZero() {
		t.Fatal("connectedAt is zero while the runtime socket is attached")
	}

	// The runtime eventually answers, and the wedged command completes.
	if err := h.enc.Encode(wireMessage{
		Type:      "result",
		RequestID: command.RequestID,
		OK:        true,
		Result:    json.RawMessage(`{"ok":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-outcomes:
		if outcome.err != nil {
			t.Fatalf("wedged command failed after the runtime answered: %v", outcome.err)
		}
	case <-time.After(browserRuntimeTestTimeout()):
		t.Fatal("wedged command did not complete after the runtime answered")
	}
}

func TestBrokerNewConnectionFailsStalePending(t *testing.T) {
	h := newWedgeHarness(t)
	outcomes, _, cancel := startWedgeCommand(t, h, "open", 2*browserRuntimeTestTimeout())
	defer cancel()

	// A replacement runtime connection — the desktop app recovering from a
	// wedge — takes over the broker and fails everything still in flight on
	// the stale socket.
	replacement, err := net.Dial("tcp", h.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replacement.Close() }()
	replacementEnc := json.NewEncoder(replacement)
	if err := replacementEnc.Encode(wireMessage{Type: "hello", Version: ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	waitConnected(t, h.broker)

	select {
	case outcome := <-outcomes:
		if !errors.Is(outcome.err, ErrUnavailable) {
			t.Fatalf("stale command error = %v, want ErrUnavailable", outcome.err)
		}
	case <-time.After(browserRuntimeTestTimeout()):
		t.Fatal("stale in-flight command was not failed by the replacement connection")
	}
	if !h.broker.Status().Connected {
		t.Fatal("replacement connection did not keep the broker connected")
	}

	// The replacement connection carries new commands end to end.
	resultCh := make(chan executeOutcome, 1)
	go func() {
		result, err := h.broker.Execute(context.Background(), "session-1", "snapshot", nil)
		resultCh <- executeOutcome{result: result, err: err}
	}()
	_ = replacement.SetReadDeadline(time.Now().Add(browserRuntimeTestTimeout()))
	var command wireMessage
	if err := json.NewDecoder(replacement).Decode(&command); err != nil {
		t.Fatal(err)
	}
	if err := replacementEnc.Encode(wireMessage{
		Type:      "result",
		RequestID: command.RequestID,
		OK:        true,
		Result:    json.RawMessage(`{"ok":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-resultCh:
		if outcome.err != nil {
			t.Fatalf("command on replacement connection failed: %v", outcome.err)
		}
	case <-time.After(browserRuntimeTestTimeout()):
		t.Fatal("command on replacement connection did not complete")
	}
}

// startPonger takes over reading the runtime side of the harness connection:
// it answers ping frames out of band and forwards every other frame to the
// returned channel. Do not use h.dec once the ponger is running.
func startPonger(t *testing.T, h *wedgeHarness) <-chan wireMessage {
	t.Helper()
	frames := make(chan wireMessage, 16)
	go func() {
		dec := json.NewDecoder(h.conn)
		for {
			var msg wireMessage
			if err := dec.Decode(&msg); err != nil {
				return
			}
			if msg.Type == "ping" {
				_ = h.enc.Encode(wireMessage{Type: "pong"})
				continue
			}
			select {
			case frames <- msg:
			case <-time.After(browserRuntimeTestTimeout()):
				return
			}
		}
	}()
	return frames
}

// The liveness tests below cover #5369's wedge: an attached runtime whose
// desktop process stopped scheduling never answers pings, and the broker must
// notice instead of reporting "connected" forever.

func TestBrokerUnresponsiveRuntimeFailsPendingFast(t *testing.T) {
	h := newTunedWedgeHarness(t, func(b *Broker) {
		b.liveness = livenessConfig{interval: 20 * time.Millisecond, stallLimit: 80 * time.Millisecond}
	})
	requestCtx, cancel := context.WithTimeout(context.Background(), 2*browserRuntimeTestTimeout())
	defer cancel()
	outcomes := make(chan executeOutcome, 1)
	go func() {
		_, err := h.broker.Execute(requestCtx, "session-1", "open", nil)
		outcomes <- executeOutcome{err: err}
	}()
	_ = h.conn.SetReadDeadline(time.Now().Add(browserRuntimeTestTimeout()))
	var command wireMessage
	if err := h.dec.Decode(&command); err != nil {
		t.Fatal(err)
	}
	waitPendingRequest(t, h.broker, command.RequestID)

	// The runtime never answers. Liveness detection must tear the connection
	// down and fail the command long before the request context expires.
	select {
	case outcome := <-outcomes:
		if !errors.Is(outcome.err, ErrUnavailable) {
			t.Fatalf("stale command error = %v, want ErrUnavailable", outcome.err)
		}
	case <-time.After(browserRuntimeTestTimeout()):
		t.Fatal("unresponsive runtime was not failed by liveness detection")
	}
	if h.broker.Status().Connected {
		t.Fatal("unresponsive runtime still reported connected")
	}
}

func TestBrokerResponsiveRuntimeSurvivesLiveness(t *testing.T) {
	h := newTunedWedgeHarness(t, func(b *Broker) {
		b.liveness = livenessConfig{interval: 20 * time.Millisecond, stallLimit: 80 * time.Millisecond}
	})
	frames := startPonger(t, h)

	requestCtx, cancel := context.WithTimeout(context.Background(), 2*browserRuntimeTestTimeout())
	defer cancel()
	outcomes := make(chan executeOutcome, 1)
	go func() {
		result, err := h.broker.Execute(requestCtx, "session-1", "wait", nil)
		outcomes <- executeOutcome{result: result, err: err}
	}()

	// Hold the command open across several liveness windows while the runtime
	// answers every ping, then let it complete.
	var command wireMessage
	select {
	case command = <-frames:
	case <-time.After(browserRuntimeTestTimeout()):
		t.Fatal("timed out waiting for the command frame")
	}
	if command.Type != "command" {
		t.Fatalf("command = %#v", command)
	}
	time.Sleep(200 * time.Millisecond)
	if err := h.enc.Encode(wireMessage{
		Type:      "result",
		RequestID: command.RequestID,
		OK:        true,
		Result:    json.RawMessage(`{"ok":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-outcomes:
		if outcome.err != nil {
			t.Fatalf("responsive runtime command failed: %v", outcome.err)
		}
	case <-time.After(browserRuntimeTestTimeout()):
		t.Fatal("responsive runtime command did not complete")
	}
	if !h.broker.Status().Connected {
		t.Fatal("responsive runtime was disconnected by liveness detection")
	}
}

func TestBrokerHelloVersionNegotiation(t *testing.T) {
	broker := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	broker.liveness = livenessConfig{interval: 20 * time.Millisecond, stallLimit: 80 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = broker.Serve(ctx, ln) }()

	// v1 is refused outright.
	stale, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(stale).Encode(wireMessage{Type: "hello", Version: 1}); err != nil {
		t.Fatal(err)
	}
	_ = stale.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := stale.Read(make([]byte, 1)); err == nil {
		t.Fatal("protocol v1 runtime connection remained open")
	}
	_ = stale.Close()
	if broker.Status().Connected {
		t.Fatal("protocol v1 runtime was accepted")
	}

	// v3 hellos are accepted even though the wire contract moved from v2, so
	// a freshly built desktop app can talk to a not-yet-upgraded daemon.
	current, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = current.Close() }()
	if err := json.NewEncoder(current).Encode(wireMessage{Type: "hello", Version: 3}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(browserRuntimeTestTimeout())
	for !broker.Status().Connected {
		if time.Now().After(deadline) {
			t.Fatal("protocol v3 runtime was not accepted")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestBrokerLegacyRuntimeGetsNoPings(t *testing.T) {
	broker := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	broker.liveness = livenessConfig{interval: 20 * time.Millisecond, stallLimit: 80 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = broker.Serve(ctx, ln) }()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := json.NewEncoder(conn).Encode(wireMessage{Type: "hello", Version: 2}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(browserRuntimeTestTimeout())
	for !broker.Status().Connected {
		if time.Now().After(deadline) {
			t.Fatal("protocol v2 runtime was not accepted")
		}
		time.Sleep(time.Millisecond)
	}

	// Several liveness windows pass: a legacy runtime must not see a single
	// ping frame, and the broker must keep treating it as connected.
	_ = conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	var frame wireMessage
	if err := json.NewDecoder(conn).Decode(&frame); err == nil {
		t.Fatalf("legacy runtime received an unexpected frame: %#v", frame)
	} else {
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Fatalf("legacy runtime connection failed unexpectedly: %v", err)
		}
	}
	if !broker.Status().Connected {
		t.Fatal("legacy runtime was disconnected by liveness detection")
	}
}
