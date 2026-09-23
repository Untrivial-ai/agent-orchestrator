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

func TestRelayOutputDeliversLiveFrame(t *testing.T) {
	registry := newTerminalStreams()
	output, unsubscribe := registry.subscribeRelayOutput("term")
	defer unsubscribe()

	if dropped := registry.relayOutput("term", terminalRelayOutput{
		sequence: 7,
		data:     []byte("hello"),
	}); dropped != 0 {
		t.Fatalf("dropped=%d, want 0", dropped)
	}
	select {
	case got := <-output:
		if got.sequence != 7 || string(got.data) != "hello" {
			t.Fatalf("got sequence=%d data=%q", got.sequence, got.data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("relay output was not delivered")
	}
}

func TestRelayOutputReportsSaturatedClient(t *testing.T) {
	registry := newTerminalStreams()
	output, unsubscribe := registry.subscribeRelayOutput("term")
	defer unsubscribe()
	for range cap(output) {
		if dropped := registry.relayOutput("term", terminalRelayOutput{sequence: 1, data: []byte("x")}); dropped != 0 {
			t.Fatalf("unexpected drop while filling buffer: %d", dropped)
		}
	}
	if dropped := registry.relayOutput("term", terminalRelayOutput{sequence: 2, data: []byte("x")}); dropped != 1 {
		t.Fatalf("dropped=%d, want 1", dropped)
	}
}
