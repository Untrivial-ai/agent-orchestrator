package httpd

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

// corsMiddleware grants cross-origin read access to the allowlisted browser
// origins only. The daemon is a no-auth loopback service, so CORS is the one
// boundary between it and hostile browser content running on the same
// machine: the allowlist must never contain "*" or the opaque "null" origin
// (every file:// page and sandboxed iframe on any website presents "null").
// The packaged Electron renderer is served from app://renderer specifically
// so it has a distinct, unforgeable origin this allowlist can name.
//
// Requests without an Origin header (the CLI, curl, health probes) pass
// through untouched. Requests bearing an Origin outside the allowlist are
// rejected with 403 before any handler runs: merely omitting CORS headers
// would hide the response but NOT the side effect — a hostile page can issue
// "simple" cross-origin POSTs (no-cors mode, text/plain body) that handlers
// would otherwise execute. Same philosophy as localControlRequest on
// /shutdown, applied to the whole surface.
func corsMiddleware(allowedOrigins []string, headersManagedByProxy bool) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		origin = strings.TrimSpace(origin)
		if origin == "" || origin == "null" || origin == "*" {
			continue
		}
		allowed[origin] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}
			// Cache keys must split on Origin even for rejected values, or a
			// 403 could be replayed to an allowed origin. In proxy-managed mode
			// the upstream owns this header along with the rest of CORS.
			if !headersManagedByProxy {
				w.Header().Add("Vary", "Origin")
			}
			if _, ok := allowed[origin]; !ok && !isLoopbackOrigin(origin) {
				envelope.WriteAPIError(w, r, http.StatusForbidden, "forbidden", "ORIGIN_FORBIDDEN",
					"Origin is not allowed to access this daemon", nil)
				return
			}
			// A trusted preview proxy can own the response headers while AO still
			// enforces the origin boundary before any state-changing handler runs.
			// Emitting ACAO at both layers produces duplicate values, which
			// Chromium correctly rejects as an invalid CORS response.
			if headersManagedByProxy {
				next.ServeHTTP(w, r)
				return
			}

			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)

			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
				if reqHeaders := r.Header.Get("Access-Control-Request-Headers"); reqHeaders != "" {
					h.Set("Access-Control-Allow-Headers", reqHeaders)
				}
				h.Set("Access-Control-Max-Age", "600")
				// Chromium's Private Network Access preflight for requests
				// reaching loopback from a less-private address space.
				if r.Header.Get("Access-Control-Request-Private-Network") == "true" {
					h.Set("Access-Control-Allow-Private-Network", "true")
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// isLoopbackOrigin reports whether a browser origin is content served from
// this machine's loopback (the Vite dev server / preview server on whatever
// port it picked). Such content can already reach the no-auth daemon directly,
// so granting it CORS adds no exposure — while a remote page can never bear a
// loopback origin (DNS rebinding changes resolution, not the Origin header).
func isLoopbackOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	// RFC 6761 reserves localhost and every name below it for loopback.
	// Workspace previews use a per-session subdomain so browser-enforced CORS
	// requests (ES modules and fetch) remain isolated without being rejected by
	// the daemon's origin boundary.
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
