package httpd

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/observe/ownership"
	"github.com/aoagents/agent-orchestrator/backend/internal/observe/sentryobs"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/telemetrymeta"
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
func requestLogger(log *slog.Logger, sink ports.EventSink) func(http.Handler) http.Handler {
	return requestLoggerWithCapture(log, sink, sentryobs.CaptureHTTPError)
}

type captureHTTPErrorFunc func(context.Context, error, map[string]string, string)

func requestLoggerWithCapture(log *slog.Logger, sink ports.EventSink, captureHTTPError captureHTTPErrorFunc) func(http.Handler) http.Handler {
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
				if ww.Status() >= http.StatusInternalServerError {
					path := telemetrymeta.RoutePattern(r)
					capErr := captured.Err
					var errorKind, errorCode string
					if capErr != nil {
						errorKind, errorCode = telemetrymeta.ErrorKindAndCode(capErr)
					}
					// Same grouping key for PostHog and Sentry so an issue lines up
					// across both.
					fingerprint := telemetrymeta.Fingerprint("httpd", "http_request", r.Method, path, strconv.Itoa(ww.Status()), errorKind, errorCode)
					if sink != nil {
						payload := map[string]any{
							"component":     "httpd",
							"operation":     "http_request",
							"method":        r.Method,
							"path":          path,
							"status":        ww.Status(),
							"status_family": telemetrymeta.StatusFamily(ww.Status()),
							"duration":      time.Since(start).Milliseconds(),
						}
						if capErr != nil {
							payload["error_kind"] = errorKind
							if errorCode != "" {
								payload["error_code"] = errorCode
							}
							payload["fingerprint"] = fingerprint
						}
						sink.Emit(r.Context(), ports.TelemetryEvent{
							Name:       "ao.http.5xx",
							Source:     "http",
							OccurredAt: time.Now().UTC(),
							Level:      ports.TelemetryLevelError,
							RequestID:  middleware.GetReqID(r.Context()),
							Payload:    payload,
						})
					}
					// Capture genuine faults to Sentry with the real error/stack.
					// 503 (transient contention) is excluded by ShouldCaptureStatus.
					if sentryobs.ShouldCaptureStatus(ww.Status()) && captured.ReportingOwner != ownership.OwnerAgentSwitchSaga {
						err := capErr
						if err == nil {
							err = fmt.Errorf("HTTP %d %s %s", ww.Status(), r.Method, path)
						}
						captureHTTPError(r.Context(), err, map[string]string{
							"component":  "httpd",
							"operation":  "http_request",
							"method":     r.Method,
							"path":       path,
							"status":     strconv.Itoa(ww.Status()),
							"category":   "http_5xx",
							"error_kind": errorKind,
							"error_code": errorCode,
							"request_id": middleware.GetReqID(r.Context()),
						}, fingerprint)
					}
				}
			}()
			next.ServeHTTP(ww, r)
		})
	}
}
