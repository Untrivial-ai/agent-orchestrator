package httpd

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
	"github.com/aoagents/agent-orchestrator/backend/internal/websession"
)

// authKind records how a request was authenticated. Handlers thread this through
// the context to gate CSRF and CSWSH checks: ambient credentials (cookie, identity)
// require same-origin validation; Bearer is unaffected.
type authKind int

const (
	authKindNone authKind = iota
	authKindBearer
	authKindWebSession
	authKindTailscaleIdentity
)

// ctxKey is an unexported type for context keys defined in this package, so a
// key here can never collide with one from another package (go vet SA1029).
type ctxKey int

const (
	authKindCtxKey ctxKey = iota
	realPeerCtxKey
)

// AuthKind returns the authKind from the request context, or authKindNone if absent.
func AuthKind(r *http.Request) authKind {
	if v := r.Context().Value(authKindCtxKey); v != nil {
		if k, ok := v.(authKind); ok {
			return k
		}
	}
	return authKindNone
}

// withAuthKind returns a new context with the given authKind.
func withAuthKind(ctx context.Context, kind authKind) context.Context {
	return context.WithValue(ctx, authKindCtxKey, kind)
}

// WithRealPeer stores the real transport peer address in the context.
func WithRealPeer(ctx context.Context, addr string) context.Context {
	return context.WithValue(ctx, realPeerCtxKey, addr)
}

// getRealPeer retrieves the real transport peer from context, falling back to
// RemoteAddr if not set. Used by the login lockout to key off the genuine source
// (not a spoofed X-Forwarded-For).
func getRealPeer(r *http.Request) string {
	if v := r.Context().Value(realPeerCtxKey); v != nil {
		if s, ok := v.(string); ok {
			if host, _, err := net.SplitHostPort(s); err == nil {
				return host
			}
			return s
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// authState holds the current password hash for the LAN listener. Swapped
// atomically on regenerate so an in-flight request never sees a torn value.
type authState struct{ hash atomic.Pointer[string] }

// NewAuthState returns a new, empty authState. Exported so the daemon can
// construct one before the shared router is built (for the login route) and
// pass the identical instance to NewLANManager via LANManagerConfig.AuthState,
// so authMiddleware validates against the same password hash the login route
// checks. Most callers building the LAN listener alone want NewMobileLAN,
// which constructs its own.
func NewAuthState() *authState { return &authState{} }

func (a *authState) setHash(h string) { a.hash.Store(&h) }
func (a *authState) currentHash() string {
	if p := a.hash.Load(); p != nil {
		return *p
	}
	return ""
}

// lockout throttles password guessing per source address.
type lockout struct {
	mu       sync.Mutex
	limit    int
	cooldown time.Duration
	now      func() time.Time
	fails    map[string]int
	until    map[string]time.Time
}

func newLockout(limit int, cooldown time.Duration, now func() time.Time) *lockout {
	return &lockout{limit: limit, cooldown: cooldown, now: now, fails: map[string]int{}, until: map[string]time.Time{}}
}

// NewLoginLockout returns a lockout configured with the same policy
// authMiddleware uses (5 attempts, 1-minute cooldown). Exported for the same
// cross-router sharing reason as NewAuthState: the daemon constructs one
// instance and passes it to both the shared router's login route and
// NewLANManager, so a brute-force attempt against POST /api/v1/web/session
// trips the identical per-source lockout as any other failed credential.
func NewLoginLockout() *lockout { return newLockout(5, time.Minute, time.Now) }

func (l *lockout) blocked(src string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	t, ok := l.until[src]
	if !ok {
		return false
	}
	if l.now().Before(t) {
		return true
	}
	// Cooldown elapsed: clear the lockout AND the fail counter so the source
	// starts a fresh window. Without this the counter stays at the limit and the
	// very next failure would immediately re-lock for another full cooldown —
	// and a client that keeps polling would stay locked out forever. This also
	// bounds map growth, since expired entries are pruned on the next request.
	delete(l.until, src)
	delete(l.fails, src)
	return false
}

func (l *lockout) fail(src string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[src]++
	if l.fails[src] >= l.limit {
		l.until[src] = l.now().Add(l.cooldown)
	}
}

func (l *lockout) reset(src string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, src)
	delete(l.until, src)
}

func sourceKey(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

// authCookieName carries the connection token for a preview page's in-page
// subresource requests. See connectionToken / maybeSetPreviewAuthCookie.
const authCookieName = "ao_conn"

// previewFilesMarker is the path segment that identifies a preview-file request
// (GET /api/v1/sessions/{id}/preview/files/*). The auth cookie is both scoped to
// and honored only on this path, so it can never authenticate any other endpoint.
const previewFilesMarker = "/preview/files/"

// previewFilesCookiePath returns the cookie Path to scope the auth cookie to the
// requesting session's preview files (".../preview/files/"), or "" if the request
// is not a preview-file request. Scoping this tightly is what keeps the cookie
// from ever reaching /kill, /send, another session, or any non-preview route.
func previewFilesCookiePath(urlPath string) string {
	i := strings.Index(urlPath, previewFilesMarker)
	if i < 0 {
		return ""
	}
	return urlPath[:i+len(previewFilesMarker)]
}

// connectionToken returns the caller's connection token. It comes from the
// Authorization: Bearer header (the mobile API client and a preview page's
// top-level navigation), ONLY on the preview-files route the auth cookie (a
// preview page's subresource requests — images/CSS/JS — which the WebView issues
// without our header), or, ONLY on /mux, the Sec-WebSocket-Protocol
// ao.bearer.<token> subprotocol (ADR-0005 R3) — the escape hatch for a browser that
// cannot attach an Authorization header to a WebSocket upgrade and whose
// cookie doesn't survive an intermediate proxy. Restricting each alternate
// source to its one route means it can never authenticate any other endpoint
// even if a client sends it elsewhere.
func connectionToken(r *http.Request) string {
	if t := bearerToken(r); t != "" {
		return t
	}
	if previewFilesCookiePath(r.URL.Path) != "" {
		if c, err := r.Cookie(authCookieName); err == nil {
			return c.Value
		}
	}
	if r.URL.Path == "/mux" {
		if t := bearerFromSubprotocol(r.Header.Get("Sec-WebSocket-Protocol")); t != "" {
			return t
		}
	}
	return ""
}

// maybeSetPreviewAuthCookie drops the auth cookie when a preview FILE is fetched
// with a valid token, so the WebView's follow-up subresource requests on the same
// password-protected preview route authenticate too (they never carry our
// Authorization header). The cookie is Path-scoped to this session's preview
// files only, HttpOnly, and re-sent only when it doesn't already match the token
// that just authenticated — so a normal subresource costs no Set-Cookie, but a
// cookie left over from a regenerated password is overwritten instead of being
// kept until it 401s every image/CSS/JS on the page. This runs on the LAN
// listener only; the loopback/desktop preview path never reaches authMiddleware,
// so desktop preview behavior is unchanged.
func maybeSetPreviewAuthCookie(w http.ResponseWriter, r *http.Request, tok string) {
	path := previewFilesCookiePath(r.URL.Path)
	if path == "" {
		return
	}
	if c, err := r.Cookie(authCookieName); err == nil && c.Value == tok {
		return // already current; don't re-send Set-Cookie on every subresource
	}
	//nolint:gosec // Secure is intentionally omitted: the LAN bridge is plaintext
	// http by design (ADR 0001, home-network-only), and a Secure cookie would never
	// be sent over it. The token already travels the same plain link via Bearer.
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    tok,
		Path:     path,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// No Secure: the LAN link is plain http (a TLS tunnel still sends it),
		// matching how the Bearer token already travels.
	})
}

// identityProbePath is the one route the LAN listener serves without the
// connection password. The phone races several endpoints, and a private
// address is not an identity: 192.168.1.42 exists on most networks. Verifying
// which machine answered has to happen BEFORE a credential is presented, or
// the phone leaks its token to whatever device holds that address on a foreign
// network. The response carries an opaque host id and nothing else.
//
// Exact path, GET only, and checked ahead of the lockout so a phone racing
// endpoints cannot lock itself out probing. See
// docs/adr/0003-unauthenticated-identity-probe.md.
const identityProbePath = "/api/v1/identity"

// isIdentityProbe reports whether r is the exempt probe. Deliberately an exact
// match rather than a prefix, so nothing nested below the path inherits the
// exemption.
func isIdentityProbe(r *http.Request) bool {
	return r.Method == http.MethodGet && r.URL.Path == identityProbePath
}

// lockoutKey returns the identifier used to key the brute-force lockout. When
// trustXFF is true (the request arrived via the trusted proxy path — a loopback
// LAN bind reached only through a local proxy, per ADR-0005 R5), the caller's
// X-Forwarded-For is genuine (set by the proxy, not the browser) and is the
// only way to distinguish devices, since every proxied request's real TCP peer
// is 127.0.0.1. When trustXFF is false (an untrusted network, e.g. the upstream
// default 0.0.0.0 bind), X-Forwarded-For is attacker-controlled and must be
// ignored; the genuine TCP peer address is used instead.
func lockoutKey(r *http.Request, trustXFF bool) string {
	if trustXFF {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// First value is the original client, per the trusted proxy's convention.
			if i := strings.Index(xff, ","); i >= 0 {
				return strings.TrimSpace(xff[:i])
			}
			return strings.TrimSpace(xff)
		}
	}
	return getRealPeer(r)
}

// authMiddleware authenticates LAN requests using three credentials in order:
// (1) Authorization: Bearer password (native clients)
// (2) web session cookie (browser login)
// (3) Tailscale-User-Login header (browser identity, when enabled)
// connected, which may be nil, is notified of the source address of every
// request that authenticates via Bearer; it exists so telemetry can observe
// that a phone actually reached this desktop, and it must not block the
// request, since it runs inline on every authenticated call.
// sessChecker validates web sessions. identityConfig controls identity trust.
// trustXFF controls whether X-Forwarded-For is trusted for the lockout key
// (see lockoutKey); it must be true only on the trusted-proxy path.
func authMiddleware(
	state *authState,
	lock *lockout,
	connected *mobileConnectReporter,
	sessChecker *websession.Store,
	identityConfig *IdentityAuthConfig,
) func(http.Handler) http.Handler {
	return authMiddlewareWithTrust(state, lock, connected, sessChecker, identityConfig, false)
}

// authMiddlewareWithTrust is authMiddleware with explicit control over whether
// X-Forwarded-For is trusted for the lockout key. NewLANManager derives trustXFF
// from the configured bind host (loopback ⇒ trusted proxy path).
func authMiddlewareWithTrust(
	state *authState,
	lock *lockout,
	connected *mobileConnectReporter,
	sessChecker *websession.Store,
	identityConfig *IdentityAuthConfig,
	trustXFF bool,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The identity probe is the one credential-free API route (ADR-0003).
			// Checked first, ahead of the lockout, so a phone racing endpoints
			// cannot lock itself out probing.
			if isIdentityProbe(r) {
				next.ServeHTTP(w, r)
				return
			}

			// Unauthenticated exemptions: the login page, the login submission,
			// and the session-status check must be reachable with no credential,
			// or a browser with no cookie could never reach /login, log in, nor
			// learn it is unauthenticated. Each of these routes implements its
			// own lockout/credential handling (webSessionHandlers), so they are
			// exempt from this middleware's credential check entirely, not just
			// its failure response.
			if r.Method == http.MethodGet && r.URL.Path == "/login" {
				next.ServeHTTP(w, r)
				return
			}
			if r.URL.Path == "/api/v1/web/session" && (r.Method == http.MethodGet || r.Method == http.MethodPost) {
				next.ServeHTTP(w, r)
				return
			}

			key := lockoutKey(r, trustXFF)

			// Check lockout FIRST: a locked-out source is rejected even with a
			// correct credential, matching the existing Bearer-auth behavior.
			if lock.blocked(key) {
				envelope.WriteAPIError(w, r, http.StatusTooManyRequests, "too_many_requests", "LOCKED_OUT",
					"too many failed attempts; try again shortly", nil)
				return
			}

			// (1) Check Bearer token first (unchanged path for native clients).
			if tok := connectionToken(r); mobilebridge.PasswordMatches(state.currentHash(), tok) {
				lock.reset(key)
				connected.report(sourceKey(r))
				maybeSetPreviewAuthCookie(w, r, tok)
				r = r.WithContext(withAuthKind(r.Context(), authKindBearer))
				next.ServeHTTP(w, r)
				return
			}

			// (2) Check web session cookie.
			if sessChecker != nil {
				if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
					if sessChecker.Validate(c.Value) {
						lock.reset(key)
						r = r.WithContext(withAuthKind(r.Context(), authKindWebSession))
						next.ServeHTTP(w, r)
						return
					}
				}
			}

			// (3) Check Tailscale identity (when enabled).
			if identityConfig != nil && identityConfig.Enabled {
				login := r.Header.Get("Tailscale-User-Login")
				if login != "" && identityConfig.AllowsLogin(login) {
					lock.reset(key)
					r = r.WithContext(withAuthKind(r.Context(), authKindTailscaleIdentity))
					next.ServeHTTP(w, r)
					return
				}
			}

			// All auth failed.
			lock.fail(key)

			// On unauthenticated document navigation, redirect to /login instead of JSON 401.
			if isDocumentNavigation(r) {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}

			// API calls get JSON 401.
			envelope.WriteAPIError(w, r, http.StatusUnauthorized, "unauthorized", "BAD_PASSWORD",
				"missing or invalid connection password", nil)
		})
	}
}

// isDocumentNavigation reports whether the request is a top-level document fetch,
// not an API call or subresource. These should redirect to /login on auth failure.
func isDocumentNavigation(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Mode") == "navigate" {
		return true
	}
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "text/html")
}

// IdentityAuthConfig controls whether Tailscale identity is trusted as a credential.
type IdentityAuthConfig struct {
	Enabled       bool
	AllowedLogins map[string]bool
}

// AllowsLogin checks whether the given login is in the allowlist.
func (c *IdentityAuthConfig) AllowsLogin(login string) bool {
	if c == nil || !c.Enabled {
		return false
	}
	return c.AllowedLogins[login]
}

// csrfMiddleware enforces CSRF protection for ambient credentials (cookie, identity).
// Bearer auth is exempt (it's not ambient). GET/HEAD/OPTIONS are exempt. Loopback
// requests are exempt (localControlRequest). Cross-origin navigations redirect to /login.
func csrfMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Exempt safe methods
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		// Exempt Bearer auth (ambient credentials need CSRF check; Bearer doesn't)
		kind := AuthKind(r)
		if kind == authKindBearer {
			next.ServeHTTP(w, r)
			return
		}

		// Exempt loopback-only control requests
		if localControlRequest(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Check same-origin for cookie and identity auth
		if kind == authKindWebSession || kind == authKindTailscaleIdentity {
			if !isSameOrigin(r) {
				envelope.WriteAPIError(w, r, http.StatusForbidden, "forbidden", "CSRF_REJECTED",
					"cross-origin request rejected", nil)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// isSameOrigin checks whether the request Origin matches the Host, per CSRF defense.
func isSameOrigin(r *http.Request) bool {
	// Check Sec-Fetch-Site header first (preferred if available)
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" {
		return sfs == "same-origin"
	}

	// Fall back to Origin vs Host check
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // No Origin header: not a cross-site request
	}

	originURL, err := url.Parse(origin)
	if err != nil {
		return false // Malformed Origin: reject
	}

	// Extract hostname from Host header (remove port)
	hostHeader := r.Host
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		hostHeader = h
	}

	// Extract hostname from Origin URL
	originHost := originURL.Hostname()

	// Origins must match
	return originHost == hostHeader
}
