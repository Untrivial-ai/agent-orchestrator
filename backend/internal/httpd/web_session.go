package httpd

import (
	"encoding/json"
	"net/http"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
	"github.com/aoagents/agent-orchestrator/backend/internal/websession"
)

// sessionCookieName is the web session cookie set on successful password login.
const sessionCookieName = "ao_session"

// webSessionHandlers implements POST/GET/DELETE /api/v1/web/session. It shares
// the LAN listener's authState and lockout with authMiddleware so a brute-force
// attempt against this route trips the same per-source lockout as any other
// failed credential, keyed the same un-spoofable way (ADR-0005 R5).
type webSessionHandlers struct {
	state    *authState
	lock     *lockout
	trustXFF bool
	store    *websession.Store
}

// newWebSessionHandlers builds the session-route handlers. store may be nil in
// configurations that never enable the web login path (identity-only, or the
// bridge disabled); the handlers then answer OK for GET (unauthenticated) but
// login/logout report the feature as unavailable.
func newWebSessionHandlers(state *authState, lock *lockout, trustXFF bool, store *websession.Store) *webSessionHandlers {
	return &webSessionHandlers{state: state, lock: lock, trustXFF: trustXFF, store: store}
}

// NewWebSessionHandlers is the exported constructor the daemon uses to build
// ControlDeps.WebSession. state and lock must be the SAME instances (via
// NewAuthState / NewLoginLockout) passed to NewLANManager's LANManagerConfig,
// so the shared-router login route and the LAN listener's authMiddleware agree
// on both the current password hash and the brute-force lockout state.
// trustXFF must be true only when bindHost is a loopback address (ADR-0005 R5) — it
// controls whether X-Forwarded-For is trusted for the lockout key.
func NewWebSessionHandlers(state *authState, lock *lockout, trustXFF bool, store *websession.Store) *webSessionHandlers {
	return newWebSessionHandlers(state, lock, trustXFF, store)
}

// Login handles POST /api/v1/web/session: verify the password, mint a session,
// and set the session cookie. Subject to the shared per-source lockout.
func (h *webSessionHandlers) Login(w http.ResponseWriter, r *http.Request) {
	key := lockoutKey(r, h.trustXFF)
	if h.lock.blocked(key) {
		envelope.WriteAPIError(w, r, http.StatusTooManyRequests, "too_many_requests", "LOCKED_OUT",
			"too many failed attempts; try again shortly", nil)
		return
	}

	var body controllers.WebSessionLoginRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Password == "" {
		h.lock.fail(key)
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON",
			"request body must be valid JSON with a non-empty password", nil)
		return
	}

	if !mobilebridge.PasswordMatches(h.state.currentHash(), body.Password) {
		h.lock.fail(key)
		envelope.WriteAPIError(w, r, http.StatusUnauthorized, "unauthorized", "BAD_PASSWORD",
			"missing or invalid connection password", nil)
		return
	}

	h.lock.reset(key)

	if h.store == nil {
		envelope.WriteAPIError(w, r, http.StatusServiceUnavailable, "unavailable", "SESSION_STORE_UNAVAILABLE",
			"web session login is not available", nil)
		return
	}

	id, err := h.store.Create()
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal", "SESSION_CREATE_FAILED",
			"failed to create session", nil)
		return
	}

	setSessionCookie(w, r, id)
	w.WriteHeader(http.StatusNoContent)
}

// Logout handles DELETE /api/v1/web/session: revoke the caller's session (if
// any) and clear the cookie.
func (h *webSessionHandlers) Logout(w http.ResponseWriter, r *http.Request) {
	if h.store != nil {
		if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
			h.store.Revoke(c.Value)
		}
	}
	clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// Status handles GET /api/v1/web/session: reports whether the current request
// carries a valid session cookie. Unauthenticated by design — a browser with no
// cookie must be able to ask "am I logged in?" without hitting a 401 first.
func (h *webSessionHandlers) Status(w http.ResponseWriter, r *http.Request) {
	authenticated := false
	if h.store != nil {
		if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
			authenticated = h.store.Validate(c.Value)
		}
	}
	envelope.WriteJSON(w, http.StatusOK, controllers.WebSessionResponse{Authenticated: authenticated})
}

// requestIsSecure reports whether the request arrived over TLS, directly or via
// a trusted proxy's X-Forwarded-Proto (a TLS-terminating proxy such as Caddy,
// nginx, or tailscale serve sets this header).
func requestIsSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return r.Header.Get("X-Forwarded-Proto") == "https"
}

// setSessionCookie sets the web session cookie per ADR-0005 R1: HttpOnly, SameSite=Strict,
// Path=/, and Secure when the request arrived over TLS (directly or via the
// trusted proxy).
func setSessionCookie(w http.ResponseWriter, r *http.Request, id string) {
	//nolint:gosec // G124: Secure IS set, computed via requestIsSecure(r) rather
	// than a literal — the linter's pattern match can't see that. Per ADR-0005 R1,
	// Secure must be true when the request arrived over TLS (or via the
	// trusted proxy's X-Forwarded-Proto) and MUST be false on a plain-HTTP
	// loopback deployment, where a literal Secure:true would silently break
	// the cookie (browsers refuse to send Secure cookies over http://).
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   requestIsSecure(r),
	})
}

// clearSessionCookie expires the session cookie on logout.
func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	//nolint:gosec // G124: see setSessionCookie — Secure is intentionally computed, not a literal.
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   requestIsSecure(r),
		MaxAge:   -1,
	})
}
