package httpd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

func TestLANManagerAuthGatesSharedHandler(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	})
	st := &authState{}
	st.setHash(mobilebridge.HashPassword("secret12"))
	m := NewLANManager(inner, st, 0, slog.Default(), nil) // port 0 → ephemeral
	port, err := m.Start(0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop(context.Background())
	if !m.Running() || m.BoundPort() != port {
		t.Fatalf("running=%v boundPort=%d port=%d", m.Running(), m.BoundPort(), port)
	}

	base := fmt.Sprintf("http://127.0.0.1:%d/anything", port)
	// no auth → 401
	resp, _ := http.Get(base)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-auth: got %d want 401", resp.StatusCode)
	}
	// with auth → 200
	req, _ := http.NewRequest(http.MethodGet, base, nil)
	req.Header.Set("Authorization", "Bearer secret12")
	resp2, _ := http.DefaultClient.Do(req)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("auth: got %d want 200", resp2.StatusCode)
	}
}

// TestLANManagerBlocksLoopbackOnlyControlRoutes proves the LAN listener never
// serves /shutdown, /internal/*, /api/v1/mobile*, /api/v1/dev*,
// /api/v1/browser*, or /api/v1/agents/codex* — even when the request carries a spoofed Host: 127.0.0.1
// and valid LAN auth, since gating on Host alone (localControlRequest) is what
// let a LAN client reach these routes.
func TestLANManagerBlocksLoopbackOnlyControlRoutes(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	})
	st := &authState{}
	st.setHash(mobilebridge.HashPassword("secret12"))
	m := NewLANManager(inner, st, 0, slog.Default(), nil)
	port, err := m.Start(0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop(context.Background())

	blocked := []string{
		"/shutdown",
		"/internal/telemetry/cli-invoked",
		"/internal/agent-switch-observability/prepare-disable",
		"/internal/agent-switch-observability/apply-policy",
		"/api/v1/mobile/status",
		"/api/v1/mobile/devices",
		"/api/v1/mobile/devices/i1",
		"/api/v1/dev/import-projects",
		"/api/v1/browser/status",
		"/api/v1/desktop/sessions/ao-1/workspace",
		"/api/v1/system/install/tmux",
		"/api/v1/sessions/ao-1/preview/server",
		"/api/v1/agents/codex/accounts",
		"/api/v1/agents/codex/accounts/login-terminal",
		"/api/v1/agents/codex/accounts/login-operations/op-1/verify",
		"/api/v1/agents/codex/account-switches",
	}
	for _, path := range blocked {
		req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), nil)
		req.Host = "127.0.0.1" // spoofed loopback Host
		req.Header.Set("Authorization", "Bearer secret12")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: request failed: %v", path, err)
		}
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: got %d want 404 (Host-spoof + valid auth must not reach control routes)", path, resp.StatusCode)
		}
	}

	// Agent install mutations are loopback-only, while the adjacent GET
	// catalog/status routes remain available to authenticated mobile clients.
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/api/v1/agents/cursor/install", port), nil)
	req.Host = "127.0.0.1"
	req.Header.Set("Authorization", "Bearer secret12")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("agent install request failed: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("agent install: got %d want 404", resp.StatusCode)
	}

	// A normal app route must still be reachable through the LAN listener
	// (not swallowed by the control-route filter). Auth-gating, not the
	// control filter, decides its fate.
	req, _ = http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/v1/sessions", port), nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sessions: request failed: %v", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		t.Fatalf("/api/v1/sessions: got 404, should not be blocked by the control-route filter")
	}
	req, _ = http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/v1/agents", port), nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("agents: request failed: %v", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		t.Fatalf("/api/v1/agents: got 404, should not be blocked by the control-route filter")
	}

}

// --- a blocked route must not look like a missing one -----------------------
//
// The block list is a policy decision, so it must not be reported with the code
// that means "this daemon does not have that endpoint" — an operator chasing
// ROUTE_NOT_FOUND audits daemon builds and finds nothing wrong. These tests pin
// the distinction on the wire, and pin exactly how much of it an unauthenticated
// LAN caller can see.

// lanBlockFixture builds a LAN listener over a real chi router, so route
// matching (and therefore webUIBypass) behaves as it does in the daemon:
// /api/v1/dev/import-projects is a registered route, /api/v1/no-such-route is
// not, and unmatched paths get the router's locked JSON 404.
func lanBlockFixture(t *testing.T) (router http.Handler, port int) {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/api/v1/dev/import-projects", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "ok")
	})
	r.Get("/api/v1/sessions", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "ok")
	})
	r.NotFound(notFoundJSON)

	st := &authState{}
	st.setHash(mobilebridge.HashPassword("secret12"))
	m := NewLANManager(r, st, 0, slog.Default(), nil)
	port, err := m.Start(0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { m.Stop(context.Background()) })
	return r, port
}

// lanTestClient keeps no idle connections: these fixtures start and stop
// listeners on ephemeral ports within a single package run, and a pooled
// connection must never outlive the listener it was opened to.
var lanTestClient = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

// lanGet issues a GET to the LAN listener and returns the status and the decoded
// error envelope (zero-valued when the body is not one — raw keeps the bytes so
// a failure says what actually came back).
func lanGet(t *testing.T, port int, path, bearer string) (int, envelopeBody) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), nil)
	if err != nil {
		t.Fatalf("%s: new request: %v", path, err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := lanTestClient.Do(req)
	if err != nil {
		t.Fatalf("%s: request failed: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var body envelopeBody
	_ = json.Unmarshal(raw, &body)
	body.raw = string(raw)
	return resp.StatusCode, body
}

type envelopeBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	raw     string
}

func TestLANBlockedRouteIsNotReportedAsMissing(t *testing.T) {
	_, port := lanBlockFixture(t)

	// A loopback-only route over the LAN listener: still 404 (it is genuinely
	// not mounted on this listener), but the code says policy, not absence.
	status, body := lanGet(t, port, "/api/v1/dev/import-projects", "secret12")
	if status != http.StatusNotFound || body.Code != "ROUTE_LOOPBACK_ONLY" {
		t.Fatalf("blocked route: got %d %s, want 404 ROUTE_LOOPBACK_ONLY (body %s)", status, body.Code, body.raw)
	}
	if !strings.Contains(body.Message, "loopback listener only") {
		t.Fatalf("blocked route: message does not say why: %q", body.Message)
	}

	// A genuinely absent route keeps meaning exactly what it did before.
	status, body = lanGet(t, port, "/api/v1/no-such-route", "secret12")
	if status != http.StatusNotFound || body.Code != "ROUTE_NOT_FOUND" {
		t.Fatalf("absent route: got %d %s, want 404 ROUTE_NOT_FOUND (body %s)", status, body.Code, body.raw)
	}

	// An unrelated app route is untouched by either.
	if status, _ := lanGet(t, port, "/api/v1/sessions", "secret12"); status != http.StatusOK {
		t.Fatalf("/api/v1/sessions: got %d, want 200", status)
	}
}

// The loopback listener serves the shared router directly — lanControlBlock
// wraps the LAN-served handler only — so the same route is unchanged there.
func TestLoopbackStillServesBlockedRoute(t *testing.T) {
	router, _ := lanBlockFixture(t)
	srv := httptest.NewServer(router)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/dev/import-projects")
	if err != nil {
		t.Fatalf("loopback request: %v", err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(got) != "ok" {
		t.Fatalf("loopback: got %d %q, want 200 \"ok\"", resp.StatusCode, got)
	}
}

// What an unauthenticated LAN caller may learn. The answer is a constant of the
// AO build — "paths under this prefix are loopback-only" — and never which
// routes exist behind it: a registered blocked route and a path under the same
// prefix that no handler serves are byte-identical. Without this, a nicer error
// message would have turned the block into a route-table oracle for anyone who
// can reach the socket.
func TestUnauthenticatedLANCallerLearnsNoRouteTable(t *testing.T) {
	_, port := lanBlockFixture(t)

	const realPath, fakePath = "/api/v1/dev/import-projects", "/api/v1/dev/no-such-dev-route"
	realStatus, hit := lanGet(t, port, realPath, "")
	fakeStatus, fake := lanGet(t, port, fakePath, "")
	if realStatus != fakeStatus || hit.Code != fake.Code {
		t.Fatalf("blocked prefix leaks route existence: real %d %q vs absent %d %q",
			realStatus, hit.Code, fakeStatus, fake.Code)
	}
	if hit.Code != "ROUTE_LOOPBACK_ONLY" {
		t.Fatalf("unauthenticated blocked path: got code %q, want ROUTE_LOOPBACK_ONLY (body %s)", hit.Code, hit.raw)
	}
	// The messages differ only where they echo the caller's own path back, so
	// the answer carries nothing the caller did not already supply.
	if got := strings.Replace(fake.Message, fakePath, realPath, 1); got != hit.Message {
		t.Fatalf("blocked-path message is not a pure function of the requested path:\n real: %q\nabsent: %q",
			hit.Message, fake.Message)
	}

	// Outside the blocked prefixes nothing changed: no credential, no answer.
	if status, _ := lanGet(t, port, "/api/v1/no-such-route", ""); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated ordinary path: got %d, want 401", status)
	}
}

func TestLANManagerStartStopIdempotent(t *testing.T) {
	m := NewLANManager(http.NotFoundHandler(), &authState{}, 0, slog.Default(), nil)
	p1, _ := m.Start(0)
	p2, _ := m.Start(0) // idempotent — same port, no error
	if p1 != p2 {
		t.Fatalf("second start changed port: %d != %d", p1, p2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if m.Running() {
		t.Fatal("still running after stop")
	}
	_ = m.Stop(ctx) // second stop is a no-op
}

// End-to-end through the real LAN stack (lanControlBlock + authMiddleware +
// router): the identity probe answers without a credential, and nothing else
// does. The middleware unit tests cover the exemption in isolation; this covers
// the composition, which is where a wiring mistake would actually live.
func TestLANManagerServesIdentityProbeWithoutAPassword(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	})
	st := &authState{}
	st.setHash(mobilebridge.HashPassword("secret12"))
	m := NewLANManager(inner, st, 0, slog.Default(), nil)
	port, err := m.Start(0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop(context.Background())

	get := func(path string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), nil)
		resp, err := http.DefaultClient.Do(req) // deliberately no Authorization
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}

	if code := get("/api/v1/identity"); code != http.StatusOK {
		t.Errorf("unauthenticated GET /api/v1/identity got %d, want 200", code)
	}
	if code := get("/api/v1/sessions"); code != http.StatusUnauthorized {
		t.Errorf("unauthenticated GET /api/v1/sessions got %d, want 401", code)
	}
}
