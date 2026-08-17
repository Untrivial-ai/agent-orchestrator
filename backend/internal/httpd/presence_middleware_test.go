package httpd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/presence"
)

func TestPresenceMiddlewareTouchesTracker(t *testing.T) {
	tr := presence.NewTracker()
	h := presenceMiddleware(tr)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	req.Header.Set(InstallIDHeader, "inst-1")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !tr.Live()["inst-1"] {
		t.Fatal("middleware did not record the install id")
	}
}

func TestPresenceMiddlewareIgnoresMissingHeader(t *testing.T) {
	tr := presence.NewTracker()
	h := presenceMiddleware(tr)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a missing header must not fail the request", rec.Code)
	}
	if len(tr.Live()) != 0 {
		t.Fatalf("live = %+v, want empty", tr.Live())
	}
}

func TestPresenceMiddlewareNilTrackerIsPassthrough(t *testing.T) {
	h := presenceMiddleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want the inner handler to run", rec.Code)
	}
}
