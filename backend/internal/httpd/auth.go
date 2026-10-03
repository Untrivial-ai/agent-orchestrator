package httpd

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

// authState keeps previously valid hashes so a client retrying an old password
// after rotation cannot lock out a newly paired client behind the same address.
type authState struct{ hashes atomic.Pointer[authHashes] }
type authHashes struct {
	current string
	retired []string
}

func (a *authState) setHash(h string) {
	for {
		previous := a.hashes.Load()
		if previous != nil && previous.current == h {
			return
		}
		next := &authHashes{current: h}
		if previous != nil {
			next.retired = append(next.retired, previous.retired...)
			if previous.current != "" {
				next.retired = append(next.retired, previous.current)
			}
		}
		if a.hashes.CompareAndSwap(previous, next) {
			return
		}
	}
}
func (a *authState) currentHash() string {
	if hashes := a.hashes.Load(); hashes != nil {
		return hashes.current
	}
	return ""
}

func (a *authState) retiredPasswordMatches(token string) bool {
	if hashes := a.hashes.Load(); hashes != nil {
		for _, hash := range hashes.retired {
			if mobilebridge.PasswordMatches(hash, token) {
				return true
			}
		}
	}
	return false
}

// lockout throttles password guessing per source address.
type lockout struct {
	mu        sync.Mutex
	now       func() time.Time
	attempts  map[string]authAttempts
	lastSweep time.Time
}

type authAttempts struct {
	fails   int
	expires time.Time
}

func newLockout(now func() time.Time) *lockout {
	return &lockout{now: now, attempts: map[string]authAttempts{}}
}

func (l *lockout) blocked(src string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.attempts[src]
	if !ok {
		return false
	}
	if l.now().Before(a.expires) {
		return a.fails >= 5
	}
	delete(l.attempts, src)
	return false
}

func (l *lockout) fail(src string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastSweep) >= time.Minute {
		for key, a := range l.attempts {
			if !now.Before(a.expires) {
				delete(l.attempts, key)
			}
		}
		l.lastSweep = now
	}
	a := l.attempts[src]
	if !now.Before(a.expires) {
		a.fails = 0
	}
	a.fails++
	a.expires = now.Add(time.Minute)
	l.attempts[src] = a
}

func (l *lockout) reset(src string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, src)
}

func (l *lockout) resetAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attempts = map[string]authAttempts{}
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
// top-level navigation) or, ONLY on the preview-files route, the auth cookie (a
// preview page's subresource requests — images/CSS/JS — which the WebView issues
// without our header). Restricting the cookie to the preview-files path means it
// can never authenticate any other mobile endpoint even if a client sends it.
func connectionToken(r *http.Request) string {
	if t := bearerToken(r); t != "" {
		return t
	}
	if previewFilesCookiePath(r.URL.Path) != "" {
		if c, err := r.Cookie(authCookieName); err == nil {
			return c.Value
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

// authMiddleware authenticates LAN requests against the current connection
// password. connected, which may be nil, is notified of the source address of
// every request that authenticates; it exists so telemetry can observe that a
// phone actually reached this desktop, and it must not block the request, since
// it runs inline on every authenticated call.
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

func authMiddleware(state *authState, lock *lockout, connected *mobileConnectReporter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isIdentityProbe(r) {
				next.ServeHTTP(w, r)
				return
			}
			remoteSrc := sourceKey(r)
			src := remoteSrc
			// Cloudflared connects from loopback, so use its visitor IP for lockout
			// only. A LAN client must not be able to spoof its source with this header.
			if ip := net.ParseIP(remoteSrc); ip != nil && ip.IsLoopback() {
				if forwarded := r.Header.Values("CF-Connecting-IP"); len(forwarded) == 1 {
					if visitor := net.ParseIP(forwarded[0]); visitor != nil {
						src = visitor.String()
					}
				}
			}
			if lock.blocked(src) {
				envelope.WriteAPIError(w, r, http.StatusTooManyRequests, "too_many_requests", "LOCKED_OUT",
					"too many failed attempts; try again shortly", nil)
				return
			}
			tok := connectionToken(r)
			if mobilebridge.PasswordMatches(state.currentHash(), tok) {
				lock.reset(src)
				connected.report(remoteSrc)
				maybeSetPreviewAuthCookie(w, r, tok)
				next.ServeHTTP(w, r)
				return
			}
			if !state.retiredPasswordMatches(tok) {
				lock.fail(src)
			}
			envelope.WriteAPIError(w, r, http.StatusUnauthorized, "unauthorized", "BAD_PASSWORD",
				"missing or invalid connection password", nil)
		})
	}
}
