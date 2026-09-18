package httpd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	agentsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/agent"
)

func TestLANManagerAuthGatesSharedHandler(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	})
	st := &authState{}
	st.setHash(mobilebridge.HashPassword("secret12"))
	m := NewLANManager(inner, st, 0, slog.Default()) // port 0 → ephemeral
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
// /api/v1/browser*, or other loopback-only control prefixes — even when the
// request carries a spoofed Host: 127.0.0.1 and valid LAN auth, since gating on
// Host alone (localControlRequest) is what let a LAN client reach these routes.
func TestLANManagerBlocksLoopbackOnlyControlRoutes(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	})
	st := &authState{}
	st.setHash(mobilebridge.HashPassword("secret12"))
	m := NewLANManager(inner, st, 0, slog.Default())
	port, err := m.Start(0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop(context.Background())

	blocked := []string{
		"/shutdown",
		"/internal/anything",
		"/api/v1/mobile/status",
		"/api/v1/mobile/devices",
		"/api/v1/mobile/devices/i1",
		"/api/v1/dev/import-projects",
		"/api/v1/browser/status",
		"/api/v1/desktop/sessions/ao-1/workspace",
		"/api/v1/system/install/tmux",
		"/api/v1/sessions/ao-1/preview/server",
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
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/api/v1/agents/opencode/install", port), nil)
	req.Host = "127.0.0.1"
	req.Header.Set("Authorization", "Bearer secret12")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("agent install request failed: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("agent install: got %d want 404", resp.StatusCode)
	}

	// The read-only model routes are not control surfaces and must stay
	// reachable so mobile can list and refresh models.
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/agents/opencode/models"},
		{http.MethodPost, "/api/v1/agents/opencode/models/refresh"},
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

}

func TestLANManagerStartStopIdempotent(t *testing.T) {
	m := NewLANManager(http.NotFoundHandler(), &authState{}, 0, slog.Default())
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
	m := NewLANManager(inner, st, 0, slog.Default())
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

// TestLANListenerServesAgentModelRoutesFromRealRouter pins the actual bug: the
// LAN control block used to list the whole /api/v1/agents/{agent} prefix, so the
// model routes mobile calls answered 404 even though the router mounts them. A
// stub inner handler cannot prove that (it answers anything), so this drives the
// real AgentsController routes through the real LAN listener over a real socket
// and asserts the handler ran, while a mounted route under a blocked control
// prefix stays unreachable.
func TestLANListenerServesAgentModelRoutesFromRealRouter(t *testing.T) {
	catalog := &lanFakeAgentCatalog{}
	router := chi.NewRouter()
	router.Route("/api/v1", func(r chi.Router) {
		(&controllers.AgentsController{Catalog: catalog}).Register(r)
	})
	// A mounted route beneath a loopback-only control prefix. The LAN block must
	// answer 404 for it even though the router would otherwise serve it.
	router.Get("/api/v1/browser/status", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "ok")
	})

	st := &authState{}
	st.setHash(mobilebridge.HashPassword("secret12"))
	m := NewLANManager(router, st, 0, slog.Default())
	port, err := m.Start(0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop(context.Background())

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/agents/opencode/models?projectId=project%20one"},
		{http.MethodPost, "/api/v1/agents/opencode/models/refresh"},
		{http.MethodPost, "/api/v1/agents/opencode/probe"},
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
			t.Fatalf("%s %s: got %d (%s) want 200 — LAN block must not swallow agent model routes", tc.method, tc.path, resp.StatusCode, body)
		}
	}
	if catalog.calls != 3 {
		t.Fatalf("catalog calls = %d, want 3 — requests never reached the real handler", catalog.calls)
	}

	// A mounted route under a blocked control prefix stays unreachable over LAN,
	// even with a spoofed loopback Host and valid auth.
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/v1/browser/status", port), nil)
	req.Host = "127.0.0.1"
	req.Header.Set("Authorization", "Bearer secret12")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("browser status: request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/api/v1/browser/status: got %d want 404 — LAN block must hide control routes", resp.StatusCode)
	}
}
