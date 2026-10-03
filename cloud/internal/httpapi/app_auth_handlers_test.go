package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAppAuthReturn(t *testing.T) {
	srv := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/app/auth/return?code=abc123&state=xyz789", nil)
	rec := httptest.NewRecorder()

	srv.appAuthReturn(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q, want text/html", ct)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control = %q, want no-store", got)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("referrer-policy = %q, want no-referrer", got)
	}

	body := rec.Body.String()
	// The page forwards the OAuth result to the ao-app:// deep link client-side.
	if !strings.Contains(body, "ao-app://callback") {
		t.Fatalf("body missing ao-app://callback forward target")
	}
	// It must NOT reflect the OAuth query into the HTML (no server-side echo =
	// no reflected-XSS vector); the query is read client-side from location.search.
	if strings.Contains(body, "abc123") || strings.Contains(body, "xyz789") {
		t.Fatalf("body unexpectedly reflected the OAuth query params")
	}
}
