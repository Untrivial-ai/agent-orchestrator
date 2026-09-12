package httpapi

import (
	"testing"
	"time"
)

func TestPushInputFastPathDeliversInMemory(t *testing.T) {
	registry := newTerminalStreams()
	stream := &workerTerminalStream{
		send: make(chan []byte, 4),
		done: make(chan struct{}),
	}
	registry.registerWorker("term", stream)

	if !registry.pushInput("term", []byte("ab")) {
		t.Fatal("expected same-replica push to succeed")
	}
	select {
	case got := <-stream.send:
		if string(got) != "ab" {
			t.Fatalf("worker got %q, want %q", got, "ab")
		}
	default:
		t.Fatal("keystroke was not delivered to the worker stream")
	}
}

func TestPushInputFallsBackWhenNoLocalStream(t *testing.T) {
	registry := newTerminalStreams()
	if registry.pushInput("absent", []byte("x")) {
		t.Fatal("push must fail (fall back to durable queue) with no local stream")
	}
}

func TestPushInputFallsBackWhenBufferFull(t *testing.T) {
	registry := newTerminalStreams()
	stream := &workerTerminalStream{
		send: make(chan []byte, 1),
		done: make(chan struct{}),
	}
	registry.registerWorker("term", stream)

	if !registry.pushInput("term", []byte("1")) {
		t.Fatal("first push should fit the buffer")
	}
	// Buffer (size 1) is now full; the next push must fall back, not block.
	done := make(chan bool, 1)
	go func() { done <- registry.pushInput("term", []byte("2")) }()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("push into a full buffer must return false, not accept")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pushInput blocked on a full buffer instead of falling back")
	}
}

func TestPushInputFailsAfterStreamRetired(t *testing.T) {
	registry := newTerminalStreams()
	stream := &workerTerminalStream{
		send: make(chan []byte), // unbuffered
		done: make(chan struct{}),
	}
	registry.registerWorker("term", stream)
	close(stream.done)
	if registry.pushInput("term", []byte("x")) {
		t.Fatal("push must fail once the stream is retired (done closed)")
	}
}

func TestTerminalRoutingKeyStableAndBounded(t *testing.T) {
	a := terminalRoutingKey("session-abc")
	b := terminalRoutingKey("session-abc")
	if a != b {
		t.Fatalf("routing key not stable: %d vs %d", a, b)
	}
	if a < 0 || a >= replicaShardSpace {
		t.Fatalf("routing key %d out of range [0,%d)", a, replicaShardSpace)
	}
	// The client socket and worker stream both hash the same session id, so
	// they must land on the same shard — that co-location is the whole point.
	if terminalRoutingKey("session-abc") != terminalRoutingKey("session-abc") {
		t.Fatal("same session must map to the same shard")
	}
	if terminalRoutingKey("session-1") == terminalRoutingKey("session-1-extra") &&
		terminalRoutingKey("session-2") == terminalRoutingKey("session-2-extra") {
		t.Fatal("routing key appears to ignore its input")
	}
}
