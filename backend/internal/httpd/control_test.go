package httpd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
)

// TestShutdownGuard verifies that POST /shutdown only fires for a trusted local
// caller: a loopback Host with no Origin header. A cross-site Origin or a
// non-loopback (DNS-rebinding) Host must be rejected without triggering the
// shutdown side effect.
func TestShutdownGuard(t *testing.T) {
	cases := []struct {
		name       string
		host       string
		origin     string
		wantStatus int
		wantFired  bool
	}{
		{name: "loopback no origin", host: "127.0.0.1:3001", wantStatus: http.StatusAccepted, wantFired: true},
		{name: "localhost no origin", host: "localhost:3001", wantStatus: http.StatusAccepted, wantFired: true},
		{name: "cross-site origin", host: "127.0.0.1:3001", origin: "https://evil.example", wantStatus: http.StatusForbidden, wantFired: false},
		{name: "rebinding host", host: "evil.example", wantStatus: http.StatusForbidden, wantFired: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fired := false
			r := NewRouterWithControl(config.Config{}, discardLogger(), nil, APIDeps{}, ControlDeps{
				RequestShutdown: func() { fired = true },
			})

			req := httptest.NewRequest(http.MethodPost, "http://"+tc.host+"/shutdown", nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if fired != tc.wantFired {
				t.Fatalf("shutdown fired = %v, want %v", fired, tc.wantFired)
			}
		})
	}
}
