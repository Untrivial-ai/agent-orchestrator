package httpd

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

// requestLogger emits one structured access-log line per request via the
// daemon's slog logger. Chi's built-in middleware.Logger writes to stdout
// using stdlib log; reusing the daemon's slog keeps every line on stderr in
// the same key=value shape as the rest of the daemon (one stream for the
// Electron supervisor to capture, one format to grep).
//
// Status, bytes, and duration come from a wrapped ResponseWriter so the log
// is accurate even when the handler returns without calling WriteHeader. The
// request id is read off the context populated by middleware.RequestID, so
// this middleware must be mounted after it.
//
// A 5xx line additionally carries the raw service error recorded by
// envelope.WriteError: the wire envelope hides internals ("Internal server
// error"), so without this the cause of a 500 was lost entirely.
func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			r, capturedErr := envelope.WithErrorCapture(r)
			start := time.Now()
			defer func() {
				attrs := []any{
					"id", middleware.GetReqID(r.Context()),
					"method", r.Method,
					"path", r.URL.Path,
					"status", ww.Status(),
					"bytes", ww.BytesWritten(),
					"duration", time.Since(start),
					"remote", r.RemoteAddr,
				}
				captured := capturedErr()
				if captured.Err != nil && ww.Status() >= http.StatusInternalServerError {
					attrs = append(attrs, "error", captured.Err)
				}
				log.Info("http request", attrs...)
			}()
			next.ServeHTTP(ww, r)
		})
	}
}
