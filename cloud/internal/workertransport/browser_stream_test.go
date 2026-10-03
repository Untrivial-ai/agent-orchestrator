package workertransport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/browsercontract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/browserstream"
	"github.com/coder/websocket"
)

type blockingBrowserWireWriter struct{}

func (blockingBrowserWireWriter) Write(ctx context.Context, _ websocket.MessageType, _ []byte) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestBrowserStreamAttachesBrowserdAndBridgesBothDirections(t *testing.T) {
	localConnections := make(chan *websocket.Conn, 1)
	localServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(browsercontract.CapabilityHeader) != "capability" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		localConnections <- connection
	}))
	defer localServer.Close()

	cpConnections := make(chan *websocket.Conn, 1)
	cpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		cpConnections <- connection
	}))
	defer cpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cpURL := "ws" + cpServer.URL[len("http"):]
	workerConn, _, err := websocket.Dial(ctx, cpURL, nil)
	if err != nil {
		t.Fatalf("dial control plane: %v", err)
	}
	defer workerConn.CloseNow()
	cpConn := <-cpConnections
	defer cpConn.CloseNow()
	bridgeResult := make(chan error, 1)
	go func() { bridgeResult <- bridgeBrowserStream(ctx, workerConn, localServer.URL, "capability") }()

	writeControl(t, ctx, cpConn, browserstream.Control{Type: "attach", Version: browserstream.Version})
	localConn := <-localConnections
	defer localConn.CloseNow()
	writeControl(t, ctx, localConn, browserstream.Control{
		Type: "hello", Version: browserstream.Version, StreamEpoch: 1,
	})
	got := readControl(t, ctx, cpConn)
	if got.Type != "hello" || got.StreamEpoch != 1 {
		t.Fatalf("control plane received %+v", got)
	}

	writeControl(t, ctx, cpConn, browserstream.Control{
		Type: "input", Version: browserstream.Version, InputSeq: 3,
		Kind: "text", Text: "hello",
	})
	got = readControl(t, ctx, localConn)
	if got.Type != "input" || got.InputSeq != 3 || got.Text != "hello" {
		t.Fatalf("browserd received %+v", got)
	}

	frame, err := browserstream.EncodeFrame(browserstream.Frame{
		StreamEpoch: 1, Sequence: 4, Width: 640, Height: 480,
		CapturedMS: 9, TargetID: "tab", JPEG: []byte{0xff, 0xd8, 0xff, 0xd9},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := localConn.Write(ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatalf("browserd write frame: %v", err)
	}
	kind, payload, err := cpConn.Read(ctx)
	if err != nil || kind != websocket.MessageBinary {
		t.Fatalf("control plane read frame: kind=%v err=%v", kind, err)
	}
	if decoded, err := browserstream.DecodeFrame(payload); err != nil || decoded.Sequence != 4 {
		t.Fatalf("decoded frame=%+v err=%v", decoded, err)
	}

	cancel()
	select {
	case <-bridgeResult:
	case <-time.After(time.Second):
		t.Fatal("browser bridge did not stop after cancellation")
	}
}

func TestBrowserStreamReconnectsBrowserdAfterLocalDrop(t *testing.T) {
	localConnections := make(chan *websocket.Conn, 2)
	localServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		localConnections <- connection
	}))
	defer localServer.Close()

	cpConnections := make(chan *websocket.Conn, 1)
	cpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		cpConnections <- connection
	}))
	defer cpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	workerConn, _, err := websocket.Dial(ctx, "ws"+cpServer.URL[len("http"):], nil)
	if err != nil {
		t.Fatal(err)
	}
	defer workerConn.CloseNow()
	cpConn := <-cpConnections
	defer cpConn.CloseNow()
	bridgeResult := make(chan error, 1)
	go func() { bridgeResult <- bridgeBrowserStream(ctx, workerConn, localServer.URL, "capability") }()

	writeControl(t, ctx, cpConn, browserstream.Control{Type: "attach", Version: browserstream.Version})
	first := <-localConnections
	first.CloseNow()
	restarting := readControl(t, ctx, cpConn)
	if restarting.Type != "error" || restarting.Code != "BROWSER_RESTARTING" {
		t.Fatalf("restart control = %+v", restarting)
	}

	var second *websocket.Conn
	select {
	case second = <-localConnections:
	case <-ctx.Done():
		t.Fatal("browserd stream did not reconnect")
	}
	defer second.CloseNow()
	writeControl(t, ctx, second, browserstream.Control{
		Type: "hello", Version: browserstream.Version, StreamEpoch: 2,
	})
	recovered := readControl(t, ctx, cpConn)
	if recovered.Type != "hello" || recovered.StreamEpoch != 2 {
		t.Fatalf("recovered control = %+v", recovered)
	}

	cancel()
	select {
	case <-bridgeResult:
	case <-time.After(time.Second):
		t.Fatal("browser bridge did not stop after recovery")
	}
}

func TestBrowserStreamReconnectsBrowserdWhenFirstFrameStalls(t *testing.T) {
	localConnections := make(chan *websocket.Conn, 2)
	localServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		localConnections <- connection
	}))
	defer localServer.Close()

	cpConnections := make(chan *websocket.Conn, 1)
	cpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		cpConnections <- connection
	}))
	defer cpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	workerConn, _, err := websocket.Dial(ctx, "ws"+cpServer.URL[len("http"):], nil)
	if err != nil {
		t.Fatal(err)
	}
	defer workerConn.CloseNow()
	cpConn := <-cpConnections
	defer cpConn.CloseNow()
	bridgeResult := make(chan error, 1)
	go func() {
		bridgeResult <- bridgeBrowserStreamWithFrameTimeout(ctx, workerConn, localServer.URL, "capability", 25*time.Millisecond)
	}()

	writeControl(t, ctx, cpConn, browserstream.Control{Type: "attach", Version: browserstream.Version})
	first := <-localConnections
	defer first.CloseNow()
	restarting := readControl(t, ctx, cpConn)
	if restarting.Type != "error" || restarting.Code != "BROWSER_RESTARTING" {
		t.Fatalf("restart control = %+v", restarting)
	}

	var second *websocket.Conn
	select {
	case second = <-localConnections:
	case <-ctx.Done():
		t.Fatal("browserd stream did not reconnect after first-frame timeout")
	}
	defer second.CloseNow()
	frame, err := browserstream.EncodeFrame(browserstream.Frame{
		StreamEpoch: 2, Sequence: 1, Width: 640, Height: 480,
		CapturedMS: 9, TargetID: "tab", JPEG: []byte{0xff, 0xd8, 0xff, 0xd9},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Write(ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatalf("browserd write recovery frame: %v", err)
	}
	kind, payload, err := cpConn.Read(ctx)
	if err != nil || kind != websocket.MessageBinary {
		t.Fatalf("control plane read recovery frame: kind=%v err=%v", kind, err)
	}
	if decoded, err := browserstream.DecodeFrame(payload); err != nil || decoded.StreamEpoch != 2 {
		t.Fatalf("decoded recovery frame=%+v err=%v", decoded, err)
	}

	select {
	case err := <-bridgeResult:
		t.Fatalf("browser bridge stopped after recovery: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case <-bridgeResult:
	case <-time.After(time.Second):
		t.Fatal("browser bridge did not stop after cancellation")
	}
}

func TestBrowserWireWriteUsesBoundedContext(t *testing.T) {
	err := writeBrowserWire(
		context.Background(), blockingBrowserWireWriter{}, 10*time.Millisecond,
		websocket.MessageBinary, []byte("frame"),
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("write error = %v, want deadline exceeded", err)
	}
}

func writeControl(t *testing.T, ctx context.Context, connection *websocket.Conn, control browserstream.Control) {
	t.Helper()
	payload, err := json.Marshal(control)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatal(err)
	}
}

func readControl(t *testing.T, ctx context.Context, connection *websocket.Conn) browserstream.Control {
	t.Helper()
	kind, payload, err := connection.Read(ctx)
	if err != nil || kind != websocket.MessageText {
		t.Fatalf("read control: kind=%v err=%v", kind, err)
	}
	var control browserstream.Control
	if err := json.Unmarshal(payload, &control); err != nil {
		t.Fatal(err)
	}
	return control
}
