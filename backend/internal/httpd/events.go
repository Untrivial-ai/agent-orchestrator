package httpd

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/cdc"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/sse"

	"time"
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

// eventsHeartbeatInterval paces the idle comment frame. Short enough that a
// buffering intermediary forwards a real event promptly, long enough to be
// negligible on a metered connection. A var so tests need not wait it out.
var eventsHeartbeatInterval = 10 * time.Second

// eventsWriteTimeout bounds stalled client writes. A var so tests need not wait it out.
var eventsWriteTimeout = sse.DefaultWriteTimeout

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
	// A cursor ahead of head means the change_log was truncated or replaced. Fall
	// back to head rather than zero: replaying from zero costs the client the whole
	// backlog, and since every connected client is reset at the same moment, they
	// stampede together. Falling back to head loses at most the events in the gap,
	// which clients recover from their next snapshot fetch.
	if after > latestSeq {
		after = latestSeq
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

	w.Header().Set(eventAfterHeader, strconv.FormatInt(after, 10))
	sw, err := sse.Upgrade(w, r, sse.WithWriteTimeout(eventsWriteTimeout))
	if err != nil {
		return
	}

	sentSeq := after
	if err := c.replay(ctx, sw, &sentSeq); err != nil {
		return
	}

	// Keep the pipe moving while nothing is happening.
	//
	// An idle stream that writes nothing is fine on a LAN but not behind a
	// proxy: an intermediary buffers a lone small write and has nothing to push
	// it through, so a single event can sit unseen for minutes while the same
	// stream is instant directly. The bulk replay always arrives because it is
	// large enough to flush on its own.
	//
	// A comment frame is the SSE no-op — clients ignore it and no cursor moves —
	// and it carries any buffered event out with it.
	heartbeat := time.NewTicker(eventsHeartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-heartbeat.C:
			if err := sw.WriteComment(""); err != nil {
				return
			}
		case <-ctx.Done():
			return
		case e := <-live:
			if err := writeSSEEvent(sw, e, &sentSeq); err != nil {
				return
			}
		}
	}
}

func (c *EventsController) replay(ctx context.Context, sw *sse.Writer, sentSeq *int64) error {
	for {
		events, err := c.Source.EventsAfter(ctx, *sentSeq, eventsReplayBatch)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		for _, e := range events {
			if err := writeSSEEvent(sw, e, sentSeq); err != nil {
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

func writeSSEEvent(sw *sse.Writer, e cdc.Event, sentSeq *int64) error {
	if e.Seq <= *sentSeq {
		return nil
	}
	if err := sw.WriteJSON(strconv.FormatInt(e.Seq, 10), sseEventName(e.Type), e); err != nil {
		return err
	}
	*sentSeq = e.Seq
	return nil
}

func sseEventName(t cdc.EventType) string {
	return strings.NewReplacer("\r", "_", "\n", "_").Replace(string(t))
}
