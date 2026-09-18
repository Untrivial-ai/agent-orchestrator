package envelope

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// codeErr mimics the modernc.org/sqlite error surface (a Code() int method)
// without importing the driver, so the transient detector can be tested in
// isolation.
type codeErr struct {
	code int
	msg  string
}

func (e codeErr) Error() string { return e.msg }
func (e codeErr) Code() int     { return e.code }

func statusOf(t *testing.T, err error) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/anything", nil)
	WriteError(rec, req, err)
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body.Code
}

func TestWriteError_TransientMapsTo503(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"sqlite busy", codeErr{code: sqliteBusy, msg: "database is locked"}},
		{"sqlite locked", codeErr{code: sqliteLocked, msg: "database table is locked"}},
		{"sqlite busy extended", codeErr{code: 261, msg: "SQLITE_BUSY_RECOVERY"}}, // 261 & 0xFF == 5
		{"sqlite locked extended", codeErr{code: 262, msg: "SQLITE_LOCKED_SHAREDCACHE"}},
		{"wrapped busy", fmt.Errorf("get session: %w", codeErr{code: sqliteBusy, msg: "database is locked"})},
		{"deadline exceeded", context.DeadlineExceeded},
		{"wrapped deadline", fmt.Errorf("query: %w", context.DeadlineExceeded)},
		{"apierr unavailable", apierr.Unavailable("SERVICE_UNAVAILABLE", "busy")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := statusOf(t, tc.err)
			if status != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", status)
			}
		})
	}
}

func TestWriteError_NonTransientStays500(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"plain error", errors.New("boom")},
		// A non-busy sqlite code must NOT be treated as transient.
		{"sqlite constraint", codeErr{code: 19, msg: "UNIQUE constraint failed"}},
		// Client cancellation is deliberately excluded from transient.
		{"context canceled", context.Canceled},
		{"apierr internal", apierr.Internal("INTERNAL", "nope")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := statusOf(t, tc.err)
			if status != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", status)
			}
		})
	}
}

func TestWriteError_WorkspaceRepoUnavailableMapsTo404(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/s1/workspace/files", nil)
	// The sentinel reaches WriteError wrapped by application layers, so match
	// the wrapped shape too.
	err := fmt.Errorf("git -C /repo status: %w", ports.ErrWorkspaceRepoUnavailable)

	WriteError(rec, req, err)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body APIError
	if decodeErr := json.Unmarshal(rec.Body.Bytes(), &body); decodeErr != nil {
		t.Fatalf("decode response: %v", decodeErr)
	}
	if body.Code != "PROJECT_FOLDER_MISSING" {
		t.Fatalf("code = %q, want PROJECT_FOLDER_MISSING", body.Code)
	}
	if body.Error != "not_found" {
		t.Fatalf("error = %q, want not_found", body.Error)
	}
	if body.Message != "Project repository is missing from disk" {
		t.Fatalf("message = %q, want the missing-repo message", body.Message)
	}
}

func TestWriteError_TypedKindsUnchanged(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{apierr.Invalid("BAD", "x", nil), http.StatusBadRequest},
		{apierr.NotFound("NF", "x"), http.StatusNotFound},
		{apierr.Conflict("C", "x", nil), http.StatusConflict},
		{apierr.Forbidden("F", "x"), http.StatusForbidden},
	} {
		status, _ := statusOf(t, tc.err)
		if status != tc.status {
			t.Fatalf("%v: status = %d, want %d", tc.err, status, tc.status)
		}
	}
}
