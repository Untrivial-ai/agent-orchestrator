package cifeedback

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

type fakeStore struct {
	item      domain.CIFeedback
	found     bool
	queued    []byte
	completed bool
	retried   bool
	ensureErr error
}

func (f *fakeStore) ClaimCIFeedback(context.Context, string, time.Duration) (domain.CIFeedback, bool, error) {
	return f.item, f.found, nil
}
func (f *fakeStore) EnsureWorkerAgentTerminal(context.Context, string, string, string, int64, time.Duration) (domain.TerminalSession, error) {
	return domain.TerminalSession{ID: "term", OrgID: f.item.OrgID, SessionID: f.item.SessionID}, f.ensureErr
}
func (f *fakeStore) QueueTerminalInput(_ context.Context, _ domain.TerminalSession, _ string, data []byte) error {
	f.queued = data
	return nil
}
func (f *fakeStore) CompleteCIFeedback(context.Context, string, string) error {
	f.completed = true
	return nil
}
func (f *fakeStore) RetryCIFeedback(context.Context, string, string, string, time.Time) error {
	f.retried = true
	return nil
}

func TestDispatcherQueuesOnePrompt(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"message": "CI failure detected; fix it."})
	store := &fakeStore{found: true, item: domain.CIFeedback{ID: "f1", ApplicationKey: "key", OrgID: "org", SessionID: "session", WorkerID: "worker", WorkerEpoch: 2, Payload: payload}}
	dispatcher := New(store, Config{Owner: "test"})
	if err := dispatcher.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if string(store.queued) != "CI failure detected; fix it.\n" {
		t.Fatalf("queued %q", store.queued)
	}
	if !store.completed {
		t.Fatal("feedback was not completed")
	}
}

func TestDispatcherRetriesUnavailableWorker(t *testing.T) {
	store := &fakeStore{found: true, ensureErr: errors.New("offline"), item: domain.CIFeedback{ID: "f1"}}
	dispatcher := New(store, Config{Owner: "test"})
	if err := dispatcher.RunOnce(context.Background()); err == nil {
		t.Fatal("expected offline error")
	}
	if !store.retried {
		t.Fatal("feedback was not left retryable")
	}
}
