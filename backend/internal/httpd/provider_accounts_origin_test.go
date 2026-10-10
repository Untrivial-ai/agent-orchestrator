package httpd

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Every account route is renderer-only: a foreign browser Origin is refused and
// the LAN listener does not serve it. Neighbouring routes are left alone.
func TestProviderAccountRoutesAreRendererAndComputerOnly(t *testing.T) {
	serve := func(middleware func(http.Handler) http.Handler, method, path, origin string) (bool, int) {
		called := false
		request := httptest.NewRequest(method, path, nil)
		request.RemoteAddr = "192.168.1.42:12345"
		request.Header.Set("Origin", origin)
		response := httptest.NewRecorder()
		middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusNoContent)
		})).ServeHTTP(response, request)
		return called, response.Code
	}
	origin := accountOriginMiddleware([]string{"app://renderer", "http://127.0.0.1:5173"})
	for path, account := range map[string]bool{
		"/api/v1/provider-accounts":                  true,
		"/api/v1/provider-accounts/":                 true,
		"/api/v1/provider-accounts/account/actions":  true,
		"/api/v1/provider-accounts/login":            true,
		"/api/v1/provider-accounts/login/attempt":    true,
		"/api/v1/provider-accounts/sessions/session": true,
		"/api/v1/provider-accounts-summary":          false,
		"/api/v1/sessions/session":                   false,
		"/api/v1/sessions/session/conversation":      false,
		"/api/v1/agents/codex/models":                false,
		"/api/v1/agents/claude-code/models":          false,
		identityProbePath:                            false,
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete, http.MethodOptions} {
			for from, trusted := range map[string]bool{
				"": true, "app://renderer": true, "http://127.0.0.1:5173": true,
				"http://127.0.0.1:9876": false, "http://ao-preview.session.localhost:9876": false, "https://example.test": false, "null": false, "app://renderer.evil": false,
			} {
				called, status := serve(origin, method, path, from)
				if refused := account && !trusted; called == refused || (refused && status != http.StatusForbidden) {
					t.Errorf("%s %s from %q: called=%v status=%d", method, path, from, called, status)
				}
			}
			called, status := serve(lanControlBlock, method, path, "app://renderer")
			if called == account || (account && status != http.StatusNotFound) || isLANControlBlockedPath(path) != account {
				t.Errorf("LAN %s %s: called=%v status=%d", method, path, called, status)
			}
		}
	}
}
