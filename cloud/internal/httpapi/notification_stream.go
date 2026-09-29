package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

const maxJavaScriptSequence int64 = 1<<53 - 1

var (
	notificationStreamPollInterval      = 5 * time.Second
	notificationStreamKeepaliveInterval = 15 * time.Second
)

type notificationWaiters struct {
	mu      sync.Mutex
	waiters map[string]map[chan struct{}]struct{}
}

func newNotificationWaiters() *notificationWaiters {
	return &notificationWaiters{waiters: make(map[string]map[chan struct{}]struct{})}
}

func (w *notificationWaiters) subscribe(orgID string) (chan struct{}, func()) {
	wake := make(chan struct{}, 1)
	w.mu.Lock()
	set := w.waiters[orgID]
	if set == nil {
		set = make(map[chan struct{}]struct{})
		w.waiters[orgID] = set
	}
	set[wake] = struct{}{}
	w.mu.Unlock()
	return wake, func() {
		w.mu.Lock()
		if set := w.waiters[orgID]; set != nil {
			delete(set, wake)
			if len(set) == 0 {
				delete(w.waiters, orgID)
			}
		}
		w.mu.Unlock()
	}
}

func (w *notificationWaiters) notify(orgID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for wake := range w.waiters[orgID] {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

func (s *Server) HandleNotificationEventNotify(orgID string) {
	if s.notificationWaiters != nil {
		s.notificationWaiters.notify(orgID)
	}
}

func (s *Server) notificationEvents(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream") {
		s.streamNotificationEvents(w, r)
		return
	}
	s.listNotificationEvents(w, r)
}

func parseNotificationSequence(w http.ResponseWriter, r *http.Request) (int64, bool) {
	value := strings.TrimSpace(r.URL.Query().Get("after"))
	if value == "" {
		value = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	}
	if value == "" {
		return 0, true
	}
	sequence, err := strconv.ParseInt(value, 10, 64)
	if err != nil || sequence < 0 || sequence > maxJavaScriptSequence {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "after must be a non-negative JavaScript-safe integer")
		return 0, false
	}
	return sequence, true
}

func (s *Server) streamNotificationEvents(w http.ResponseWriter, r *http.Request) {
	after, ok := parseNotificationSequence(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, r, http.StatusInternalServerError, "stream_unsupported", "Streaming is unavailable.")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	orgID := chi.URLParam(r, "orgId")
	wake, unsubscribe := s.notificationWaiters.subscribe(orgID)
	defer unsubscribe()
	poll := time.NewTicker(notificationStreamPollInterval)
	defer poll.Stop()
	keepalive := time.NewTicker(notificationStreamKeepaliveInterval)
	defer keepalive.Stop()

	for {
		for {
			events, hasMore, err := s.store.ListNotificationEvents(r.Context(), principalFrom(r), orgID, after, 100)
			if err != nil {
				s.logger.Error("stream cloud notifications", "error", err, "request_id", requestID(r))
				return
			}
			for _, event := range events {
				payload, err := json.Marshal(event)
				if err != nil {
					return
				}
				if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Kind, payload); err != nil {
					return
				}
				after = event.Sequence
			}
			if len(events) > 0 {
				flusher.Flush()
			}
			if !hasMore {
				break
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-s.drain:
			return
		case <-wake:
		case <-poll.C:
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
