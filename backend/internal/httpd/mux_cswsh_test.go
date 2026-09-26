package httpd

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
	"github.com/aoagents/agent-orchestrator/backend/internal/terminal"
	"github.com/aoagents/agent-orchestrator/backend/internal/websession"
)

// muxCSWSHFixture stands up a real LAN listener (auth + csrf + control-block)
// with the terminal mux mounted, so CSWSH tests exercise the genuine upgrade
// path — coder/websocket's own same-origin check under InsecureSkipVerify:false
// — not just the InsecureSkipVerify value in isolation.
type muxCSWSHFixture struct {
	t        *testing.T
	lan      *LANManager
	port     int
	password string
	store    *websession.Store
}

func newMuxCSWSHFixture(t *testing.T, identity *IdentityAuthConfig) *muxCSWSHFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("PTY spawning not supported on Windows")
	}

	mgr := terminal.NewManager(&stubSource{argv: []string{"/bin/sh"}}, nil, discardLogger())
	t.Cleanup(mgr.Close)

	dir := t.TempDir()
	store, err := websession.NewStore(dir+"/sessions", 24*time.Hour, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	password := "secret12"
	state := NewAuthState()
	state.setHash(mobilebridge.HashPassword(password))
	lock := NewLoginLockout()

	webSession := NewWebSessionHandlers(state, lock, true, store)

	router := NewRouterWithControl(
		config.Config{},
		discardLogger(),
		mgr,
		APIDeps{},
		ControlDeps{WebSession: webSession},
	)

	cfg := LANManagerConfig{
		DefaultPort:    0,
		BindHost:       "127.0.0.1",
		SessionStore:   store,
		IdentityConfig: identity,
		AuthState:      state,
		Lockout:        lock,
	}
	lan := NewMobileLAN(router, cfg, discardLogger(), nil)
	port, err := lan.Start(0)
	if err != nil {
		t.Fatalf("lan.Start: %v", err)
	}
	t.Cleanup(func() { _ = lan.Stop(context.Background()) })

	return &muxCSWSHFixture{t: t, lan: lan, port: port, password: password, store: store}
}

func (f *muxCSWSHFixture) muxURL() string {
	return fmt.Sprintf("ws://127.0.0.1:%d/mux", f.port)
}

func (f *muxCSWSHFixture) login() *http.Cookie {
	f.t.Helper()
	loginReq, _ := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/api/v1/web/session", f.port),
		jsonBody(controllers.WebSessionLoginRequest{Password: f.password}))
	loginReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(loginReq)
	if err != nil {
		f.t.Fatalf("login: %v", err)
	}
	c := findSetCookie(resp, sessionCookieName)
	if c == nil {
		f.t.Fatal("login did not set a session cookie")
	}
	return c
}

// TestMuxCSWSHRejectsCrossOriginCookieUpgrade is the highest-risk CSWSH test:
// a cookie-authenticated WebSocket upgrade from a cross-origin page must be
// rejected by coder/websocket's own same-origin check (InsecureSkipVerify
// flips to false once AuthKind is cookie). This proves the escape hatch
// documented as void once a cookie exists on a network listener (ADR-0005 R2) is
// actually closed.
func TestMuxCSWSHRejectsCrossOriginCookieUpgrade(t *testing.T) {
	f := newMuxCSWSHFixture(t, nil)
	cookie := f.login()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := websocket.Dial(ctx, f.muxURL(), &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Cookie": {cookie.Name + "=" + cookie.Value},
			"Origin": {"https://evil.example"},
		},
	})
	if err == nil {
		t.Fatal("cross-origin cookie-authed /mux upgrade should be rejected")
	}
}

// TestMuxCSWSHAcceptsSameOriginCookieUpgrade proves the same-origin cookie
// path still works — CSWSH defense must not be so strict it breaks the
// legitimate same-origin browser client.
func TestMuxCSWSHAcceptsSameOriginCookieUpgrade(t *testing.T) {
	f := newMuxCSWSHFixture(t, nil)
	cookie := f.login()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, f.muxURL(), &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Cookie": {cookie.Name + "=" + cookie.Value},
			"Origin": {fmt.Sprintf("http://127.0.0.1:%d", f.port)},
		},
	})
	if err != nil {
		t.Fatalf("same-origin cookie-authed /mux upgrade should succeed: %v", err)
	}
	_ = c.Close(websocket.StatusNormalClosure, "test done")
}

// TestMuxCSWSHRejectsCrossOriginIdentityUpgrade is the identity-auth analogue:
// identity has NO SameSite backstop at all, so this same-origin check is the
// entire defense for it (ADR-0005 R7).
func TestMuxCSWSHRejectsCrossOriginIdentityUpgrade(t *testing.T) {
	f := newMuxCSWSHFixture(t, &IdentityAuthConfig{
		Enabled:       true,
		AllowedLogins: map[string]bool{"operator@example.com": true},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := websocket.Dial(ctx, f.muxURL(), &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Tailscale-User-Login": {"operator@example.com"},
			"Origin":               {"https://evil.example"},
		},
	})
	if err == nil {
		t.Fatal("cross-origin identity-authed /mux upgrade should be rejected")
	}
}

// TestMuxCSWSHAcceptsSameOriginIdentityUpgrade mirrors the cookie same-origin
// success case for identity auth.
func TestMuxCSWSHAcceptsSameOriginIdentityUpgrade(t *testing.T) {
	f := newMuxCSWSHFixture(t, &IdentityAuthConfig{
		Enabled:       true,
		AllowedLogins: map[string]bool{"operator@example.com": true},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, f.muxURL(), &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Tailscale-User-Login": {"operator@example.com"},
			"Origin":               {fmt.Sprintf("http://127.0.0.1:%d", f.port)},
		},
	})
	if err != nil {
		t.Fatalf("same-origin identity-authed /mux upgrade should succeed: %v", err)
	}
	_ = c.Close(websocket.StatusNormalClosure, "test done")
}

// TestMuxCSWSHPreservesBearerCrossOrigin proves Bearer auth is unaffected: the
// InsecureSkipVerify:true path native clients rely on must survive unchanged.
// Origin is pinned to "http://localhost", matching packages/mobile/lib/mux.ts's
// documented workaround (React Native's WebSocket auto-sets Origin to a
// non-loopback value that the daemon's PRE-EXISTING CORS guard 403s before the
// upgrade, so the mobile client pins a loopback Origin to pass it — that CORS
// gate is unrelated to and unaffected by this workstream's CSWSH change; what
// this test isolates is that coder/websocket's own same-origin check, which
// InsecureSkipVerify controls, still does not additionally reject it under
// Bearer auth once CORS has let the request through).
func TestMuxCSWSHPreservesBearerCrossOrigin(t *testing.T) {
	f := newMuxCSWSHFixture(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, f.muxURL(), &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization": {"Bearer " + f.password},
			"Origin":        {"http://localhost"}, // differs from the loopback:port Host — still must pass under Bearer
		},
	})
	if err != nil {
		t.Fatalf("cross-origin (vs Host) Bearer-authed /mux upgrade should succeed (native clients): %v", err)
	}
	_ = c.Close(websocket.StatusNormalClosure, "test done")
}

// TestMuxSubprotocolTokenAuthenticates verifies the ADR-0005 R3 escape hatch end to
// end: with NO Authorization header and NO cookie — exactly the situation a
// browser WebSocket client is in — a Sec-WebSocket-Protocol: ao.bearer.<token>
// upgrade must still authenticate through authMiddleware (not just negotiate a
// protocol after the fact) and the selected subprotocol is echoed back.
func TestMuxSubprotocolTokenAuthenticates(t *testing.T) {
	f := newMuxCSWSHFixture(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, f.muxURL(), &websocket.DialOptions{
		Subprotocols: []string{"ao.bearer." + f.password},
	})
	if err != nil {
		t.Fatalf("subprotocol-only upgrade should authenticate and succeed: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "test done")

	got := resp.Header.Get("Sec-WebSocket-Protocol")
	want := "ao.bearer." + f.password
	if got != want {
		t.Fatalf("selected subprotocol = %q, want %q", got, want)
	}
}

// TestMuxSubprotocolWrongTokenRejected proves the subprotocol path is a real
// credential check, not a rubber stamp: a wrong token must fail the upgrade.
func TestMuxSubprotocolWrongTokenRejected(t *testing.T) {
	f := newMuxCSWSHFixture(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := websocket.Dial(ctx, f.muxURL(), &websocket.DialOptions{
		Subprotocols: []string{"ao.bearer.wrongtoken"},
	})
	if err == nil {
		t.Fatal("subprotocol upgrade with a wrong token should be rejected")
	}
}
