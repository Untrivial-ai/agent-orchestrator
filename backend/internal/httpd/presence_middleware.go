package httpd

import (
	"net/http"

	"github.com/aoagents/agent-orchestrator/backend/internal/presence"
)

// InstallIDHeader carries the mobile app's stable per-install identifier. The
// phone sets it on every request; the desktop renderer never does.
const InstallIDHeader = "X-AO-Install-Id"

// presenceMiddleware records each request's install id so the roster can report
// which phones are running the app right now. It never rejects a request: an
// absent header simply means the caller is not a phone (or is an older build),
// and that device reads as offline rather than failing.
func presenceMiddleware(t *presence.Tracker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if t == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Touch(r.Header.Get(InstallIDHeader))
			next.ServeHTTP(w, r)
		})
	}
}
