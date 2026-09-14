package sse

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

const (
	// DefaultWriteTimeout is the rolling per-write deadline for SSE frame writes.
	DefaultWriteTimeout = 5 * time.Second

	// DefaultHeartbeatInterval is the default idle heartbeat interval.
	DefaultHeartbeatInterval = 15 * time.Second
)

// ErrUnsupported is returned when the ResponseWriter does not support flushing.
var ErrUnsupported = errors.New("sse: streaming unsupported by server")

// Event represents an individual Server-Sent Event frame.
type Event struct {
	ID    string
	Event string
	Data  []byte
}

// Option configures the SSE Writer.
type Option func(*Writer)

// WithWriteTimeout sets the rolling write timeout for SSE frame delivery.
func WithWriteTimeout(d time.Duration) Option {
	return func(w *Writer) {
		w.writeTimeout = d
	}
}

// WithHeader adds a response header prior to writing HTTP 200 OK.
func WithHeader(key, val string) Option {
	return func(w *Writer) {
		w.w.Header().Set(key, val)
	}
}

// Writer provides structured framing, checked flushing, and rolling write
// deadlines for Server-Sent Events streams.
type Writer struct {
	w            http.ResponseWriter
	rc           *http.ResponseController
	writeTimeout time.Duration
}

// Upgrade verifies flusher support, sets common SSE response headers,
// writes HTTP 200 OK, and flushes the initial frame.
// If streaming is unsupported, an SSE_UNSUPPORTED API error is written to w
// and ErrUnsupported is returned.
func Upgrade(w http.ResponseWriter, r *http.Request, opts ...Option) (*Writer, error) {
	rc := http.NewResponseController(w)

	// Check if flushing is supported directly or via ResponseController unwrap.
	if _, ok := w.(http.Flusher); !ok {
		if err := rc.Flush(); errors.Is(err, http.ErrNotSupported) {
			envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal", "SSE_UNSUPPORTED",
				"Streaming is not supported by this server", nil)
			return nil, ErrUnsupported
		}
	}

	sw := &Writer{
		w:            w,
		rc:           rc,
		writeTimeout: DefaultWriteTimeout,
	}
	for _, opt := range opts {
		opt(sw)
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")

	w.WriteHeader(http.StatusOK)

	// Flush initial headers bounded by write deadline.
	if err := sw.Flush(); err != nil {
		return nil, err
	}

	return sw, nil
}

// SetWriteTimeout updates the rolling write deadline duration.
func (w *Writer) SetWriteTimeout(d time.Duration) {
	w.writeTimeout = d
}

// Header returns the response header map.
func (w *Writer) Header() http.Header {
	return w.w.Header()
}

// ResponseWriter returns the underlying http.ResponseWriter.
func (w *Writer) ResponseWriter() http.ResponseWriter {
	return w.w
}

// WriteEvent writes an SSE event frame with checked flushing and a rolling write deadline.
func (w *Writer) WriteEvent(e Event) error {
	var buf bytes.Buffer
	if e.ID != "" {
		buf.WriteString("id: ")
		buf.WriteString(e.ID)
		buf.WriteByte('\n')
	}
	if e.Event != "" {
		buf.WriteString("event: ")
		buf.WriteString(e.Event)
		buf.WriteByte('\n')
	}
	if len(e.Data) > 0 {
		lines := bytes.Split(e.Data, []byte("\n"))
		for _, line := range lines {
			buf.WriteString("data: ")
			buf.Write(line)
			buf.WriteByte('\n')
		}
	} else {
		buf.WriteString("data:\n")
	}
	buf.WriteByte('\n')

	return w.writeFrame(buf.Bytes())
}

// WriteJSON marshals v as JSON and writes it as an SSE event frame.
func (w *Writer) WriteJSON(id, event string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return w.WriteEvent(Event{
		ID:    id,
		Event: event,
		Data:  data,
	})
}

// WriteComment writes an SSE comment frame (e.g. ": keepalive\n\n" or ":\n\n").
// Comment frames are no-op frames that pace idle connections and flush buffering proxies.
func (w *Writer) WriteComment(comment string) error {
	var buf bytes.Buffer
	buf.WriteString(":")
	if comment != "" {
		buf.WriteString(" ")
		buf.WriteString(comment)
	}
	buf.WriteString("\n\n")

	return w.writeFrame(buf.Bytes())
}

// Flush flushes pending buffered data with a rolling write deadline.
func (w *Writer) Flush() error {
	return w.writeFrame(nil)
}

func (w *Writer) writeFrame(data []byte) error {
	if w.writeTimeout > 0 {
		if err := w.rc.SetWriteDeadline(time.Now().Add(w.writeTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		defer func() {
			_ = w.rc.SetWriteDeadline(time.Time{})
		}()
	}

	if len(data) > 0 {
		if _, err := w.w.Write(data); err != nil {
			return err
		}
	}

	if err := w.rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}
