package httpd

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
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
			handler := requestLogger(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				envelope.WriteError(w, r, tc.err)
			}))

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/sessions/x/kill", nil))

			got := buf.String()
			if tc.absent {
				if strings.Contains(got, tc.wantInLog) {
					t.Fatalf("log line unexpectedly contains %q:\n%s", tc.wantInLog, got)
				}
				return
			}
			if !strings.Contains(got, tc.wantInLog) {
				t.Fatalf("log line missing %q:\n%s", tc.wantInLog, got)
			}
		})
	}
}
