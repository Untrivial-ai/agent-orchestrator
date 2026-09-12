package httpd

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/cdc"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
)

type fakeEventSource struct {
	live                    *fakeEventSubscriber
	sawSubscriptionOnReplay bool
}

func (s *fakeEventSource) EventsAfter(context.Context, int64, int) ([]cdc.Event, error) {
	s.sawSubscriptionOnReplay = s.live.hasSubscriber()
	s.live.publish(testCDCEvent(2))
	return []cdc.Event{testCDCEvent(1)}, nil
}

func (*fakeEventSource) LatestSeq(context.Context) (int64, error) {
	return 0, nil
}

type fakeEventSubscriber struct {
	mu sync.Mutex
	fn func(cdc.Event)
}

func (s *fakeEventSubscriber) Subscribe(fn func(cdc.Event)) func() {
	s.mu.Lock()
	s.fn = fn
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.fn = nil
		s.mu.Unlock()
	}
}

func (s *fakeEventSubscriber) hasSubscriber() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fn != nil
}

func (s *fakeEventSubscriber) publish(e cdc.Event) {
	s.mu.Lock()
	fn := s.fn
	s.mu.Unlock()
	if fn != nil {
		fn(e)
	}
}

func TestEventsStreamSubscribesBeforeReplayAndDrainsBufferedLive(t *testing.T) {
	live := &fakeEventSubscriber{}
	src := &fakeEventSource{live: live}
	router := NewRouterWithControl(config.Config{}, discardLogger(), nil, APIDeps{
		CDC:    src,
		Events: live,
	}, ControlDeps{})
	ts := httptest.NewServer(router)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/events?after=0", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/events: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", got)
	}

	ids := readSSEIDs(t, resp.Body, 2)
	if got, want := strings.Join(ids, ","), "1,2"; got != want {
		t.Fatalf("ids = %s, want %s", got, want)
	}
	if !src.sawSubscriptionOnReplay {
		t.Fatal("replay started before live subscription was installed")
	}
}

type reviewRunEventSource struct {
	live *fakeEventSubscriber
}

func (s *reviewRunEventSource) EventsAfter(context.Context, int64, int) ([]cdc.Event, error) {
	s.live.publish(testCDCEventWithType(2, cdc.EventReviewRunUpdated))
	return []cdc.Event{testCDCEventWithType(1, cdc.EventReviewRunCreated)}, nil
}

func (*reviewRunEventSource) LatestSeq(context.Context) (int64, error) { return 0, nil }

func TestEventsStreamForwardsReviewRunCDC(t *testing.T) {
	live := &fakeEventSubscriber{}
	src := &reviewRunEventSource{live: live}
	router := NewRouterWithControl(config.Config{}, discardLogger(), nil, APIDeps{
		CDC:    src,
		Events: live,
	}, ControlDeps{})
	ts := httptest.NewServer(router)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/events?after=0", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/events: %v", err)
	}
	defer resp.Body.Close()

	events := readSSEEventTypes(t, resp.Body, 2)
	if got, want := strings.Join(events, ","), "review_run_created,review_run_updated"; got != want {
		t.Fatalf("event types = %s, want %s", got, want)
	}
}

func TestEventsStreamRejectsInvalidAfter(t *testing.T) {
	router := NewRouterWithControl(config.Config{}, discardLogger(), nil, APIDeps{
		CDC:    &fakeEventSource{live: &fakeEventSubscriber{}},
		Events: &fakeEventSubscriber{},
	}, ControlDeps{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events?after=nope", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "INVALID_AFTER") {
		t.Fatalf("body = %s, want INVALID_AFTER", rec.Body.String())
	}
}

type resetEventSource struct {
	after int64
}

func (s *resetEventSource) EventsAfter(_ context.Context, after int64, _ int) ([]cdc.Event, error) {
	s.after = after
	return []cdc.Event{testCDCEvent(1)}, nil
}

func (*resetEventSource) LatestSeq(context.Context) (int64, error) { return 1, nil }

// A cursor ahead of head means the change_log was truncated or replaced. Such a
// client is sent to head, not to zero: replaying from zero costs it the entire
// backlog, and because every connected client is reset at the same moment, they
// stampede together. The gap is recovered from the next snapshot fetch instead.
func TestEventsStreamClampsCursorAheadOfCurrentDatabaseToHead(t *testing.T) {
	live := &fakeEventSubscriber{}
	src := &resetEventSource{}
	router := NewRouterWithControl(config.Config{}, discardLogger(), nil, APIDeps{
		CDC:    src,
		Events: live,
	}, ControlDeps{})
	ts := httptest.NewServer(router)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/events?after=100", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/events: %v", err)
	}
	defer resp.Body.Close()

	// resetEventSource reports a head of 1, so a cursor of 100 lands on 1.
	if got := resp.Header.Get("X-AO-Event-After"); got != "1" {
		t.Fatalf("X-AO-Event-After = %q, want 1 (head, not a replay from zero)", got)
	}

	// The replayed event (seq 1) is at the cursor, so it is not re-sent. Only a
	// genuinely newer event reaches the client — proving no backlog was replayed.
	live.publish(testCDCEvent(2))
	if ids := readSSEIDs(t, resp.Body, 1); ids[0] != "2" {
		t.Fatalf("id = %q, want 2 (history before head must not be replayed)", ids[0])
	}
	if src.after != 1 {
		t.Fatalf("EventsAfter called with %d, want head cursor 1", src.after)
	}
}

func TestWriteSSEEventSanitizesEventNameNewlines(t *testing.T) {
	rec := httptest.NewRecorder()
	sentSeq := int64(0)
	e := testCDCEvent(1)
	e.Type = cdc.EventType("session_updated\nid: 999\rdata: injected")

	if err := writeSSEEvent(rec, rec, e, &sentSeq); err != nil {
		t.Fatalf("writeSSEEvent: %v", err)
	}

	body := rec.Body.String()
	if strings.Contains(body, "\nid: 999") || strings.Contains(body, "\rdata: injected") {
		t.Fatalf("body contains injected SSE field: %q", body)
	}
	if !strings.Contains(body, "event: session_updated_id: 999_data: injected\n") {
		t.Fatalf("body = %q, want sanitized event name", body)
	}
}

func readSSEIDs(t *testing.T, r io.Reader, want int) []string {
	t.Helper()
	ids := make([]string, 0, want)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "id: ") {
			ids = append(ids, strings.TrimPrefix(line, "id: "))
			if len(ids) == want {
				return ids
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read stream: %v", err)
	}
	t.Fatalf("stream ended after ids %v, want %d ids", ids, want)
	return nil
}

func readSSEEventTypes(t *testing.T, r io.Reader, want int) []string {
	t.Helper()
	types := make([]string, 0, want)
	scanner := bufio.NewScanner(r)
	var current string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			current = strings.TrimPrefix(line, "event: ")
		case line == "":
			if current != "" {
				types = append(types, current)
				current = ""
				if len(types) == want {
					return types
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read stream: %v", err)
	}
	t.Fatalf("stream ended after event types %v, want %d event types", types, want)
	return nil
}

// dedupeEventSource publishes a duplicate of its replay event plus a new event
// into the live channel so the dedupe path in writeSSEEvent can be exercised.
type dedupeEventSource struct {
	live *fakeEventSubscriber
}

func (s *dedupeEventSource) EventsAfter(_ context.Context, _ int64, _ int) ([]cdc.Event, error) {
	// Both published before replay returns: seq=5 duplicates the replay event;
	// seq=6 is genuinely new. After replay sentSeq=5, so seq=5 must be dropped
	// and seq=6 sent.
	s.live.publish(testCDCEvent(5))
	s.live.publish(testCDCEvent(6))
	return []cdc.Event{testCDCEvent(5)}, nil
}

func (*dedupeEventSource) LatestSeq(context.Context) (int64, error) { return 0, nil }

// TestEventsStreamDeduplicatesLiveEventOverlappingReplay verifies that a live
// event whose seq falls within the already-replayed range is silently dropped,
// so the client sees each seq exactly once.
func TestEventsStreamDeduplicatesLiveEventOverlappingReplay(t *testing.T) {
	live := &fakeEventSubscriber{}
	src := &dedupeEventSource{live: live}
	router := NewRouterWithControl(config.Config{}, discardLogger(), nil, APIDeps{
		CDC:    src,
		Events: live,
	}, ControlDeps{})
	ts := httptest.NewServer(router)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/events?after=0", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/events: %v", err)
	}
	defer resp.Body.Close()

	// Replay emits seq=5; live buffer holds seq=5 (dup) then seq=6 (new).
	// writeSSEEvent must drop seq=5 from the live drain (5 <= sentSeq=5).
	// The client must see exactly [5, 6], not [5, 5, 6].
	ids := readSSEIDs(t, resp.Body, 2)
	if got, want := strings.Join(ids, ","), "5,6"; got != want {
		t.Fatalf("ids = %s, want %s (duplicate seq was not deduped)", got, want)
	}
}

// lastEventIDSource returns a single event whose seq is calledAfter+1, letting
// the test prove EventsAfter received the cursor from Last-Event-ID by checking
// the event seq the client receives.
type lastEventIDSource struct {
	live *fakeEventSubscriber
}

func (s *lastEventIDSource) EventsAfter(_ context.Context, after int64, _ int) ([]cdc.Event, error) {
	return []cdc.Event{testCDCEvent(after + 1)}, nil
}

func (*lastEventIDSource) LatestSeq(context.Context) (int64, error) { return 8, nil }

// TestEventsStreamParsesLastEventIDHeader verifies that the Last-Event-ID
// request header is used as the replay cursor when the after query param is
// absent. The source returns after+1, so receiving seq=8 proves the cursor
// was parsed as 7.
func TestEventsStreamParsesLastEventIDHeader(t *testing.T) {
	live := &fakeEventSubscriber{}
	src := &lastEventIDSource{live: live}
	router := NewRouterWithControl(config.Config{}, discardLogger(), nil, APIDeps{
		CDC:    src,
		Events: live,
	}, ControlDeps{})
	ts := httptest.NewServer(router)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Last-Event-ID", "7")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/events: %v", err)
	}
	defer resp.Body.Close()

	// EventsAfter(after=7) returns seq=8. Receiving seq=8 proves the header
	// was parsed correctly. If the header were ignored (after=0 default),
	// EventsAfter would return seq=1 and this assertion would fail.
	ids := readSSEIDs(t, resp.Body, 1)
	if got, want := ids[0], "8"; got != want {
		t.Fatalf("id = %q, want %q (Last-Event-ID header was not used as cursor)", got, want)
	}
}

func testCDCEvent(seq int64) cdc.Event {
	return testCDCEventWithType(seq, cdc.EventSessionUpdated)
}

func testCDCEventWithType(seq int64, typ cdc.EventType) cdc.Event {
	return cdc.Event{
		Seq:       seq,
		ProjectID: "proj_1",
		SessionID: "sess_1",
		Type:      typ,
		Payload:   json.RawMessage(`{"status":"running"}`),
		CreatedAt: time.Unix(seq, 0).UTC(),
	}
}

// An idle stream sent nothing at all, which is fine on a LAN and broken behind
// a proxy: Cloudflare buffers a lone small write and has nothing to push it
// through, so a single agent reply sat unseen for over a minute while the same
// stream worked instantly over the LAN. The bulk replay always arrived because
// it is large enough to flush on its own.
//
// A periodic comment frame keeps the pipe moving and carries any buffered event
// out with it. Comments are the SSE no-op: clients ignore them, and the cursor
// is untouched.
func TestEventsStreamHeartbeatsWhileIdle(t *testing.T) {
	restore := eventsHeartbeatInterval
	eventsHeartbeatInterval = 50 * time.Millisecond
	defer func() { eventsHeartbeatInterval = restore }()

	live := &fakeEventSubscriber{}
	src := &fakeEventSource{live: live}
	router := NewRouterWithControl(config.Config{}, discardLogger(), nil, APIDeps{
		CDC:    src,
		Events: live,
	}, ControlDeps{})
	ts := httptest.NewServer(router)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Past head, so the server clamps to head and replays nothing: the stream is
	// genuinely idle and anything that arrives can only be a heartbeat.
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/events?after=999999", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/events: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	deadline := time.Now().Add(4 * time.Second)
	seen := ""
	buf := make([]byte, 256)
	for time.Now().Before(deadline) {
		n, err := resp.Body.Read(buf)
		if err != nil {
			break
		}
		seen += string(buf[:n])
		for _, line := range strings.Split(seen, "\n") {
			// An SSE comment: a frame beginning with a colon.
			if strings.HasPrefix(line, ":") {
				return
			}
		}
	}
	t.Fatalf("idle stream sent no comment frame in 4s (got %q); a buffering proxy has nothing to flush an event through", seen)
}
