package browserstream

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"net"
	"testing"
	"time"
)

func TestProtocolFrameRoundTrip(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
	want := Frame{StreamID: 7, Sequence: 9, Captured: time.UnixMilli(1234).UTC(), Width: 1280, Height: 720, JPEG: []byte{0xff, 0xd8, 0xff, 0xd9}}
	go func() { _ = writeFrame(server, want) }()
	kind, body, err := readPacket(client)
	if err != nil {
		t.Fatal(err)
	}
	if kind != KindJPEG {
		t.Fatalf("kind = %d, want %d", kind, KindJPEG)
	}
	got, err := decodeFrame(body)
	if err != nil {
		t.Fatal(err)
	}
	if got.StreamID != want.StreamID || got.Sequence != want.Sequence || got.Width != want.Width || got.Height != want.Height || string(got.JPEG) != string(want.JPEG) {
		t.Fatalf("frame = %+v, want %+v", got, want)
	}
}

func TestProtocolRejectsOutOfRangeFrameTimestamps(t *testing.T) {
	if err := writeFrame(io.Discard, Frame{Captured: time.UnixMilli(-1), Width: 1, Height: 1, JPEG: []byte{1}}); err == nil {
		t.Fatal("writeFrame accepted a negative timestamp")
	}
	body := make([]byte, frameHeaderSize)
	binary.BigEndian.PutUint64(body[12:20], math.MaxUint64)
	body[24] = 1
	if _, err := decodeFrame(body); err == nil {
		t.Fatal("decodeFrame accepted a timestamp above MaxInt64")
	}
}

func TestBrokerAuthenticatesStartsAndPublishesLatestFrame(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := New(nil, "secret")
	server, host := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = host.Close() })
	go b.serveConn(ctx, server)
	if err := writeControl(host, Control{Type: "hello", Version: ProtocolVersion, Token: "secret"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for !b.Connected() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !b.Connected() {
		t.Fatal("host did not connect")
	}

	type result struct {
		sub *Subscription
		err error
	}
	resultCh := make(chan result, 1)
	go func() {
		sub, err := b.Subscribe(context.Background(), "session-1")
		resultCh <- result{sub: sub, err: err}
	}()
	kind, body, err := readPacket(host)
	if err != nil || kind != KindControl {
		t.Fatalf("read start: kind=%d err=%v", kind, err)
	}
	start, err := decodeControl(body)
	if err != nil || start.Type != "start" || start.SessionID != "session-1" || start.StreamID == 0 {
		t.Fatalf("start = %+v err=%v", start, err)
	}
	res := <-resultCh
	if res.err != nil {
		t.Fatal(res.err)
	}
	defer func() {
		_ = host.Close()
		res.sub.Close()
	}()

	for seq := uint64(1); seq <= 2; seq++ {
		if err := writeFrame(host, Frame{StreamID: start.StreamID, Sequence: seq, Captured: time.Now(), Width: 10, Height: 10, JPEG: []byte{0xff, 0xd8, byte(seq)}}); err != nil {
			t.Fatal(err)
		}
	}
	// Let the broker consume both pipe writes before observing the one-slot
	// latest-value subscription.
	time.Sleep(10 * time.Millisecond)
	select {
	case event := <-res.sub.Events:
		if event.Frame == nil || event.Frame.Sequence != 2 {
			t.Fatalf("latest frame = %+v, want sequence 2", event.Frame)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for frame")
	}
}

func TestBrokerRejectsBadToken(t *testing.T) {
	b := New(nil, "secret")
	server, host := net.Pipe()
	go b.serveConn(context.Background(), server)
	if err := writeControl(host, Control{Type: "hello", Version: ProtocolVersion, Token: "wrong"}); err != nil {
		t.Fatal(err)
	}
	_ = host.SetReadDeadline(time.Now().Add(time.Second))
	var one [1]byte
	if _, err := host.Read(one[:]); err == nil {
		t.Fatal("bad-token connection stayed open")
	}
	if b.Connected() {
		t.Fatal("bad-token host became active")
	}
}
