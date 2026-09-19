package httpd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
)

// TestCORS exercises the allowlist boundary on a real router: trusted origins
// get per-origin CORS headers (REST reads and preflights), everything else —
// including the opaque "null" origin and no-Origin CLI traffic — gets none.
func TestCORS(t *testing.T) {
	cfg := config.Config{AllowedOrigins: []string{"app://renderer"}}
	router := newTestRouter(cfg, discardLogger(), nil)
	srv := httptest.NewServer(router)
	defer srv.Close()

	tests := []struct {
		name       string
		method     string
		headers    map[string]string
		wantStatus int
		wantACAO   string
	}{
		{
			name:       "allowed origin gets ACAO",
			method:     http.MethodGet,
			headers:    map[string]string{"Origin": "app://renderer"},
			wantStatus: http.StatusOK,
			wantACAO:   "app://renderer",
		},
		{
			// Not in the allowlist — trusted because loopback-served content
			// can already reach the daemon directly (dev/preview servers on
			// arbitrary ports).
			name:       "loopback origin allowed without an allowlist entry",
			method:     http.MethodGet,
			headers:    map[string]string{"Origin": "http://localhost:5181"},
			wantStatus: http.StatusOK,
			wantACAO:   "http://localhost:5181",
		},
		{
			name:       "loopback IP origin allowed",
			method:     http.MethodGet,
			headers:    map[string]string{"Origin": "http://127.0.0.1:8080"},
			wantStatus: http.StatusOK,
			wantACAO:   "http://127.0.0.1:8080",
		},
		{
			name:       "isolated localhost preview origin allowed",
			method:     http.MethodGet,
			headers:    map[string]string{"Origin": "http://ao-preview.mfxs2mi.localhost:5181"},
			wantStatus: http.StatusOK,
			wantACAO:   "http://ao-preview.mfxs2mi.localhost:5181",
		},
		{
			// localhost in the host position of a non-loopback origin must not
			// fool the predicate.
			name:       "lookalike origin rejected",
			method:     http.MethodGet,
			headers:    map[string]string{"Origin": "http://localhost.evil.example"},
			wantStatus: http.StatusForbidden,
			wantACAO:   "",
		},
		{
			// Rejected outright, not just denied CORS headers: a missing ACAO
			// hides the response but a "simple" cross-origin POST would still
			// execute the handler on this no-auth daemon.
			name:       "unknown origin is rejected before handlers",
			method:     http.MethodGet,
			headers:    map[string]string{"Origin": "http://evil.example"},
			wantStatus: http.StatusForbidden,
			wantACAO:   "",
		},
		{
			name:       "localhost suffix lookalike rejected",
			method:     http.MethodGet,
			headers:    map[string]string{"Origin": "http://ao-preview.localhost.evil.example"},
			wantStatus: http.StatusForbidden,
			wantACAO:   "",
		},
		{
			name:       "null origin is rejected",
			method:     http.MethodGet,
			headers:    map[string]string{"Origin": "null"},
			wantStatus: http.StatusForbidden,
			wantACAO:   "",
		},
		{
			name:       "no origin passes through untouched",
			method:     http.MethodGet,
			headers:    nil,
			wantStatus: http.StatusOK,
			wantACAO:   "",
		},
		{
			name:   "preflight from allowed origin",
			method: http.MethodOptions,
			headers: map[string]string{
				"Origin":                         "app://renderer",
				"Access-Control-Request-Method":  "POST",
				"Access-Control-Request-Headers": "content-type",
			},
			wantStatus: http.StatusNoContent,
			wantACAO:   "app://renderer",
		},
		{
			name:   "preflight from unknown origin is rejected",
			method: http.MethodOptions,
			headers: map[string]string{
				"Origin":                        "http://evil.example",
				"Access-Control-Request-Method": "POST",
			},
			wantStatus: http.StatusForbidden,
			wantACAO:   "",
		},
	}

	client := &http.Client{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, srv.URL+"/healthz", nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("%s /healthz: %v", tt.method, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if got := resp.Header.Get("Access-Control-Allow-Origin"); got != tt.wantACAO {
				t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, tt.wantACAO)
			}
			if tt.headers["Origin"] != "" && resp.Header.Get("Vary") == "" {
				t.Error("Vary header missing for request with Origin")
			}
		})
	}
}

// TestCORSPreflightHeaders pins the preflight grant shape: methods, echoed
// request headers, max-age, and the private-network opt-in.
func TestCORSPreflightHeaders(t *testing.T) {
	cfg := config.Config{AllowedOrigins: []string{"app://renderer"}}
	router := newTestRouter(cfg, discardLogger(), nil)
	srv := httptest.NewServer(router)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodOptions, srv.URL+"/api/v1/sessions", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Origin", "app://renderer")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type")
	req.Header.Set("Access-Control-Request-Private-Network", "true")

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("OPTIONS: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	for header, want := range map[string]string{
		"Access-Control-Allow-Origin":          "app://renderer",
		"Access-Control-Allow-Methods":         "GET, POST, PATCH, PUT, DELETE, OPTIONS",
		"Access-Control-Allow-Headers":         "content-type",
		"Access-Control-Max-Age":               "600",
		"Access-Control-Allow-Private-Network": "true",
	} {
		if got := resp.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}
