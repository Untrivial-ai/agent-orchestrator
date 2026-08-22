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
	return []cdc.Event{testCDCEvent(after + 1)}, nil
}

func (*resetEventSource) LatestSeq(context.Context) (int64, error) { return 100, nil }

func TestEventsStreamSnapsCursorAheadOfCurrentDatabase(t *testing.T) {
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

	// A cursor ahead of the head must snap forward to the head (never reset to
	// a full-history replay) so a stale client cannot trigger a whole-log
	// replay (#3963). The head here is 100, so replay resumes at 100.
	if got := resp.Header.Get("X-AO-Event-After"); got != "100" {
		t.Fatalf("X-AO-Event-After = %q, want 100", got)
	}
	if ids := readSSEIDs(t, resp.Body, 1); ids[0] != "101" {
		t.Fatalf("id = %q, want 101", ids[0])
	}
	if src.after != 100 {
		t.Fatalf("EventsAfter called with %d, want snapped cursor 100", src.after)
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

type sseFrame struct {
	event string
	id    string
	data  string
}

// readSSEFrames reads want complete SSE frames (terminated by a blank line).
func readSSEFrames(t *testing.T, r io.Reader, want int) []sseFrame {
	t.Helper()
	frames := make([]sseFrame, 0, want)
	cur := sseFrame{}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			cur.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "id: "):
			cur.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "data: "):
			cur.data = strings.TrimPrefix(line, "data: ")
		case line == "":
			frames = append(frames, cur)
			cur = sseFrame{}
			if len(frames) == want {
				return frames
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read stream: %v", err)
	}
	t.Fatalf("stream ended after %d frames, want %d", len(frames), want)
	return nil
}

// TestEventsStreamEmitsCursorResetOnSnap verifies the #3963 review fix: when
// the server snaps a deep-gap cursor, it emits an explicit events_cursor_reset
// control event before any replay/live delivery so clients can refetch the
// projections whose targeted invalidations rode on skipped payloads.
func TestEventsStreamEmitsCursorResetOnSnap(t *testing.T) {
	live := &fakeEventSubscriber{}
	src := &snapCursorSource{live: live, head: 20_000}

	resp := getEventStream(t, src, live, func(req *http.Request) {
		req.URL.RawQuery = "after=5"
	})

	frames := readSSEFrames(t, resp.Body, 2)
	if frames[0].event != eventsCursorResetEvent {
		t.Fatalf("first frame event = %q, want %q", frames[0].event, eventsCursorResetEvent)
	}
	if frames[0].id != "20000" {
		t.Fatalf("reset frame id = %q, want 20000 (effective cursor)", frames[0].id)
	}
	if !strings.Contains(frames[0].data, `"requested":5`) || !strings.Contains(frames[0].data, `"after":20000`) {
		t.Fatalf("reset frame data = %q, want requested=5 and after=20000", frames[0].data)
	}
	if frames[1].id != "20001" {
		t.Fatalf("post-reset id = %q, want 20001", frames[1].id)
	}
}

// TestEventsStreamSmallGapHasNoReset verifies that genuine small-gap resumes
// do not emit the reset control event — payloads are replayed in full.
func TestEventsStreamSmallGapHasNoReset(t *testing.T) {
	live := &fakeEventSubscriber{}
	src := &snapCursorSource{live: live, head: 20}

	resp := getEventStream(t, src, live, func(req *http.Request) {
		req.URL.RawQuery = "after=10"
	})

	frames := readSSEFrames(t, resp.Body, 1)
	if frames[0].event == eventsCursorResetEvent {
		t.Fatal("small-gap resume emitted a cursor reset; only snapped cursors must")
	}
	if frames[0].id != "11" {
		t.Fatalf("id = %q, want 11", frames[0].id)
	}
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

// LatestSeq reports a realistic head above the served events so the small-gap
// resume path under test is not short-circuited by the stale-cursor snap.
func (*lastEventIDSource) LatestSeq(context.Context) (int64, error) { return 100, nil }

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
	return cdc.Event{
		Seq:       seq,
		ProjectID: "proj_1",
		SessionID: "sess_1",
		Type:      cdc.EventSessionUpdated,
		Payload:   json.RawMessage(`{"status":"running"}`),
		CreatedAt: time.Unix(seq, 0).UTC(),
	}
}

// snapCursorSource records the replay cursor it was called with and serves a
// single event at that cursor+1, so a test asserts the effective cursor by the
// seq the client receives. head simulates the durable log's MAX(seq).
type snapCursorSource struct {
	live      *fakeEventSubscriber
	head      int64
	gotCursor int64
}

func (s *snapCursorSource) EventsAfter(_ context.Context, after int64, _ int) ([]cdc.Event, error) {
	s.gotCursor = after
	return []cdc.Event{testCDCEvent(after + 1)}, nil
}

func (s *snapCursorSource) LatestSeq(context.Context) (int64, error) { return s.head, nil }

// getEventStream opens /api/v1/events against a fresh test server and returns
// the response with its body still open (closed via t.Cleanup). mutate may set
// headers or assert on the request before it is sent.
func getEventStream(t *testing.T, src cdc.Source, live *fakeEventSubscriber, mutate func(*http.Request)) *http.Response {
	t.Helper()
	router := NewRouterWithControl(config.Config{}, discardLogger(), nil, APIDeps{
		CDC:    src,
		Events: live,
	}, ControlDeps{})
	ts := httptest.NewServer(router)
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if mutate != nil {
		mutate(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/events: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// TestEventsStreamSnapsUnpositionedCursorToHead is the #3963 regression guard:
// a fresh connect with no cursor at all must start at the head, never replay
// the whole change_log. The renderer refetches state in onopen anyway.
func TestEventsStreamSnapsUnpositionedCursorToHead(t *testing.T) {
	live := &fakeEventSubscriber{}
	src := &snapCursorSource{live: live, head: 30_000}

	resp := getEventStream(t, src, live, nil)

	frames := readSSEFrames(t, resp.Body, 2)
	if frames[0].event != eventsCursorResetEvent {
		t.Fatalf("first frame event = %q, want %q", frames[0].event, eventsCursorResetEvent)
	}
	if frames[0].id != "30000" {
		t.Fatalf("reset frame id = %q, want 30000", frames[0].id)
	}
	if got, want := resp.Header.Get(eventAfterHeader), "30000"; got != want {
		t.Fatalf("X-AO-Event-After = %q, want %q", got, want)
	}
	if frames[1].id != "30001" {
		t.Fatalf("post-reset id = %q, want 30001 (cursor-less connect replayed history instead of snapping to head)", frames[1].id)
	}
}

// TestEventsStreamSnapsFutureCursorToHead verifies that a cursor beyond the
// head snaps to the head rather than resetting to a full-history replay.
func TestEventsStreamSnapsFutureCursorToHead(t *testing.T) {
	live := &fakeEventSubscriber{}
	src := &snapCursorSource{live: live, head: 50}

	resp := getEventStream(t, src, live, func(req *http.Request) {
		req.URL.RawQuery = "after=999"
	})

	if got, want := resp.Header.Get(eventAfterHeader), "50"; got != want {
		t.Fatalf("X-AO-Event-After = %q, want %q", got, want)
	}
	frames := readSSEFrames(t, resp.Body, 2)
	if frames[0].event != eventsCursorResetEvent || frames[0].id != "50" {
		t.Fatalf("first frame = %q/%q, want reset at 50", frames[0].event, frames[0].id)
	}
	if frames[1].id != "51" {
		t.Fatalf("post-reset id = %q, want 51 (future cursor did not snap to head)", frames[1].id)
	}
}

// TestEventsStreamSnapsDeepGapToHead verifies that a cursor far behind the
// head (> maxReplayGap events) snaps forward instead of streaming the gap.
func TestEventsStreamSnapsDeepGapToHead(t *testing.T) {
	live := &fakeEventSubscriber{}
	src := &snapCursorSource{live: live, head: 20_000}

	resp := getEventStream(t, src, live, func(req *http.Request) {
		req.URL.RawQuery = "after=5"
	})

	frames := readSSEFrames(t, resp.Body, 2)
	if frames[0].event != eventsCursorResetEvent || frames[0].id != "20000" {
		t.Fatalf("first frame = %q/%q, want reset at 20000", frames[0].event, frames[0].id)
	}
	if frames[1].id != "20001" {
		t.Fatalf("post-reset id = %q, want 20001 (deep-gap cursor was replayed instead of snapped)", frames[1].id)
	}
}

// TestEventsStreamReplaysSmallGap verifies that genuine small-gap resumes —
// the EventSource auto-reconnect path — still replay durable history.
func TestEventsStreamReplaysSmallGap(t *testing.T) {
	live := &fakeEventSubscriber{}
	src := &snapCursorSource{live: live, head: 20}

	resp := getEventStream(t, src, live, func(req *http.Request) {
		req.URL.RawQuery = "after=10"
	})

	ids := readSSEIDs(t, resp.Body, 1)
	if got, want := ids[0], "11"; got != want {
		t.Fatalf("id = %q, want %q (small-gap resume did not replay from the cursor)", got, want)
	}
}
