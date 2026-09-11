package controllers_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/agentauth"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
)

type fakeAgentAuthService struct {
	plans       []agentauth.Plan
	startResult agentauth.StartResult
	startErr    error
	startedID   string
	startCalls  int
}

func (f *fakeAgentAuthService) Plans(context.Context) []agentauth.Plan { return f.plans }

func (f *fakeAgentAuthService) Start(_ context.Context, agentID string) (agentauth.StartResult, error) {
	f.startCalls++
	f.startedID = agentID
	return f.startResult, f.startErr
}

func newAgentAuthTestServer(t *testing.T, svc *fakeAgentAuthService) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{AgentAuth: svc}, httpd.ControlDeps{}))
	t.Cleanup(server.Close)
	return server
}

func TestAgentAuthListReturnsDisplaySafePlans(t *testing.T) {
	svc := &fakeAgentAuthService{plans: []agentauth.Plan{{
		AgentID:          "codex",
		Action:           agentauth.ActionLogin,
		LaunchMode:       agentauth.LaunchTerminal,
		Available:        true,
		DisplayCommand:   "codex login",
		Guidance:         "Native browser flow",
		DocumentationURL: "https://developers.openai.com/codex/auth/",
	}}}
	server := newAgentAuthTestServer(t, svc)

	body, status, _ := doRequest(t, server, http.MethodGet, "/api/v1/agents/auth-plans", "")
	if status != http.StatusOK {
		t.Fatalf("GET auth-plans = %d, body=%s", status, body)
	}
	for _, want := range []string{`"agentId":"codex"`, `"action":"login"`, `"launchMode":"terminal"`, `"displayCommand":"codex login"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("body missing %s: %s", want, body)
		}
	}
	for _, forbidden := range []string{"argv", "initialInput"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("body exposed %q: %s", forbidden, body)
		}
	}
}

func TestAgentAuthStartReturnsTerminalHandle(t *testing.T) {
	createdAt := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	svc := &fakeAgentAuthService{startResult: agentauth.StartResult{
		AgentID:  "pi",
		Action:   agentauth.ActionLogin,
		Guidance: "Native Pi login flow",
		Terminal: shellterm.ShellTerminal{HandleID: "shellterm-auth", Title: "Log in to Pi", WorkingDir: "/tmp/ao", CreatedAt: createdAt},
	}}
	server := newAgentAuthTestServer(t, svc)

	body, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/agents/pi/auth", "")
	if status != http.StatusCreated {
		t.Fatalf("POST agent auth = %d, body=%s", status, body)
	}
	if svc.startedID != "pi" || svc.startCalls != 1 {
		t.Fatalf("Start calls = %d, id=%q", svc.startCalls, svc.startedID)
	}
	for _, want := range []string{`"agentId":"pi"`, `"handleId":"shellterm-auth"`, `"title":"Log in to Pi"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("body missing %s: %s", want, body)
		}
	}
}

func TestAgentAuthStartIgnoresRequestBody(t *testing.T) {
	for _, body := range []string{`{"argv":["rm","-rf"],"token":"secret"}`, `{not json`} {
		t.Run(body, func(t *testing.T) {
			svc := &fakeAgentAuthService{startResult: agentauth.StartResult{AgentID: "codex", Action: agentauth.ActionLogin}}
			server := newAgentAuthTestServer(t, svc)

			_, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/agents/codex/auth", body)
			if status != http.StatusCreated {
				t.Fatalf("POST agent auth = %d, want %d", status, http.StatusCreated)
			}
			if svc.startedID != "codex" || svc.startCalls != 1 {
				t.Fatalf("Start calls = %d, id=%q", svc.startCalls, svc.startedID)
			}
		})
	}
}

func TestAgentAuthErrorsPreserveEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name string
		code string
	}{
		{name: "unknown", code: "AGENT_AUTH_TARGET_UNKNOWN"},
		{name: "unavailable", code: "AGENT_AUTH_UNAVAILABLE"},
		{name: "instructions", code: "AGENT_AUTH_INSTRUCTIONS_ONLY"},
		{name: "documentation", code: "AGENT_AUTH_DOCUMENTATION_ONLY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeAgentAuthService{startErr: apierr.Invalid(tc.code, tc.name, nil)}
			server := newAgentAuthTestServer(t, svc)

			body, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/agents/codex/auth", "")
			if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"`+tc.code+`"`) || !strings.Contains(string(body), `"requestId":"`) {
				t.Fatalf("POST agent auth = %d, body=%s", status, body)
			}
		})
	}
}

func TestAgentAuthNotImplementedWithoutService(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{}, httpd.ControlDeps{}))
	defer server.Close()

	for _, request := range []struct{ method, path string }{
		{method: http.MethodGet, path: "/api/v1/agents/auth-plans"},
		{method: http.MethodPost, path: "/api/v1/agents/codex/auth"},
	} {
		body, status, _ := doRequest(t, server, request.method, request.path, "")
		if status != http.StatusNotImplemented || !strings.Contains(string(body), `"code":"NOT_IMPLEMENTED"`) {
			t.Fatalf("%s %s = %d, body=%s", request.method, request.path, status, body)
		}
	}
}
