package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWorkWaitersNotifyWakesOnlyTheSession(t *testing.T) {
	ww := newWorkWaiters()
	wake, cancel := ww.subscribe("s1")
	defer cancel()

	// A notify for a different session must not wake this subscriber.
	ww.notify("s2")
	select {
	case <-wake:
		t.Fatal("woke on an unrelated session's notify")
	default:
	}

	ww.notify("s1")
	select {
	case <-wake:
	default:
		t.Fatal("did not wake on this session's notify")
	}

	// After deregister, a notify must not panic or deliver.
	cancel()
	ww.notify("s1")
}

func waitWorkServer() *Server {
	return &Server{
		workWaiters: newWorkWaiters(),
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestWorkerWaitForWorkReturnsPromptlyOnNotify(t *testing.T) {
	srv := waitWorkServer()
	w := httptest.NewRecorder()
	req := workerRequest(t, http.MethodGet, "/worker/work/wait", "", "worker:connect")

	done := make(chan struct{})
	start := time.Now()
	go func() { srv.workerWaitForWork(w, req); close(done) }()

	// Wait until the handler has subscribed, then notify — robust against the
	// goroutine not yet having run.
	for i := 0; i < 200; i++ {
		srv.workWaiters.mu.Lock()
		n := len(srv.workWaiters.waiters[testOrchestratorID])
		srv.workWaiters.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	srv.workWaiters.notify(testOrchestratorID)

	select {
	case <-done:
		if elapsed := time.Since(start); elapsed >= workWaitTimeout {
			t.Fatalf("returned after %v; the notify should have woken it well before the %v fallback", elapsed, workWaitTimeout)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("workerWaitForWork did not return after a notify")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestWorkerWaitForWorkRequiresConnectScope(t *testing.T) {
	srv := waitWorkServer()
	w := httptest.NewRecorder()
	srv.workerWaitForWork(w, workerRequest(t, http.MethodGet, "/worker/work/wait", ""))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}
