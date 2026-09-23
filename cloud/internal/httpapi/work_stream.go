package httpapi

import (
	"net/http"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

// workWaitTimeout bounds a single WaitForWork long-poll. A NOTIFY delivers work
// within milliseconds; this is only the fallback re-check cadence for the rare
// missed-notification or gated-turn case, keeping the loop correct without the
// old ~100ms busy-poll. It must stay well under the worker's HTTP client
// timeout (30s) and the control plane's per-request timeout.
const workWaitTimeout = 5 * time.Second

// workWaiters wakes workers that are blocked in WaitForWork the instant a turn
// or transport request is enqueued for their session. It mirrors the terminal
// output wake registry: notifications are accelerants, the durable claim query
// remains the source of truth.
type workWaiters struct {
	mu      sync.Mutex
	waiters map[string]map[chan struct{}]struct{}
}

func newWorkWaiters() *workWaiters {
	return &workWaiters{waiters: make(map[string]map[chan struct{}]struct{})}
}

// subscribe returns a coalescing wake channel for the session and a deregister
// func. The channel is buffered so a notify never blocks and never misses a
// wake that arrives between drains.
func (w *workWaiters) subscribe(sessionID string) (chan struct{}, func()) {
	wake := make(chan struct{}, 1)
	w.mu.Lock()
	set := w.waiters[sessionID]
	if set == nil {
		set = make(map[chan struct{}]struct{})
		w.waiters[sessionID] = set
	}
	set[wake] = struct{}{}
	w.mu.Unlock()
	return wake, func() {
		w.mu.Lock()
		if set := w.waiters[sessionID]; set != nil {
			delete(set, wake)
			if len(set) == 0 {
				delete(w.waiters, sessionID)
			}
		}
		w.mu.Unlock()
	}
}

func (w *workWaiters) notify(sessionID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for wake := range w.waiters[sessionID] {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

// HandleWorkerWorkNotify is the Postgres NOTIFY callback for ao_worker_work; the
// payload is the session id whose turn/transport queue just gained a row. Every
// replica runs its own listener, so whichever one holds the waiting worker's
// long-poll wakes it; the others no-op.
func (s *Server) HandleWorkerWorkNotify(sessionID string) {
	if s.workWaiters == nil {
		return
	}
	s.workWaiters.notify(sessionID)
}

// workerWaitForWork blocks until the session has new work to claim (a turn or a
// transport request), or a short fallback timeout elapses. It replaces the
// worker's ~100ms busy-poll: the worker holds one request open and the control
// plane wakes it on enqueue. It never returns the work itself — the worker
// re-runs its atomic claim RPCs when this returns.
func (s *Server) workerWaitForWork(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:connect") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:connect scope is required.")
		return
	}
	if s.workWaiters == nil {
		writeJSON(w, http.StatusOK, struct{}{})
		return
	}
	wake, cancel := s.workWaiters.subscribe(claims.SessionID)
	defer cancel()
	timer := time.NewTimer(workWaitTimeout)
	defer timer.Stop()
	select {
	case <-r.Context().Done():
	case <-wake:
	case <-timer.C:
	}
	writeJSON(w, http.StatusOK, struct{}{})
}
