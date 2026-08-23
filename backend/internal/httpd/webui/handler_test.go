package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandlerServesIndexWithoutRedirect guards against a real bug found via
// live daemon smoke-testing: naively rewriting r.URL.Path to "/index.html"
// and delegating to http.FileServer triggers FileServer's built-in "a request
// literally for .../index.html redirects to .../" behavior, turning every
// request for "/" or an SPA client-side route into a 301 (and, for "/"
// specifically, an infinite-redirect-shaped loop from the browser's
// perspective). index.html must be served directly, at 200, with no redirect.
func TestHandlerServesIndexWithoutRedirect(t *testing.T) {
	h := Handler()

	for _, path := range []string{"/", "/index.html"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("%s: got %d want 200 (no redirect)", path, rec.Code)
			}
			if loc := rec.Header().Get("Location"); loc != "" {
				t.Fatalf("%s: unexpected Location header %q", path, loc)
			}
			if rec.Body.Len() == 0 {
				t.Fatalf("%s: empty body", path)
			}
		})
	}
}

// TestHandlerSPAHistoryFallback proves a client-side route with no matching
// file (no file extension) serves the app shell directly at 200, not a
// redirect — the same bug as above, reached via the fallback branch instead
// of the literal "/index.html" branch.
func TestHandlerSPAHistoryFallback(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/some/client/route", nil)
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d want 200 (SPA fallback, no redirect)", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Fatalf("unexpected Location header %q", loc)
	}
}

// TestHandlerNeverShadowsAPIShapedPaths proves the static handler 404s (does
// not serve index.html for) every reserved prefix, even though those paths
// have no file extension and would otherwise match the SPA-fallback branch.
func TestHandlerNeverShadowsAPIShapedPaths(t *testing.T) {
	paths := []string{
		"/api/v1/sessions",
		"/mux",
		"/healthz",
		"/readyz",
		"/shutdown",
		"/internal/telemetry",
		"/login",
	}
	h := Handler()
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s: got %d want 404 (must never be shadowed by the SPA)", path, rec.Code)
			}
		})
	}
}

// TestIsAPIShapedPath is a table test of the prefix matcher router.go's
// NotFound hook relies on.
func TestIsAPIShapedPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/api/v1/sessions", true},
		{"/apix", false}, // must not match unrelated siblings
		{"/mux", true},
		{"/healthz", true},
		{"/readyz", true},
		{"/shutdown", true},
		{"/internal/foo", true},
		{"/login", true},
		{"/", false},
		{"/index.html", false},
		{"/assets/app.js", false},
	}
	for _, tc := range cases {
		if got := IsAPIShapedPath(tc.path); got != tc.want {
			t.Errorf("IsAPIShapedPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
