package httpd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/requestscope"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	agentsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/agent"
)

func TestLANControlBlockMarksRequestContext(t *testing.T) {
	seenLAN := false
	handler := lanControlBlock(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenLAN = requestscope.IsLAN(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/cues/cue-a/invoke", nil))
	if recorder.Code != http.StatusNoContent || !seenLAN {
		t.Fatalf("status=%d seenLAN=%v", recorder.Code, seenLAN)
	}
}

func TestMobileLANRejectsReassignedAddressWithSamePassword(t *testing.T) {
	calls := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusAccepted)
	})
	// The address formerly belonged to h_A; h_B now answers there and happens
	// to use the same connection password.
	m := NewMobileLAN(inner, "h_B", 0, nil, nil)
	m.SetPasswordHash(mobilebridge.HashPassword("same-secret"))
	request := func(expectedHostID, password string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/orchestrators/delegate", nil)
		req.Header.Set("Authorization", "Bearer "+password)
		if expectedHostID != "" {
			req.Header.Set("X-AO-Expected-Host-ID", expectedHostID)
		}
		recorder := httptest.NewRecorder()
		m.handler.ServeHTTP(recorder, req)
		return recorder
	}
	if got := request("h_A", "same-secret"); got.Code != http.StatusMisdirectedRequest || !strings.Contains(got.Body.String(), `"code":"HOST_ID_MISMATCH"`) || calls != 0 {
		t.Fatalf("stale h_A request: status=%d body=%q calls=%d", got.Code, got.Body.String(), calls)
	}
	if got := request("h_B", "same-secret"); got.Code != http.StatusAccepted || calls != 1 {
		t.Fatalf("matching h_B request: status=%d calls=%d", got.Code, calls)
	}
	if got := request("", "same-secret"); got.Code != http.StatusAccepted || calls != 2 {
		t.Fatalf("legacy request: status=%d calls=%d", got.Code, calls)
	}
	if got := request("h_A", "wrong"); got.Code != http.StatusUnauthorized || calls != 2 {
		t.Fatalf("unauthenticated request: status=%d calls=%d", got.Code, calls)
	}
}

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

func TestLANManagerTunnelOnlyBindsLoopback(t *testing.T) {
	m := NewMobileLAN(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), "h_test", 0, nil, nil)
	m.SetPasswordHash(mobilebridge.HashPassword("secret12"))
	if _, err := m.StartLoopback(0); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(context.Background())
	if addr := m.ln.Addr().(*net.TCPAddr); !addr.IP.IsLoopback() {
		t.Fatalf("tunnel-only listener bound %s, want loopback", addr)
	}
	if _, err := m.Start(0); err == nil {
		t.Fatal("LAN start silently reused a loopback-only listener")
	}
}

// TestLANManagerIdentityEndpoint verifies the unauthenticated identity probe
// works as expected for mobile pairing (ADR 0003).
func TestLANManagerIdentityEndpoint(t *testing.T) {
	inner := chi.NewRouter()
	inner.Get("/api/v1/identity", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"hostId":"test-host-id","apiVersion":1}`)
	})
	st := &authState{}
	st.setHash(mobilebridge.HashPassword("secret12"))
	m := NewLANManager(inner, st, 0, slog.Default(), nil)
	port, err := m.Start(0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop(context.Background())

	// Identity endpoint should be accessible without auth (ADR 0003)
	base := fmt.Sprintf("http://127.0.0.1:%d/api/v1/identity", port)
	resp, err := http.Get(base)
	if err != nil {
		t.Fatalf("identity probe failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("identity probe: got %d want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"hostId":"test-host-id","apiVersion":1}` {
		t.Fatalf("identity probe response: got %s want hostId", string(body))
	}

	// Other endpoints should still require auth
	authBase := fmt.Sprintf("http://127.0.0.1:%d/api/v1/anything", port)
	resp2, _ := http.Get(authBase)
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("other endpoint without auth: got %d want 401", resp2.StatusCode)
	}
}

// TestLANManagerBlocksLoopbackOnlyControlRoutes proves the LAN listener never
// serves /shutdown, /internal/*, /api/v1/mobile*, /api/v1/dev*,
// /api/v1/browser*, or the Codex credential routes under
// /api/v1/agents/codex/accounts* and /api/v1/agents/codex/account-switches* —
// even when the request carries a spoofed Host: 127.0.0.1
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
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		req, _ := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d/api/v1/sessions/ao-1/preview/server", port), nil)
		req.Host = "127.0.0.1"
		req.Header.Set("Authorization", "Bearer secret12")
		req.Header.Set("X-AO-Preview-Capability", "shell-preview-token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("LAN %s preview/server = %d, want 404", method, resp.StatusCode)
		}
	}
	{
		req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/api/v1/mobile/enable-lan-only", port), nil)
		req.Header.Set("Authorization", "Bearer secret12")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("LAN-only control route on LAN listener: got %d want 404", resp.StatusCode)
		}
	}

	// Paired clients can request the daemon's fixed harness installer.
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/api/v1/agents/cursor/install", port), nil)
	req.Host = "127.0.0.1"
	req.Header.Set("Authorization", "Bearer secret12")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("agent install request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("agent install: got %d want 200", resp.StatusCode)
	}
	for _, path := range []string{"/api/v1/agents/unknown/install", "/api/v1/agents/cursor/other/install"} {
		blockedInstall, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), nil)
		blockedInstall.Header.Set("Authorization", "Bearer secret12")
		blockedResp, err := http.DefaultClient.Do(blockedInstall)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if blockedResp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: got %d want 404", path, blockedResp.StatusCode)
		}
	}
	unauthenticatedInstall, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/api/v1/agents/cursor/install", port), nil)
	unauthenticatedResp, err := http.DefaultClient.Do(unauthenticatedInstall)
	if err != nil {
		t.Fatalf("unauthenticated agent install: %v", err)
	}
	if unauthenticatedResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated agent install: got %d want 401", unauthenticatedResp.StatusCode)
	}
	previewReq, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/v1/sessions/ao-1/preview/app/index.html", port), nil)
	previewReq.Header.Set("Authorization", "Bearer secret12")
	previewResp, err := http.DefaultClient.Do(previewReq)
	if err != nil {
		t.Fatalf("preview app: %v", err)
	}
	if previewResp.StatusCode != http.StatusOK {
		t.Fatalf("preview app: got %d want 200", previewResp.StatusCode)
	}

	// The read-only Codex model routes are not credential surfaces and must
	// stay reachable so mobile can list and refresh models.
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/agents/codex/models"},
		{http.MethodPost, "/api/v1/agents/codex/models/refresh"},
	} {
		req, _ := http.NewRequest(tc.method, fmt.Sprintf("http://127.0.0.1:%d%s", port, tc.path), nil)
		req.Header.Set("Authorization", "Bearer secret12")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: request failed: %v", tc.path, err)
		}
		if resp.StatusCode == http.StatusNotFound {
			t.Fatalf("%s: got 404, must not be blocked by the control-route filter", tc.path)
		}
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

	// Browsing a remote host for a project path is the whole point of fs/dirs,
	// so it must behave like every other data route: credential-gated, and
	// specifically NOT a loopback-only control route. A 404 here would be a
	// silent feature kill that no loopback-side test could catch.
	fsReq, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/v1/fs/dirs", port), nil)
	fsReq.Host = "127.0.0.1" // spoofed loopback Host, as above
	fsReq.Header.Set("Authorization", "Bearer secret12")
	fsResp, err := http.DefaultClient.Do(fsReq)
	if err != nil {
		t.Fatalf("fs/dirs: request failed: %v", err)
	}
	if fsResp.StatusCode == http.StatusNotFound {
		t.Fatal("/api/v1/fs/dirs: got 404 — remote folder browsing needs it reachable over the LAN")
	}
	fsNoAuth, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/v1/fs/dirs", port))
	if err != nil {
		t.Fatalf("fs/dirs unauthenticated: request failed: %v", err)
	}
	if fsNoAuth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/api/v1/fs/dirs unauthenticated: got %d want 401", fsNoAuth.StatusCode)
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

// lanFakeAgentCatalog is a controllers.AgentCatalog that records whether the
// real handler was actually reached. Only the Codex model/probe routes are
// exercised; the rest satisfy the interface.
type lanFakeAgentCatalog struct{ calls int }

func (c *lanFakeAgentCatalog) CachedReadiness(context.Context) (agentsvc.Readiness, error) {
	return agentsvc.Readiness{}, nil
}

func (c *lanFakeAgentCatalog) EnsureReadiness(context.Context, []string, domain.AgentReadinessPurpose) (agentsvc.Readiness, error) {
	return agentsvc.Readiness{}, nil
}

func (c *lanFakeAgentCatalog) List(context.Context) (agentsvc.Inventory, error) {
	return agentsvc.Inventory{}, nil
}

func (c *lanFakeAgentCatalog) Refresh(context.Context) (agentsvc.Inventory, error) {
	return agentsvc.Inventory{}, nil
}

func (c *lanFakeAgentCatalog) Probe(context.Context, string) (agentsvc.ProbeResult, error) {
	c.calls++
	return agentsvc.ProbeResult{}, nil
}

func (c *lanFakeAgentCatalog) Models(_ context.Context, agentID, _ string, _ bool) (ports.AgentModelCatalog, error) {
	c.calls++
	return ports.AgentModelCatalog{AgentID: agentID}, nil
}

func (c *lanFakeAgentCatalog) RevalidateModels(_ context.Context, agentID, _ string) (ports.AgentModelCatalog, error) {
	c.calls++
	return ports.AgentModelCatalog{AgentID: agentID}, nil
}

// TestLANListenerServesCodexModelRoutesFromRealRouter pins the actual bug: the
// LAN control block used to list the whole /api/v1/agents/codex prefix, so the
// model routes mobile calls answered 404 even though the router mounts them.
// A stub inner handler cannot prove that (it answers anything), so this drives
// the real AgentsController routes through the real LAN listener over a real
// socket and asserts the handler ran, while the credential routes stay blocked.
func TestLANListenerServesCodexModelRoutesFromRealRouter(t *testing.T) {
	catalog := &lanFakeAgentCatalog{}
	router := chi.NewRouter()
	router.Route("/api/v1", func(r chi.Router) {
		(&controllers.AgentsController{Catalog: catalog}).Register(r)
	})

	st := &authState{}
	st.setHash(mobilebridge.HashPassword("secret12"))
	m := NewLANManager(router, st, 0, slog.Default(), nil)
	port, err := m.Start(0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop(context.Background())

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/agents/codex/models?projectId=project%20one"},
		{http.MethodPost, "/api/v1/agents/codex/models/refresh"},
		{http.MethodPost, "/api/v1/agents/codex/probe"},
	} {
		req, _ := http.NewRequest(tc.method, fmt.Sprintf("http://127.0.0.1:%d%s", port, tc.path), nil)
		req.Header.Set("Authorization", "Bearer secret12")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: request failed: %v", tc.method, tc.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: got %d (%s) want 200 — LAN block must not swallow Codex model routes", tc.method, tc.path, resp.StatusCode, body)
		}
	}
	if catalog.calls != 3 {
		t.Fatalf("catalog calls = %d, want 3 — requests never reached the real handler", catalog.calls)
	}

	// The credential surface stays unreachable over LAN, even with a spoofed
	// loopback Host and valid auth.
	for _, path := range []string{
		"/api/v1/agents/codex/accounts",
		"/api/v1/agents/codex/accounts/events",
		"/api/v1/agents/codex/account-switches",
	} {
		req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), nil)
		req.Host = "127.0.0.1"
		req.Header.Set("Authorization", "Bearer secret12")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: request failed: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: got %d want 404", path, resp.StatusCode)
		}
	}
}
