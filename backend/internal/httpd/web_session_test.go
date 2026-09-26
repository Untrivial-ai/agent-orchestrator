package httpd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
	"github.com/aoagents/agent-orchestrator/backend/internal/websession"
)

// webTestFixture wires a LANManager the same way the daemon does: a shared
// authState + lockout across both the router's login route and authMiddleware,
// through the real chi router (so tests exercise the actual middleware chain,
// not a hand-assembled shortcut). Requests must be sent via HTTP to a bound
// listener (not passed directly to a handler) so chi's own RealIP middleware
// runs in its real position, after authMiddleware/capturePeerMiddleware.
type webTestFixture struct {
	t        *testing.T
	lan      *LANManager
	port     int
	password string
	store    *websession.Store
}

func newWebTestFixture(t *testing.T, opts ...func(*fixtureOpts)) *webTestFixture {
	t.Helper()

	o := fixtureOpts{
		password: "secret12",
		bindHost: "127.0.0.1",
	}
	for _, fn := range opts {
		fn(&o)
	}

	dir := t.TempDir()
	store, err := websession.NewStore(dir+"/sessions", 24*time.Hour, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	state := NewAuthState()
	state.setHash(mobilebridge.HashPassword(o.password))
	lock := NewLoginLockout()

	trustXFF := isLoopbackHost(o.bindHost)
	webSession := NewWebSessionHandlers(state, lock, trustXFF, store)

	router := NewRouterWithControl(
		config.Config{},
		discardLogger(),
		nil,
		APIDeps{},
		ControlDeps{WebSession: webSession},
	)

	cfg := LANManagerConfig{
		DefaultPort:    0,
		BindHost:       o.bindHost,
		StrictPort:     false,
		SessionStore:   store,
		IdentityConfig: o.identity,
		AuthState:      state,
		Lockout:        lock,
	}

	lan := NewMobileLAN(router, cfg, discardLogger(), nil)
	port, err := lan.Start(0)
	if err != nil {
		t.Fatalf("lan.Start: %v", err)
	}
	t.Cleanup(func() { _ = lan.Stop(context.Background()) })

	return &webTestFixture{t: t, lan: lan, port: port, password: o.password, store: store}
}

type fixtureOpts struct {
	password string
	bindHost string
	identity *IdentityAuthConfig
}

func withIdentity(cfg *IdentityAuthConfig) func(*fixtureOpts) {
	return func(o *fixtureOpts) { o.identity = cfg }
}

func withBindHost(host string) func(*fixtureOpts) {
	return func(o *fixtureOpts) { o.bindHost = host }
}

func (f *webTestFixture) url(path string) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", f.port, path)
}

func (f *webTestFixture) do(req *http.Request) *http.Response {
	f.t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("request %s %s failed: %v", req.Method, req.URL, err)
	}
	return resp
}

// ===== Test 1: cookie login/logout round-trip =====

func TestWebSessionLoginLogoutRoundTrip(t *testing.T) {
	f := newWebTestFixture(t)

	// Login with correct password.
	loginReq, _ := http.NewRequest(http.MethodPost, f.url("/api/v1/web/session"),
		jsonBody(controllers.WebSessionLoginRequest{Password: f.password}))
	loginReq.Header.Set("Content-Type", "application/json")
	loginResp := f.do(loginReq)
	if loginResp.StatusCode != http.StatusNoContent {
		t.Fatalf("login: got %d want 204", loginResp.StatusCode)
	}
	cookie := findSetCookie(loginResp, sessionCookieName)
	if cookie == nil || cookie.Value == "" {
		t.Fatal("login should set a non-empty ao_session cookie")
	}

	// Status check with the cookie reports authenticated.
	statusReq, _ := http.NewRequest(http.MethodGet, f.url("/api/v1/web/session"), nil)
	statusReq.AddCookie(cookie)
	statusResp := f.do(statusReq)
	var status controllers.WebSessionResponse
	if err := json.NewDecoder(statusResp.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !status.Authenticated {
		t.Fatal("should report authenticated=true with a valid cookie")
	}

	// The cookie also authenticates a normal API route.
	apiReq, _ := http.NewRequest(http.MethodGet, f.url("/api/v1/sessions"), nil)
	apiReq.AddCookie(cookie)
	apiResp := f.do(apiReq)
	if apiResp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("cookie should authenticate API routes, got 401")
	}

	// Logout revokes the session.
	logoutReq, _ := http.NewRequest(http.MethodDelete, f.url("/api/v1/web/session"), nil)
	logoutReq.AddCookie(cookie)
	logoutResp := f.do(logoutReq)
	if logoutResp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: got %d want 204", logoutResp.StatusCode)
	}

	// Status check with the now-revoked cookie reports unauthenticated.
	statusReq2, _ := http.NewRequest(http.MethodGet, f.url("/api/v1/web/session"), nil)
	statusReq2.AddCookie(cookie)
	statusResp2 := f.do(statusReq2)
	var status2 controllers.WebSessionResponse
	_ = json.NewDecoder(statusResp2.Body).Decode(&status2)
	if status2.Authenticated {
		t.Fatal("session should be unauthenticated after logout")
	}
}

// ===== Test 2: lockout that a rotating X-Forwarded-For cannot evade (ADR-0005 R5) =====

func TestLoginLockoutResistsRotatingXFF(t *testing.T) {
	// bindHost is loopback, so trustXFF would be true — but since these
	// requests go directly (not through a trusted proxy), the point is that
	// the REAL transport peer is what matters here; XFF rotation must not help
	// an attacker reach the LAN listener directly (untrusted network shape).
	// Use a non-loopback bind to exercise the "XFF is never trusted" branch,
	// which is the actual brute-force-target scenario described in ADR-0005 R5 for
	// an attacker who can reach the socket directly.
	f := newWebTestFixture(t, withBindHost("0.0.0.0"))

	failOnce := func(xff string) int {
		req, _ := http.NewRequest(http.MethodPost, f.url("/api/v1/web/session"),
			jsonBody(controllers.WebSessionLoginRequest{Password: "wrong"}))
		req.Header.Set("Content-Type", "application/json")
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		return f.do(req).StatusCode
	}

	// 5 failed attempts, each with a DIFFERENT spoofed X-Forwarded-For. All
	// arrive from the same real TCP peer (127.0.0.1, since the test client is
	// local), so an un-spoofable lockout must still trip after 5.
	for i := 0; i < 5; i++ {
		xff := "203.0.113." + strconv.Itoa(i+1)
		code := failOnce(xff)
		if code != http.StatusUnauthorized {
			t.Fatalf("attempt %d (xff=%s): got %d want 401", i+1, xff, code)
		}
	}

	// 6th attempt, yet another new XFF value, and even the CORRECT password:
	// must still be locked out, proving XFF rotation cannot evade the lockout.
	req, _ := http.NewRequest(http.MethodPost, f.url("/api/v1/web/session"),
		jsonBody(controllers.WebSessionLoginRequest{Password: f.password}))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "198.51.100.200")
	resp := f.do(req)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("locked out attempt with rotated XFF + correct password: got %d want 429", resp.StatusCode)
	}
}

// TestLoginLockoutTrustsXFFOnlyOnTrustedProxyPath verifies the OTHER half of
// ADR-0005 R5: when the LAN listener is loopback-bound (the trusted-proxy shape —
// a local proxy is the only path in, and it sets a genuine XFF), the lockout
// key uses XFF, so two DIFFERENT real devices proxied through it are locked
// out independently rather than sharing one lockout bucket keyed on the
// always-127.0.0.1 transport peer.
func TestLoginLockoutTrustsXFFOnlyOnTrustedProxyPath(t *testing.T) {
	f := newWebTestFixture(t, withBindHost("127.0.0.1"))

	failAs := func(xff string) int {
		req, _ := http.NewRequest(http.MethodPost, f.url("/api/v1/web/session"),
			jsonBody(controllers.WebSessionLoginRequest{Password: "wrong"}))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", xff)
		return f.do(req).StatusCode
	}

	// Lock out device A (5 failures).
	for i := 0; i < 5; i++ {
		if code := failAs("100.64.0.1"); code != http.StatusUnauthorized {
			t.Fatalf("device A attempt %d: got %d want 401", i+1, code)
		}
	}
	lockedReq, _ := http.NewRequest(http.MethodPost, f.url("/api/v1/web/session"),
		jsonBody(controllers.WebSessionLoginRequest{Password: f.password}))
	lockedReq.Header.Set("Content-Type", "application/json")
	lockedReq.Header.Set("X-Forwarded-For", "100.64.0.1")
	if resp := f.do(lockedReq); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("device A should be locked out: got %d want 429", resp.StatusCode)
	}

	// Device B, a DIFFERENT genuine tailnet IP, must be unaffected.
	deviceBReq, _ := http.NewRequest(http.MethodPost, f.url("/api/v1/web/session"),
		jsonBody(controllers.WebSessionLoginRequest{Password: f.password}))
	deviceBReq.Header.Set("Content-Type", "application/json")
	deviceBReq.Header.Set("X-Forwarded-For", "100.64.0.2")
	if resp := f.do(deviceBReq); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("device B should not share device A's lockout: got %d want 204", resp.StatusCode)
	}
}

// ===== Test 3: CSRF rejection under both cookie and identity auth =====

func TestCSRFRejectionUnderCookieAuth(t *testing.T) {
	f := newWebTestFixture(t)

	loginReq, _ := http.NewRequest(http.MethodPost, f.url("/api/v1/web/session"),
		jsonBody(controllers.WebSessionLoginRequest{Password: f.password}))
	loginReq.Header.Set("Content-Type", "application/json")
	loginResp := f.do(loginReq)
	cookie := findSetCookie(loginResp, sessionCookieName)
	if cookie == nil {
		t.Fatal("login should set a session cookie")
	}

	// Cross-origin state-changing request must be rejected.
	req, _ := http.NewRequest(http.MethodPost, f.url("/api/v1/sessions/cleanup"), jsonBody(map[string]any{}))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	req.AddCookie(cookie)
	resp := f.do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin cookie-authed POST: got %d want 403", resp.StatusCode)
	}

	// Same-origin (matching Host) request must NOT be CSRF-rejected.
	req2, _ := http.NewRequest(http.MethodPost, f.url("/api/v1/sessions/cleanup"), jsonBody(map[string]any{}))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Origin", fmt.Sprintf("http://127.0.0.1:%d", f.port))
	req2.AddCookie(cookie)
	resp2 := f.do(req2)
	if resp2.StatusCode == http.StatusForbidden {
		t.Fatal("same-origin cookie-authed POST must not be CSRF-rejected")
	}

	// Sec-Fetch-Site: same-origin also passes, regardless of Origin host.
	req3, _ := http.NewRequest(http.MethodPost, f.url("/api/v1/sessions/cleanup"), jsonBody(map[string]any{}))
	req3.Header.Set("Content-Type", "application/json")
	req3.Header.Set("Sec-Fetch-Site", "same-origin")
	req3.AddCookie(cookie)
	resp3 := f.do(req3)
	if resp3.StatusCode == http.StatusForbidden {
		t.Fatal("Sec-Fetch-Site: same-origin must not be CSRF-rejected")
	}
}

// TestCSRFAndCSWSHRejectionUnderIdentityAuth is the highest-risk test: identity
// auth has NO SameSite backstop (the Tailscale-User-Login header is attached by
// tailscaled to any request the browser makes, including one from a hostile
// page), so CSRF and CSWSH are the entire defense (ADR-0005 R7). This must be tested
// against identity auth specifically, not just cookies.
func TestCSRFRejectionUnderIdentityAuth(t *testing.T) {
	f := newWebTestFixture(t, withIdentity(&IdentityAuthConfig{
		Enabled:       true,
		AllowedLogins: map[string]bool{"operator@example.com": true},
	}))

	// Cross-origin state-changing request, authenticated purely by the
	// ambient Tailscale-User-Login header, must be rejected.
	req, _ := http.NewRequest(http.MethodPost, f.url("/api/v1/sessions/cleanup"), jsonBody(map[string]any{}))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Tailscale-User-Login", "operator@example.com")
	resp := f.do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin identity-authed POST: got %d want 403", resp.StatusCode)
	}

	// Same-origin identity-authed request must succeed past the CSRF gate.
	req2, _ := http.NewRequest(http.MethodPost, f.url("/api/v1/sessions/cleanup"), jsonBody(map[string]any{}))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Origin", fmt.Sprintf("http://127.0.0.1:%d", f.port))
	req2.Header.Set("Tailscale-User-Login", "operator@example.com")
	resp2 := f.do(req2)
	if resp2.StatusCode == http.StatusForbidden {
		t.Fatal("same-origin identity-authed POST must not be CSRF-rejected")
	}

}

// TestCSRFMiddlewareExemptsSafeMethods is a direct unit test of csrfMiddleware
// (bypassing the CORS layer, which independently rejects unlisted origins
// regardless of method) to isolate the CSRF-specific claim that GET/HEAD are
// exempt even cross-origin and even under identity auth.
func TestCSRFMiddlewareExemptsSafeMethods(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := csrfMiddleware(ok)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	req.Header.Set("Origin", "https://evil.example")
	req = req.WithContext(withAuthKind(req.Context(), authKindTailscaleIdentity))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code == http.StatusForbidden {
		t.Fatal("GET should not be CSRF-rejected even cross-origin under identity auth")
	}
}

// TestCSRFMiddlewareRejectsCrossOriginIdentityPOST is a direct unit test
// isolating the CSRF gate itself (no CORS interference) for the highest-risk
// case: a state-changing request authenticated ONLY by the ambient
// Tailscale-User-Login header, from a cross-origin page.
func TestCSRFMiddlewareRejectsCrossOriginIdentityPOST(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := csrfMiddleware(ok)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/cleanup", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Host = "vibebox.goose-marlin.ts.net:8443"
	req = req.WithContext(withAuthKind(req.Context(), authKindTailscaleIdentity))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin identity-authed POST: got %d want 403", w.Code)
	}

	// Same-origin (Origin host == Host) passes.
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/cleanup", nil)
	req2.Header.Set("Origin", "https://vibebox.goose-marlin.ts.net:8443")
	req2.Host = "vibebox.goose-marlin.ts.net:8443"
	req2 = req2.WithContext(withAuthKind(req2.Context(), authKindTailscaleIdentity))
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if w2.Code == http.StatusForbidden {
		t.Fatal("same-origin identity-authed POST must not be CSRF-rejected")
	}

	// Bearer auth is exempt from the CSRF gate entirely (native clients).
	req3 := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/cleanup", nil)
	req3.Header.Set("Origin", "https://evil.example")
	req3.Host = "vibebox.goose-marlin.ts.net:8443"
	req3 = req3.WithContext(withAuthKind(req3.Context(), authKindBearer))
	w3 := httptest.NewRecorder()
	h.ServeHTTP(w3, req3)
	if w3.Code == http.StatusForbidden {
		t.Fatal("Bearer-authed POST must not be CSRF-rejected")
	}
}

// ===== Test 4: cookie/identity-vs-Bearer origin behavior on /mux =====

func TestMuxOriginBehaviorByAuthKind(t *testing.T) {
	// Bearer: InsecureSkipVerify stays true (native clients send arbitrary Origin).
	req := httptest.NewRequest(http.MethodGet, "/mux", nil)
	req = req.WithContext(withAuthKind(req.Context(), authKindBearer))
	if AuthKind(req) != authKindBearer {
		t.Fatal("expected authKindBearer in context")
	}

	// No auth kind in context (loopback listener, no authMiddleware at all):
	// must behave like Bearer (insecure=true), so the desktop renderer's
	// cross-origin loopback connection is unaffected — this is the "silent
	// regression" trap: getting this inverted breaks the desktop app.
	reqNone := httptest.NewRequest(http.MethodGet, "/mux", nil)
	if AuthKind(reqNone) != authKindNone {
		t.Fatal("expected authKindNone with no auth middleware")
	}

	// Cookie and identity: same-origin must be enforced (insecure=false).
	reqCookie := httptest.NewRequest(http.MethodGet, "/mux", nil)
	reqCookie = reqCookie.WithContext(withAuthKind(reqCookie.Context(), authKindWebSession))
	if AuthKind(reqCookie) != authKindWebSession {
		t.Fatal("expected authKindWebSession in context")
	}

	reqIdentity := httptest.NewRequest(http.MethodGet, "/mux", nil)
	reqIdentity = reqIdentity.WithContext(withAuthKind(reqIdentity.Context(), authKindTailscaleIdentity))
	if AuthKind(reqIdentity) != authKindTailscaleIdentity {
		t.Fatal("expected authKindTailscaleIdentity in context")
	}
}

// ===== Test 5: Sec-WebSocket-Protocol: ao.bearer.<token> negotiation =====

func TestMuxSubprotocolBearerExtraction(t *testing.T) {
	cases := []struct {
		proto, want string
	}{
		{"ao.bearer.mytoken123", "mytoken123"},
		{"ao.bearer.", ""},
		{"other.protocol", ""},
		{"", ""},
	}
	for _, c := range cases {
		got := bearerFromSubprotocol(c.proto)
		if got != c.want {
			t.Errorf("bearerFromSubprotocol(%q) = %q, want %q", c.proto, got, c.want)
		}
	}
}

// ===== Test 6: session persistence across a restart =====

func TestSessionPersistenceAcrossDaemonRestart(t *testing.T) {
	dir := t.TempDir()
	sessDir := dir + "/sessions"

	store1, err := websession.NewStore(sessDir, 24*time.Hour, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	id, err := store1.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !store1.Validate(id) {
		t.Fatal("session should validate before restart")
	}

	// Simulate a daemon restart: a fresh Store instance over the same directory.
	store2, err := websession.NewStore(sessDir, 24*time.Hour, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("NewStore (restart): %v", err)
	}
	if !store2.Validate(id) {
		t.Fatal("session should survive a daemon restart")
	}
}

// ===== Test 7: session revocation on password regeneration =====

func TestSessionRevocationOnPasswordRegeneration(t *testing.T) {
	f := newWebTestFixture(t)

	loginReq, _ := http.NewRequest(http.MethodPost, f.url("/api/v1/web/session"),
		jsonBody(controllers.WebSessionLoginRequest{Password: f.password}))
	loginReq.Header.Set("Content-Type", "application/json")
	cookie := findSetCookie(f.do(loginReq), sessionCookieName)
	if cookie == nil {
		t.Fatal("login should set a session cookie")
	}

	// Simulate password regeneration: revoke all sessions (this is the
	// operation SetPasswordHash / regenerate must trigger in the CLI wiring).
	f.store.RevokeAll()

	statusReq, _ := http.NewRequest(http.MethodGet, f.url("/api/v1/web/session"), nil)
	statusReq.AddCookie(cookie)
	statusResp := f.do(statusReq)
	var status controllers.WebSessionResponse
	_ = json.NewDecoder(statusResp.Body).Decode(&status)
	if status.Authenticated {
		t.Fatal("session should be revoked after RevokeAll (password regeneration)")
	}
}

// ===== Test 8: identity auth refused for a login outside the allowlist =====

func TestIdentityAuthRefusedOutsideAllowlist(t *testing.T) {
	f := newWebTestFixture(t, withIdentity(&IdentityAuthConfig{
		Enabled:       true,
		AllowedLogins: map[string]bool{"operator@example.com": true},
	}))

	// Off-allowlist login: rejected.
	req, _ := http.NewRequest(http.MethodGet, f.url("/api/v1/sessions"), nil)
	req.Header.Set("Tailscale-User-Login", "attacker@github")
	resp := f.do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("off-allowlist login: got %d want 401", resp.StatusCode)
	}

	// On-allowlist login: accepted.
	req2, _ := http.NewRequest(http.MethodGet, f.url("/api/v1/sessions"), nil)
	req2.Header.Set("Tailscale-User-Login", "operator@example.com")
	resp2 := f.do(req2)
	if resp2.StatusCode == http.StatusUnauthorized {
		t.Fatalf("in-allowlist login should authenticate, got 401")
	}
}

func TestIdentityAuthEmptyAllowlistDeniesEveryone(t *testing.T) {
	f := newWebTestFixture(t, withIdentity(&IdentityAuthConfig{
		Enabled:       true,
		AllowedLogins: map[string]bool{}, // empty: deny everyone, never "any tailnet user"
	}))

	req, _ := http.NewRequest(http.MethodGet, f.url("/api/v1/sessions"), nil)
	req.Header.Set("Tailscale-User-Login", "operator@example.com")
	resp := f.do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("empty allowlist must deny everyone: got %d want 401", resp.StatusCode)
	}
}

// ===== Test 9: daemon refusing to start with identity trust on + non-loopback bind =====

func TestRefusesToStartIdentityTrustNonLoopback(t *testing.T) {
	dir := t.TempDir()
	store, _ := websession.NewStore(dir+"/sessions", 24*time.Hour, 90*24*time.Hour)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	cfg := LANManagerConfig{
		DefaultPort:  0,
		BindHost:     "0.0.0.0", // non-loopback
		SessionStore: store,
		IdentityConfig: &IdentityAuthConfig{
			Enabled:       true,
			AllowedLogins: map[string]bool{"operator@example.com": true},
		},
	}
	lan := NewMobileLAN(handler, cfg, discardLogger(), nil)
	_, err := lan.Start(0)
	if err == nil {
		t.Fatal("Start must refuse (return an error), not warn, with identity trust on + non-loopback bind")
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("error should explain the loopback-bind invariant: %v", err)
	}
	if lan.Running() {
		t.Fatal("listener must not be running after a refused start")
	}
}

func TestStartsWithIdentityTrustOnLoopback(t *testing.T) {
	dir := t.TempDir()
	store, _ := websession.NewStore(dir+"/sessions", 24*time.Hour, 90*24*time.Hour)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	cfg := LANManagerConfig{
		DefaultPort:  0,
		BindHost:     "127.0.0.1",
		SessionStore: store,
		IdentityConfig: &IdentityAuthConfig{
			Enabled:       true,
			AllowedLogins: map[string]bool{"operator@example.com": true},
		},
	}
	lan := NewMobileLAN(handler, cfg, discardLogger(), nil)
	port, err := lan.Start(0)
	if err != nil {
		t.Fatalf("Start should succeed with identity trust on loopback bind: %v", err)
	}
	defer lan.Stop(context.Background())
	if port == 0 {
		t.Fatal("expected a nonzero bound port")
	}
}

// ===== Test 10: unauthenticated navigation → 302 /login, API call → JSON 401 =====

func TestUnauthenticatedNavigationRedirectsAPIStays401(t *testing.T) {
	f := newWebTestFixture(t)

	// Document navigation with no credential.
	navReq, _ := http.NewRequest(http.MethodGet, f.url("/dashboard"), nil)
	navReq.Header.Set("Sec-Fetch-Mode", "navigate")
	noRedirectClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	navResp, err := noRedirectClient.Do(navReq)
	if err != nil {
		t.Fatalf("nav request: %v", err)
	}
	if navResp.StatusCode != http.StatusFound {
		t.Fatalf("unauthenticated navigation: got %d want 302", navResp.StatusCode)
	}
	if loc := navResp.Header.Get("Location"); loc != "/login" {
		t.Fatalf("redirect Location: got %q want /login", loc)
	}

	// API call (Accept: application/json, no Sec-Fetch-Mode) with no credential.
	apiReq, _ := http.NewRequest(http.MethodGet, f.url("/api/v1/sessions"), nil)
	apiReq.Header.Set("Accept", "application/json")
	apiResp := f.do(apiReq)
	if apiResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated API call: got %d want 401", apiResp.StatusCode)
	}
	ct := apiResp.Header.Get("Content-Type")
	if !strings.Contains(ct, "json") {
		t.Fatalf("API 401 should be JSON, got Content-Type %q", ct)
	}
	var body map[string]any
	if err := json.NewDecoder(apiResp.Body).Decode(&body); err != nil {
		t.Fatalf("API 401 body should decode as JSON: %v", err)
	}

	// /login itself is reachable unauthenticated.
	loginPageReq, _ := http.NewRequest(http.MethodGet, f.url("/login"), nil)
	loginPageResp := f.do(loginPageReq)
	if loginPageResp.StatusCode != http.StatusOK {
		t.Fatalf("/login should be reachable unauthenticated: got %d", loginPageResp.StatusCode)
	}
}

// ===== Test 11: lanControlBlock still 404s every blocked prefix =====

func TestLANControlBlockStillBlocksEveryPrefix(t *testing.T) {
	f := newWebTestFixture(t)

	loginReq, _ := http.NewRequest(http.MethodPost, f.url("/api/v1/web/session"),
		jsonBody(controllers.WebSessionLoginRequest{Password: f.password}))
	loginReq.Header.Set("Content-Type", "application/json")
	cookie := findSetCookie(f.do(loginReq), sessionCookieName)
	if cookie == nil {
		t.Fatal("login should set a session cookie")
	}

	blocked := []string{
		"/shutdown",
		"/internal/telemetry/cli-invoked",
		"/api/v1/mobile/status",
		"/api/v1/mobile/devices",
		"/api/v1/dev/import-projects",
		"/api/v1/browser/status",
		"/api/v1/system/install/tmux",
		"/api/v1/sessions/ao-1/preview/server",
	}
	for _, path := range blocked {
		req, _ := http.NewRequest(http.MethodGet, f.url(path), nil)
		req.AddCookie(cookie) // even authenticated, these must 404 on the LAN socket
		resp := f.do(req)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: got %d want 404 (authenticated LAN request must still be blocked)", path, resp.StatusCode)
		}
	}

	// A normal app route remains reachable (proves the filter isn't overbroad).
	okReq, _ := http.NewRequest(http.MethodGet, f.url("/api/v1/sessions"), nil)
	okReq.AddCookie(cookie)
	okResp := f.do(okReq)
	if okResp.StatusCode == http.StatusNotFound {
		t.Fatal("/api/v1/sessions must not be blocked by the control-route filter")
	}
}

// ===== Helpers =====

func jsonBody(v any) io.Reader {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return strings.NewReader(string(b))
}

func findSetCookie(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}
