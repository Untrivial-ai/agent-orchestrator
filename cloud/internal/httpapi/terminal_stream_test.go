package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

func TestTerminalStreamsRegisterReplacesPrevious(t *testing.T) {
	registry := newTerminalStreams()
	first := &workerTerminalStream{done: make(chan struct{})}
	second := &workerTerminalStream{done: make(chan struct{})}

	deregisterFirst := registry.registerWorker("term", first)
	registry.registerWorker("term", second)
	select {
	case <-first.done:
	default:
		t.Fatal("replaced stream was not closed")
	}
	// The first stream's deferred deregister must not evict its replacement.
	deregisterFirst()
	if registry.lookupWorker("term") != second {
		t.Fatal("stale deregister evicted the replacement stream")
	}
}

func TestTerminalStreamsOutputSubscription(t *testing.T) {
	registry := newTerminalStreams()
	wake, cancel := registry.subscribeOutput("term")
	registry.notifyOutput("term")
	select {
	case <-wake:
	default:
		t.Fatal("subscriber missed the output notification")
	}
	// Coalescing: two rapid notifications never block the notifier.
	registry.notifyOutput("term")
	registry.notifyOutput("term")
	cancel()
	registry.notifyOutput("term")
	select {
	case <-wake:
		// A signal buffered before cancel is fine.
	default:
	}
}

// pushStubStore serves exactly the store surface pushPendingTerminalInput
// touches; everything else panics via the embedded nil interface.
type pushStubStore struct {
	Store
	mu        sync.Mutex
	pending   []domain.WorkerRequest
	completed []string
	failed    []string
}

func (s *pushStubStore) ClaimTerminalInput(
	_ context.Context,
	_, _, _ string,
	_ int64,
	_ string,
	_ time.Duration,
) (domain.WorkerRequest, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return domain.WorkerRequest{}, false, nil
	}
	request := s.pending[0]
	s.pending = s.pending[1:]
	return request, true, nil
}

func (s *pushStubStore) CompleteWorkerRequest(
	_ context.Context,
	_, _, _, requestID string,
	_ int64,
	_ int,
	_ json.RawMessage,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed = append(s.completed, requestID)
	return nil
}

func (s *pushStubStore) FailWorkerRequest(
	_ context.Context,
	_, _, _, requestID string,
	_ int64,
	_ int,
	_, _ string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed = append(s.failed, requestID)
	return nil
}

func inputRequest(id, terminalID, data string) domain.WorkerRequest {
	payload, _ := json.Marshal(map[string]any{
		"terminalId": terminalID,
		"data":       []byte(data),
	})
	return domain.WorkerRequest{ID: id, Kind: "terminal.input", Payload: payload, Attempt: 1}
}

func TestPushPendingTerminalInputDrainsAndCompletes(t *testing.T) {
	store := &pushStubStore{pending: []domain.WorkerRequest{
		inputRequest("req-1", "term", "a"),
		inputRequest("req-2", "term", "b"),
	}}
	server := &Server{store: store, logger: slog.Default()}
	stream := &workerTerminalStream{
		claims: worker.Claims{OrgID: "org", SessionID: "session", WorkerID: "worker", Epoch: 1},
		send:   make(chan []byte, 4),
		done:   make(chan struct{}),
	}
	server.pushPendingTerminalInput(context.Background(), stream, "term")

	if got := len(stream.send); got != 2 {
		t.Fatalf("pushed %d frames, want 2", got)
	}
	if first := <-stream.send; string(first) != "a" {
		t.Fatalf("first push %q, want %q", first, "a")
	}
	if len(store.completed) != 2 {
		t.Fatalf("completed %v, want both requests", store.completed)
	}
}

func TestPushPendingTerminalInputFailsInvalidPayload(t *testing.T) {
	broken := domain.WorkerRequest{
		ID: "req-bad", Kind: "terminal.input",
		Payload: json.RawMessage(`{"terminalId":"term"}`), Attempt: 1,
	}
	store := &pushStubStore{pending: []domain.WorkerRequest{broken}}
	server := &Server{store: store, logger: slog.Default()}
	stream := &workerTerminalStream{
		claims: worker.Claims{OrgID: "org", SessionID: "session", WorkerID: "worker", Epoch: 1},
		send:   make(chan []byte, 1),
		done:   make(chan struct{}),
	}
	server.pushPendingTerminalInput(context.Background(), stream, "term")
	if len(store.failed) != 1 || store.failed[0] != "req-bad" {
		t.Fatalf("failed %v, want the invalid request", store.failed)
	}
	if len(stream.send) != 0 {
		t.Fatal("invalid payload must not be pushed")
	}
}
