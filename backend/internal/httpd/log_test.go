package httpd

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/observe/ownership"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// TestRequestLoggerRecords5xxCause: the wire envelope collapses unrecognized
// service errors into "Internal server error", so the access log line is the
// only place the cause can survive. A 500 must carry it; a typed 4xx (whose
// envelope already explains itself) must not.
func TestRequestLoggerRecords5xxCause(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantInLog string
		absent    bool
	}{
		{name: "raw error on 500 is logged", err: errors.New("gitworktree: worktree remove exploded"), wantInLog: "gitworktree: worktree remove exploded"},
		{name: "typed 404 carries no error attr", err: apierr.NotFound("SESSION_NOT_FOUND", "Unknown session"), wantInLog: "error=", absent: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))
			sink := &captureSink{}
			handler := requestLogger(log, sink)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				envelope.WriteError(w, r, tc.err)
			}))

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/sessions/x/kill", nil))

			got := buf.String()
			if tc.absent {
				if strings.Contains(got, tc.wantInLog) {
					t.Fatalf("log line unexpectedly contains %q:\n%s", tc.wantInLog, got)
				}
				if len(sink.events) != 0 {
					t.Fatalf("5xx telemetry events = %d, want 0 for typed 4xx", len(sink.events))
				}
				return
			}
			if !strings.Contains(got, tc.wantInLog) {
				t.Fatalf("log line missing %q:\n%s", tc.wantInLog, got)
			}
			if len(sink.events) != 1 || sink.events[0].Name != "ao.http.5xx" {
				t.Fatalf("telemetry events = %#v, want one ao.http.5xx event", sink.events)
			}
			payload := sink.events[0].Payload
			if got := payload["component"]; got != "httpd" {
				t.Fatalf("payload.component = %#v, want httpd", got)
			}
			if got := payload["operation"]; got != "http_request" {
				t.Fatalf("payload.operation = %#v, want http_request", got)
			}
			if got := payload["method"]; got != http.MethodPost {
				t.Fatalf("payload.method = %#v, want POST", got)
			}
			if got := payload["path"]; got != "/api/v1/sessions/x/kill" {
				t.Fatalf("payload.path = %#v, want request path fallback", got)
			}
			if got := payload["status"]; got != http.StatusInternalServerError {
				t.Fatalf("payload.status = %#v, want 500", got)
			}
			if got := payload["status_family"]; got != "5xx" {
				t.Fatalf("payload.status_family = %#v, want 5xx", got)
			}
			if got := payload["error_kind"]; got != "internal" {
				t.Fatalf("payload.error_kind = %#v, want internal", got)
			}
			if got := payload["fingerprint"]; got == "" {
				t.Fatalf("payload.fingerprint = %#v, want non-empty", got)
			}
		})
	}
}

func TestRequestLoggerSuppressesOnlySagaOwnedSentryCapture(t *testing.T) {
	tests := []struct {
		name         string
		owner        ownership.Owner
		wantCaptures int
	}{
		{name: "pre-admission HTTP owner captures", owner: ownership.OwnerHTTP, wantCaptures: 1},
		{name: "post-admission saga owner is suppressed", owner: ownership.OwnerAgentSwitchSaga, wantCaptures: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))
			sink := &captureSink{}
			captures := 0
			handler := requestLoggerWithCapture(log, sink, func(context.Context, error, map[string]string, string) {
				captures++
			})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				envelope.WriteError(w, r, ownership.Own(errors.New("switch settlement failed"), tc.owner))
			}))

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s1/switch-agent", nil))

			if captures != tc.wantCaptures {
				t.Fatalf("Sentry captures = %d, want %d", captures, tc.wantCaptures)
			}
			if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), `"message":"Internal server error"`) {
				t.Fatalf("response changed: status=%d body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(buf.String(), "switch settlement failed") {
				t.Fatalf("local log lost error: %s", buf.String())
			}
			if len(sink.events) != 1 || sink.events[0].Name != "ao.http.5xx" {
				t.Fatalf("HTTP telemetry events = %#v, want one ao.http.5xx", sink.events)
			}
		})
	}
}

type captureSink struct {
	events []ports.TelemetryEvent
}

func (s *captureSink) Emit(_ context.Context, ev ports.TelemetryEvent) {
	s.events = append(s.events, ev)
}

func (s *captureSink) Close(context.Context) error { return nil }
