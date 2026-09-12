package sse_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/sse"
)

type nonFlusherResponseWriter struct {
	header http.Header
}

func (n *nonFlusherResponseWriter) Header() http.Header {
	if n.header == nil {
		n.header = make(http.Header)
	}
	return n.header
}
func (n *nonFlusherResponseWriter) Write(b []byte) (int, error) { return len(b), nil }
func (n *nonFlusherResponseWriter) WriteHeader(statusCode int)   {}

func TestUpgrade_Success(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/events", nil)

	sw, err := sse.Upgrade(rec, req,
		sse.WithHeader("X-Custom", "custom-val"),
		sse.WithWriteTimeout(100*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sw == nil {
		t.Fatal("expected non-nil writer")
	}

	if got := rec.Header().Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/event-stream; charset=utf-8", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
	if got := rec.Header().Get("Connection"); got != "keep-alive" {
		t.Errorf("Connection = %q, want keep-alive", got)
	}
	if got := rec.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering = %q, want no", got)
	}
	if got := rec.Header().Get("X-Custom"); got != "custom-val" {
		t.Errorf("X-Custom = %q, want custom-val", got)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !rec.Flushed {
		t.Error("expected initial flush")
	}
}

func TestUpgrade_UnsupportedFlusher(t *testing.T) {
	w := &nonFlusherResponseWriter{}
	req := httptest.NewRequest(http.MethodGet, "/events", nil)

	sw, err := sse.Upgrade(w, req)
	if err != sse.ErrUnsupported {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
	if sw != nil {
		t.Fatal("expected nil writer on failure")
	}
}

func TestWriter_WriteEvent(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/events", nil)

	sw, err := sse.Upgrade(rec, req)
	if err != nil {
		t.Fatal(err)
	}
	rec.Body.Reset()

	err = sw.WriteEvent(sse.Event{
		ID:    "42",
		Event: "test_event",
		Data:  []byte("hello world"),
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "id: 42\nevent: test_event\ndata: hello world\n\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("WriteEvent got %q, want %q", got, want)
	}
}

func TestWriter_WriteEventMultiline(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/events", nil)

	sw, err := sse.Upgrade(rec, req)
	if err != nil {
		t.Fatal(err)
	}
	rec.Body.Reset()

	err = sw.WriteEvent(sse.Event{
		Event: "multiline",
		Data:  []byte("line1\nline2"),
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "event: multiline\ndata: line1\ndata: line2\n\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("WriteEvent got %q, want %q", got, want)
	}
}

func TestWriter_WriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/events", nil)

	sw, err := sse.Upgrade(rec, req)
	if err != nil {
		t.Fatal(err)
	}
	rec.Body.Reset()

	payload := struct {
		Message string `json:"message"`
	}{Message: "success"}

	if err := sw.WriteJSON("10", "notification", payload); err != nil {
		t.Fatal(err)
	}

	data, _ := json.Marshal(payload)
	want := "id: 10\nevent: notification\ndata: " + string(data) + "\n\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("WriteJSON got %q, want %q", got, want)
	}
}

func TestWriter_WriteComment(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/events", nil)

	sw, err := sse.Upgrade(rec, req)
	if err != nil {
		t.Fatal(err)
	}
	rec.Body.Reset()

	if err := sw.WriteComment(""); err != nil {
		t.Fatal(err)
	}
	if got := rec.Body.String(); got != ":\n\n" {
		t.Fatalf("empty comment got %q, want %q", got, ":\n\n")
	}

	rec.Body.Reset()
	if err := sw.WriteComment("keepalive"); err != nil {
		t.Fatal(err)
	}
	if got := rec.Body.String(); got != ": keepalive\n\n" {
		t.Fatalf("named comment got %q, want %q", got, ": keepalive\n\n")
	}
}

func TestWriter_UnwrapsThroughMiddleware(t *testing.T) {
	rec := httptest.NewRecorder()
	ww := middleware.NewWrapResponseWriter(rec, 1)
	req := httptest.NewRequest(http.MethodGet, "/events", nil)

	sw, err := sse.Upgrade(ww, req)
	if err != nil {
		t.Fatal(err)
	}
	rec.Body.Reset()

	if err := sw.WriteComment("heartbeat"); err != nil {
		t.Fatal(err)
	}
	if got := rec.Body.String(); !strings.Contains(got, ": heartbeat") {
		t.Fatalf("expected comment frame through wrap writer, got %q", got)
	}
}

type deadlineRecordingWriter struct {
	*httptest.ResponseRecorder
	mu        sync.Mutex
	deadlines []time.Time
}

func (d *deadlineRecordingWriter) SetWriteDeadline(t time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deadlines = append(d.deadlines, t)
	return nil
}

func (d *deadlineRecordingWriter) getDeadlines() []time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	cp := make([]time.Time, len(d.deadlines))
	copy(cp, d.deadlines)
	return cp
}

func TestWriter_RollingWriteDeadlineAppliedAndReset(t *testing.T) {
	dw := &deadlineRecordingWriter{ResponseRecorder: httptest.NewRecorder()}
	ww := middleware.NewWrapResponseWriter(dw, 1)
	req := httptest.NewRequest(http.MethodGet, "/events", nil)

	sw, err := sse.Upgrade(ww, req, sse.WithWriteTimeout(500*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}

	// Initial Upgrade flushes, which sets a deadline then resets it to zero.
	deadlines := dw.getDeadlines()
	if len(deadlines) < 2 {
		t.Fatalf("expected at least 2 deadline calls from initial Upgrade flush, got %d", len(deadlines))
	}
	if deadlines[0].IsZero() {
		t.Error("expected first deadline to be non-zero (timeout)")
	}
	if !deadlines[1].IsZero() {
		t.Error("expected second deadline to be reset to zero time")
	}

	// Next write sets deadline and resets to zero again.
	dw.deadlines = nil
	if err := sw.WriteComment(""); err != nil {
		t.Fatal(err)
	}
	deadlines = dw.getDeadlines()
	if len(deadlines) != 2 {
		t.Fatalf("expected 2 deadline calls, got %d", len(deadlines))
	}
	if deadlines[0].IsZero() {
		t.Error("expected rolling deadline before write")
	}
	if !deadlines[1].IsZero() {
		t.Error("expected deadline cleared after write")
	}
}
