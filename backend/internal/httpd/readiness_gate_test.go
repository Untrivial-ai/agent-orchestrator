package httpd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
)

type readinessProbeSessionService struct {
	controllers.SessionService
	listCalls atomic.Int32
	getCalls  atomic.Int32
}

func (s *readinessProbeSessionService) List(context.Context, sessionsvc.ListFilter) ([]domain.Session, error) {
	s.listCalls.Add(1)
	return nil, nil
}

func (s *readinessProbeSessionService) Get(_ context.Context, id domain.SessionID) (domain.Session, error) {
	s.getCalls.Add(1)
	return domain.Session{SessionRecord: domain.SessionRecord{ID: id}}, nil
}

const readinessPreviewHost = "ao-preview.orsxg5a.localhost"

type startupUnavailableResponse struct {
	Error     string `json:"error"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId"`
}

func TestStartupReadinessGateProtectsLoopbackStateUntilRecoveryCompletes(t *testing.T) {
	var ready atomic.Bool
	var failed atomic.Bool
	var shutdownCalls atomic.Int32
	sessions := &readinessProbeSessionService{}
	router := NewRouterWithControl(config.Config{}, discardLogger(), nil, APIDeps{
		Sessions: sessions,
		HostID:   "host-readiness-test",
	}, ControlDeps{
		RequestShutdown: func() { shutdownCalls.Add(1) },
		IsReady:         ready.Load,
		StartupFailed:   failed.Load,
	})

	assertStartupUnavailable(t, router, http.MethodGet, "/api/v1/sessions",
		startupRecoveryPendingCode, startupRecoveryPendingMessage, "")
	assertStartupUnavailable(t, router, http.MethodPost, "/api/v1/sessions",
		startupRecoveryPendingCode, startupRecoveryPendingMessage, `{}`)
	assertStartupUnavailable(t, router, http.MethodGet, "/mux",
		startupRecoveryPendingCode, startupRecoveryPendingMessage, "")
	if got := sessions.listCalls.Load(); got != 0 {
		t.Fatalf("session list handler calls while startup recovery is pending = %d, want 0", got)
	}

	for _, path := range []string{"/healthz", "/api/v1/identity"} {
		rec := serveRequest(router, http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s during startup recovery = %d, want 200", path, rec.Code)
		}
	}
	readyProbe := serveRequest(router, http.MethodGet, "/readyz", "")
	if readyProbe.Code != http.StatusServiceUnavailable || !strings.Contains(readyProbe.Body.String(), `"status":"starting"`) {
		t.Fatalf("GET /readyz during startup recovery = %d %s, want 503 starting", readyProbe.Code, readyProbe.Body.String())
	}
	shutdown := serveRequest(router, http.MethodPost, "/shutdown", "")
	if shutdown.Code != http.StatusAccepted || shutdownCalls.Load() != 1 {
		t.Fatalf("POST /shutdown during startup recovery = %d calls=%d, want 202 calls=1", shutdown.Code, shutdownCalls.Load())
	}

	// Exact control-looking paths on a preview origin still represent preview
	// requests until recovery succeeds. Path-only exemptions would let the
	// preview middleware perform a session lookup before chi selects the health,
	// readiness, identity, or shutdown route.
	for _, target := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/healthz"},
		{method: http.MethodGet, path: "/readyz"},
		{method: http.MethodGet, path: "/api/v1/identity"},
		{method: http.MethodPost, path: "/shutdown"},
	} {
		assertStartupUnavailableAtHost(t, router, target.method, target.path,
			startupRecoveryPendingCode, startupRecoveryPendingMessage, "", readinessPreviewHost)
	}
	if got := sessions.getCalls.Load(); got != 0 {
		t.Fatalf("preview-origin session lookups while startup recovery is pending = %d, want 0", got)
	}

	// Exemptions are exact in both verb and path.
	assertStartupUnavailable(t, router, http.MethodPost, "/api/v1/identity",
		startupRecoveryPendingCode, startupRecoveryPendingMessage, "")
	assertStartupUnavailable(t, router, http.MethodGet, "/api/v1/identity/nested",
		startupRecoveryPendingCode, startupRecoveryPendingMessage, "")
	assertStartupUnavailable(t, router, http.MethodGet, "/shutdown",
		startupRecoveryPendingCode, startupRecoveryPendingMessage, "")

	failed.Store(true)
	assertStartupUnavailable(t, router, http.MethodGet, "/api/v1/sessions",
		startupRecoveryFailureCode, startupRecoveryFailureMessage, "")
	assertStartupUnavailableAtHost(t, router, http.MethodGet, "/api/v1/identity",
		startupRecoveryFailureCode, startupRecoveryFailureMessage, "", readinessPreviewHost)
	if got := sessions.listCalls.Load(); got != 0 {
		t.Fatalf("session list handler calls after terminal startup failure = %d, want 0", got)
	}

	ready.Store(true)
	assertStartupUnavailable(t, router, http.MethodGet, "/api/v1/sessions",
		startupRecoveryFailureCode, startupRecoveryFailureMessage, "")
	failed.Store(false)
	rec := serveRequest(router, http.MethodGet, "/api/v1/sessions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/sessions after startup recovery = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if got := sessions.listCalls.Load(); got != 1 {
		t.Fatalf("session list handler calls after startup recovery = %d, want 1", got)
	}

	// Once ready, the reserved identity path must still reach the identity
	// controller even when a caller supplies a preview-origin Host.
	identity := serveRequestAtHost(router, http.MethodGet, "/api/v1/identity", "", readinessPreviewHost)
	if identity.Code != http.StatusOK || !strings.Contains(identity.Body.String(), `"hostId":"host-readiness-test"`) {
		t.Fatalf("preview-host GET /api/v1/identity after recovery = %d %s, want identity response", identity.Code, identity.Body.String())
	}
	if got := sessions.getCalls.Load(); got != 0 {
		t.Fatalf("preview-origin session lookups through reserved identity route = %d, want 0", got)
	}
}

func TestStartupReadinessGateProtectsAuthenticatedLANAPIAndPreservesIdentityProbe(t *testing.T) {
	var ready atomic.Bool
	var failed atomic.Bool
	sessions := &readinessProbeSessionService{}
	router := NewRouterWithControl(config.Config{}, discardLogger(), nil, APIDeps{
		Sessions: sessions,
		HostID:   "host-readiness-test",
	}, ControlDeps{
		IsReady:       ready.Load,
		StartupFailed: failed.Load,
	})
	state := &authState{}
	state.setHash(mobilebridge.HashPassword("secret12"))
	lan := NewLANManager(router, state, 0, discardLogger(), nil)
	port, err := lan.Start(0)
	if err != nil {
		t.Fatalf("start LAN listener: %v", err)
	}
	defer func() {
		if err := lan.Stop(context.Background()); err != nil {
			t.Errorf("stop LAN listener: %v", err)
		}
	}()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	identity := doLANRequest(t, http.MethodGet, base+"/api/v1/identity", "")
	if identity.StatusCode != http.StatusOK {
		identity.Body.Close()
		t.Fatalf("unauthenticated LAN identity probe during recovery = %d, want 200", identity.StatusCode)
	}
	identity.Body.Close()

	// The Host header is client-controlled on the LAN listener. Before recovery,
	// a preview-looking Host must not turn the unauthenticated identity exemption
	// into preview dispatch.
	previewIdentity := doLANRequestAtHost(t, http.MethodGet, base+"/api/v1/identity", "", readinessPreviewHost)
	if previewIdentity.StatusCode != http.StatusServiceUnavailable {
		previewIdentity.Body.Close()
		t.Fatalf("unauthenticated LAN preview-host identity during recovery = %d, want 503", previewIdentity.StatusCode)
	}
	var previewPending startupUnavailableResponse
	if err := json.NewDecoder(previewIdentity.Body).Decode(&previewPending); err != nil {
		previewIdentity.Body.Close()
		t.Fatalf("decode unauthenticated LAN preview-host identity response: %v", err)
	}
	previewIdentity.Body.Close()
	if previewPending.Code != startupRecoveryPendingCode {
		t.Fatalf("unauthenticated LAN preview-host identity code = %q, want %q", previewPending.Code, startupRecoveryPendingCode)
	}
	if got := sessions.getCalls.Load(); got != 0 {
		t.Fatalf("LAN preview-origin session lookups during recovery = %d, want 0", got)
	}

	unauthenticated := doLANRequest(t, http.MethodGet, base+"/api/v1/sessions", "")
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		unauthenticated.Body.Close()
		t.Fatalf("unauthenticated LAN state request during recovery = %d, want 401", unauthenticated.StatusCode)
	}
	unauthenticated.Body.Close()

	assertLANStartupUnavailable(t, base+"/api/v1/sessions",
		startupRecoveryPendingCode, startupRecoveryPendingMessage)
	if got := sessions.listCalls.Load(); got != 0 {
		t.Fatalf("LAN session list handler calls while startup recovery is pending = %d, want 0", got)
	}

	// The auth and readiness exemptions are both GET + exact path. Neither a
	// nested path nor another verb may inherit the unauthenticated identity probe.
	for _, target := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/api/v1/identity"},
		{method: http.MethodGet, path: "/api/v1/identity/nested"},
	} {
		resp := doLANRequest(t, target.method, base+target.path, "")
		if resp.StatusCode != http.StatusUnauthorized {
			resp.Body.Close()
			t.Fatalf("unauthenticated LAN %s %s = %d, want 401", target.method, target.path, resp.StatusCode)
		}
		resp.Body.Close()
	}

	health := doLANRequest(t, http.MethodGet, base+"/healthz", "secret12")
	if health.StatusCode != http.StatusOK {
		health.Body.Close()
		t.Fatalf("authenticated LAN GET /healthz during recovery = %d, want 200", health.StatusCode)
	}
	health.Body.Close()

	failed.Store(true)
	assertLANStartupUnavailable(t, base+"/api/v1/sessions",
		startupRecoveryFailureCode, startupRecoveryFailureMessage)

	failed.Store(false)
	ready.Store(true)
	previewIdentity = doLANRequestAtHost(t, http.MethodGet, base+"/api/v1/identity", "", readinessPreviewHost)
	var identityBody map[string]any
	if err := json.NewDecoder(previewIdentity.Body).Decode(&identityBody); err != nil {
		previewIdentity.Body.Close()
		t.Fatalf("decode ready LAN preview-host identity response: %v", err)
	}
	previewIdentity.Body.Close()
	if previewIdentity.StatusCode != http.StatusOK || identityBody["hostId"] != "host-readiness-test" {
		t.Fatalf("unauthenticated ready LAN preview-host identity = %d %+v, want identity controller response", previewIdentity.StatusCode, identityBody)
	}
	if got := sessions.getCalls.Load(); got != 0 {
		t.Fatalf("LAN preview-origin session lookups through identity route = %d, want 0", got)
	}
	resp := doLANRequest(t, http.MethodGet, base+"/api/v1/sessions", "secret12")
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		t.Fatalf("authenticated LAN state request after recovery = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	if got := sessions.listCalls.Load(); got != 1 {
		t.Fatalf("LAN session list handler calls after startup recovery = %d, want 1", got)
	}
}

func assertStartupUnavailable(
	t *testing.T,
	h http.Handler,
	method string,
	path string,
	wantCode string,
	wantMessage string,
	body string,
) {
	t.Helper()
	assertStartupUnavailableAtHost(t, h, method, path, wantCode, wantMessage, body, "127.0.0.1")
}

func assertStartupUnavailableAtHost(
	t *testing.T,
	h http.Handler,
	method string,
	path string,
	wantCode string,
	wantMessage string,
	body string,
	host string,
) {
	t.Helper()
	rec := serveRequestAtHost(h, method, path, body, host)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("%s %s = %d body=%s, want 503", method, path, rec.Code, rec.Body.String())
	}
	var got startupUnavailableResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode %s %s response: %v", method, path, err)
	}
	if got.Error != "unavailable" || got.Code != wantCode || got.Message != wantMessage || got.RequestID == "" {
		t.Fatalf("%s %s response = %+v, want unavailable/%s/%q with request id", method, path, got, wantCode, wantMessage)
	}
}

func assertLANStartupUnavailable(t *testing.T, url, wantCode, wantMessage string) {
	t.Helper()
	resp := doLANRequest(t, http.MethodGet, url, "secret12")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("authenticated LAN GET %s = %d, want 503", url, resp.StatusCode)
	}
	var got startupUnavailableResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode authenticated LAN GET %s response: %v", url, err)
	}
	if got.Error != "unavailable" || got.Code != wantCode || got.Message != wantMessage || got.RequestID == "" {
		t.Fatalf("authenticated LAN GET %s response = %+v, want unavailable/%s/%q with request id", url, got, wantCode, wantMessage)
	}
}

func serveRequest(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	return serveRequestAtHost(h, method, path, body, "127.0.0.1")
}

func serveRequestAtHost(h http.Handler, method, path, body, host string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func doLANRequest(t *testing.T, method, url, password string) *http.Response {
	return doLANRequestAtHost(t, method, url, password, "")
}

func doLANRequestAtHost(t *testing.T, method, url, password, host string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, http.NoBody)
	if err != nil {
		t.Fatalf("new LAN request: %v", err)
	}
	if password != "" {
		req.Header.Set("Authorization", "Bearer "+password)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("LAN request: %v", err)
	}
	return resp
}
