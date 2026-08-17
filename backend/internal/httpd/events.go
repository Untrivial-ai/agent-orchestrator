package httpd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/cdc"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

const (
	eventsReplayBatch = 512
	eventsLiveBuffer  = 1024
	eventAfterHeader  = "X-AO-Event-After"
)

type cdcSubscriber interface {
	Subscribe(func(cdc.Event)) (unsubscribe func())
}

// EventsController owns the client-facing CDC stream. Durable replay comes from
// change_log through Source; Broadcaster remains a live-only pub/sub seam.
type EventsController struct {
	Source cdc.Source
	Live   cdcSubscriber
}

// Register mounts the CDC SSE stream route.
func (c *EventsController) Register(r chi.Router) {
	r.Get("/events", c.stream)
}

func (c *EventsController) stream(w http.ResponseWriter, r *http.Request) {
	if c.Source == nil || c.Live == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/events")
		return
	}

	after, err := parseEventsAfter(r)
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_AFTER",
			"after must be a non-negative integer", nil)
		return
	}
	latestSeq, err := c.Source.LatestSeq(r.Context())
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal", "EVENT_CURSOR_FAILED",
			"Could not inspect the event cursor", nil)
		return
	}
	if after > latestSeq {
		after = 0
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal", "SSE_UNSUPPORTED",
			"Streaming is not supported by this server", nil)
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	live := make(chan cdc.Event, eventsLiveBuffer)
	unsubscribe := c.Live.Subscribe(func(e cdc.Event) {
		select {
		case live <- e:
		default:
			// Never block the broadcaster. Closing the stream is safer than
			// silently dropping a live event; the client replays on reconnect.
			cancel()
		}
	})
	defer unsubscribe()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	h.Set(eventAfterHeader, strconv.FormatInt(after, 10))
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	sentSeq := after
	if err := c.replay(ctx, w, flusher, &sentSeq); err != nil {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case e := <-live:
			if err := writeSSEEvent(w, flusher, e, &sentSeq); err != nil {
				return
			}
		}
	}
}

func (c *EventsController) replay(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, sentSeq *int64) error {
	for {
		events, err := c.Source.EventsAfter(ctx, *sentSeq, eventsReplayBatch)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		for _, e := range events {
			if err := writeSSEEvent(w, flusher, e, sentSeq); err != nil {
				return err
			}
		}
		if len(events) < eventsReplayBatch {
			return nil
		}
	}
}

func parseEventsAfter(r *http.Request) (int64, error) {
	raw := r.URL.Query().Get("after")
	if raw == "" {
		raw = r.Header.Get("Last-Event-ID")
	}
	if raw == "" {
		return 0, nil
	}
	seq, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || seq < 0 {
		return 0, fmt.Errorf("invalid after: %q", raw)
	}
	return seq, nil
}

func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, e cdc.Event, sentSeq *int64) error {
	if e.Seq <= *sentSeq {
		return nil
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.Seq, sseEventName(e.Type), data); err != nil {
		return err
	}
	*sentSeq = e.Seq
	flusher.Flush()
	return nil
}

func sseEventName(t cdc.EventType) string {
	return strings.NewReplacer("\r", "_", "\n", "_").Replace(string(t))
}
