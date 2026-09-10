package controllers_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	agentsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/agent"
)

type fakeAgentCatalog struct {
	inventory       agentsvc.Inventory
	refreshed       agentsvc.Inventory
	probed          agentsvc.ProbeResult
	err             error
	listCalls       int
	refreshCalls    int
	probeCalls      int
	probeAgent      string
	models          ports.AgentModelCatalog
	modelCalls      int
	modelAgent      string
	modelProject    string
	modelRefresh    bool
	revalidateCalls int
	readiness       agentsvc.Readiness
	readinessCalls  int
	ensureCalls     int
	ensureAgentIDs  []string
	ensurePurpose   domain.AgentReadinessPurpose
}

func (f *fakeAgentCatalog) CachedReadiness(context.Context) (agentsvc.Readiness, error) {
	f.readinessCalls++
	return f.readiness, f.err
}

func (f *fakeAgentCatalog) EnsureReadiness(_ context.Context, agentIDs []string, purpose domain.AgentReadinessPurpose) (agentsvc.Readiness, error) {
	f.ensureCalls++
	f.ensureAgentIDs = append([]string(nil), agentIDs...)
	f.ensurePurpose = purpose
	return f.readiness, f.err
}

func (f *fakeAgentCatalog) List(context.Context) (agentsvc.Inventory, error) {
	f.listCalls++
	return f.inventory, f.err
}

func (f *fakeAgentCatalog) Refresh(context.Context) (agentsvc.Inventory, error) {
	f.refreshCalls++
	if f.refreshed.Supported != nil {
		return f.refreshed, f.err
	}
	return f.inventory, f.err
}

func (f *fakeAgentCatalog) Probe(_ context.Context, agentID string) (agentsvc.ProbeResult, error) {
	f.probeCalls++
	f.probeAgent = agentID
	return f.probed, f.err
}

func (f *fakeAgentCatalog) Models(_ context.Context, agentID, projectID string, refresh bool) (ports.AgentModelCatalog, error) {
	f.modelCalls++
	f.modelAgent = agentID
	f.modelProject = projectID
	f.modelRefresh = refresh
	return f.models, f.err
}

func (f *fakeAgentCatalog) RevalidateModels(_ context.Context, agentID, projectID string) (ports.AgentModelCatalog, error) {
	f.revalidateCalls++
	f.modelAgent = agentID
	f.modelProject = projectID
	return f.models, f.err
}

func TestListAgents(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	catalog := &fakeAgentCatalog{inventory: agentsvc.Inventory{
		Supported:  []agentsvc.Info{{ID: "claude-code", Label: "Claude Code"}, {ID: "codex", Label: "Codex"}},
		Installed:  []agentsvc.Info{{ID: "codex", Label: "Codex"}},
		Authorized: []agentsvc.Info{{ID: "codex", Label: "Codex"}},
	}}
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		Agents: catalog,
	}, httpd.ControlDeps{}))
	defer srv.Close()

	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/agents", "")
	if status != http.StatusOK {
		t.Fatalf("GET /agents = %d, body=%s", status, body)
	}
	for _, want := range []string{`"supported"`, `"installed"`, `"authorized"`, `"id":"codex"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("body missing %s: %s", want, body)
		}
	}
	if strings.Contains(string(body), `"counts"`) {
		t.Fatalf("body includes removed counts field: %s", body)
	}
	if catalog.listCalls != 1 || catalog.refreshCalls != 0 {
		t.Fatalf("calls: list=%d refresh=%d, want list=1 refresh=0", catalog.listCalls, catalog.refreshCalls)
	}
}

func TestGetAgentReadinessUsesCachedSnapshot(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	catalog := &fakeAgentCatalog{readiness: agentsvc.Readiness{Agents: []domain.AgentReadinessSnapshot{{
		ID: "codex", Label: "Codex",
		Installation:       domain.AgentInstallationObservation{State: domain.AgentInstallationInstalled, Freshness: domain.AgentReadinessFresh, ReasonCode: domain.AgentReadinessReasonInstalled, Reason: "Codex is installed."},
		Authentication:     domain.AgentAuthenticationObservation{State: domain.AgentAuthenticationAuthorized, Freshness: domain.AgentReadinessFresh, ReasonCode: domain.AgentReadinessReasonAuthorized, Reason: "Codex appears signed in."},
		EffectiveReadiness: domain.AgentReadinessReady,
	}}}}
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{Agents: catalog}, httpd.ControlDeps{}))
	defer srv.Close()

	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/agents/readiness", "")
	if status != http.StatusOK {
		t.Fatalf("GET /agents/readiness = %d, body=%s", status, body)
	}
	for _, want := range []string{`"agents"`, `"state":"installed"`, `"state":"authorized"`, `"effectiveReadiness":"ready"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("body missing %s: %s", want, body)
		}
	}
	if catalog.readinessCalls != 1 || catalog.ensureCalls != 0 {
		t.Fatalf("calls: cached=%d ensure=%d", catalog.readinessCalls, catalog.ensureCalls)
	}
}

func TestEnsureAgentReadinessDecodesBatchAndPurpose(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	catalog := &fakeAgentCatalog{readiness: agentsvc.Readiness{Agents: []domain.AgentReadinessSnapshot{}}}
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{Agents: catalog}, httpd.ControlDeps{}))
	defer srv.Close()

	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/agents/readiness/ensure", `{"agentIds":["codex","codex"],"purpose":"launch"}`)
	if status != http.StatusOK {
		t.Fatalf("POST /agents/readiness/ensure = %d, body=%s", status, body)
	}
	if catalog.ensureCalls != 1 || catalog.ensurePurpose != domain.AgentReadinessPurposeLaunch || len(catalog.ensureAgentIDs) != 2 {
		t.Fatalf("ensure call = count %d ids %#v purpose %q", catalog.ensureCalls, catalog.ensureAgentIDs, catalog.ensurePurpose)
	}
}

func TestEnsureAgentReadinessRejectsInvalidJSON(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	catalog := &fakeAgentCatalog{}
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{Agents: catalog}, httpd.ControlDeps{}))
	defer srv.Close()

	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/agents/readiness/ensure", `{`)
	if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"INVALID_JSON"`) {
		t.Fatalf("invalid ensure = %d, body=%s", status, body)
	}
	if !strings.Contains(string(body), `"requestId":"`) {
		t.Fatalf("error envelope missing request id: %s", body)
	}
}

func TestEnsureAgentReadinessReturnsTypedValidationEnvelopes(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		Agents: agentsvc.NewWithAgents(nil),
	}, httpd.ControlDeps{}))
	defer srv.Close()

	for _, test := range []struct {
		name string
		body string
		code string
	}{
		{name: "unknown agent", body: `{"agentIds":["missing"],"purpose":"display"}`, code: "UNKNOWN_AGENT_ID"},
		{name: "invalid purpose", body: `{"purpose":"force"}`, code: "INVALID_READINESS_PURPOSE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/agents/readiness/ensure", test.body)
			if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"`+test.code+`"`) {
				t.Fatalf("ensure validation = %d, body=%s", status, body)
			}
			if !strings.Contains(string(body), `"requestId":"`) {
				t.Fatalf("error envelope missing request id: %s", body)
			}
		})
	}
}

func TestRefreshAgents(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	catalog := &fakeAgentCatalog{
		inventory: agentsvc.Inventory{Supported: []agentsvc.Info{{ID: "codex", Label: "Codex"}}},
		refreshed: agentsvc.Inventory{
			Supported:  []agentsvc.Info{{ID: "codex", Label: "Codex"}},
			Installed:  []agentsvc.Info{{ID: "codex", Label: "Codex"}},
			Authorized: []agentsvc.Info{{ID: "codex", Label: "Codex"}},
		},
	}
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		Agents: catalog,
	}, httpd.ControlDeps{}))
	defer srv.Close()

	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/agents/refresh", "")
	if status != http.StatusOK {
		t.Fatalf("POST /agents/refresh = %d, body=%s", status, body)
	}
	for _, want := range []string{`"supported"`, `"installed"`, `"authorized"`, `"id":"codex"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("body missing %s: %s", want, body)
		}
	}
	if catalog.listCalls != 0 || catalog.refreshCalls != 1 {
		t.Fatalf("calls: list=%d refresh=%d, want list=0 refresh=1", catalog.listCalls, catalog.refreshCalls)
	}
}

func TestProbeAgent(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	catalog := &fakeAgentCatalog{
		probed: agentsvc.ProbeResult{
			Agent:     agentsvc.Info{ID: "codex", Label: "Codex", AuthStatus: "authorized"},
			Supported: true,
			Installed: true,
		},
	}
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		Agents: catalog,
	}, httpd.ControlDeps{}))
	defer srv.Close()

	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/agents/codex/probe", "")
	if status != http.StatusOK {
		t.Fatalf("POST /agents/codex/probe = %d, body=%s", status, body)
	}
	for _, want := range []string{`"supported":true`, `"installed":true`, `"id":"codex"`, `"authStatus":"authorized"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("body missing %s: %s", want, body)
		}
	}
	if catalog.probeCalls != 1 || catalog.probeAgent != "codex" {
		t.Fatalf("probe calls=%d agent=%q, want one codex probe", catalog.probeCalls, catalog.probeAgent)
	}
}

func TestGetAndRefreshAgentModels(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tc := range []struct {
		name           string
		method         string
		path           string
		wantRefresh    bool
		wantRevalidate bool
	}{
		{name: "cached", method: http.MethodGet, path: "/api/v1/agents/codex/models?projectId=proj-1"},
		{name: "refresh", method: http.MethodPost, path: "/api/v1/agents/codex/models/refresh?projectId=proj-1", wantRefresh: true},
		{name: "revalidate", method: http.MethodPost, path: "/api/v1/agents/codex/models/refresh?projectId=proj-1&revalidate=true", wantRevalidate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := &fakeAgentCatalog{models: ports.AgentModelCatalog{
				AgentID:          "codex",
				SelectionMode:    ports.ModelSelectionCatalog,
				Models:           []ports.AgentModelInfo{{ID: "gpt-5.6-sol", Label: "GPT-5.6 Sol"}},
				CustomModelEntry: ports.CustomModelEntryDirect,
				AllowCustom:      true,
				Source:           "official-catalog",
			}}
			srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{Agents: catalog}, httpd.ControlDeps{}))
			defer srv.Close()

			body, status, _ := doRequest(t, srv, tc.method, tc.path, "")
			if status != http.StatusOK {
				t.Fatalf("%s %s = %d, body=%s", tc.method, tc.path, status, body)
			}
			for _, want := range []string{`"agentId":"codex"`, `"selectionMode":"catalog"`, `"customModelEntry":"direct"`, `"allowCustom":true`, `"id":"gpt-5.6-sol"`} {
				if !strings.Contains(string(body), want) {
					t.Fatalf("body missing %s: %s", want, body)
				}
			}
			wantModelCalls := 1
			if tc.wantRevalidate {
				wantModelCalls = 0
			}
			if catalog.modelCalls != wantModelCalls || catalog.revalidateCalls != btoi(tc.wantRevalidate) || catalog.modelAgent != "codex" || catalog.modelProject != "proj-1" || catalog.modelRefresh != tc.wantRefresh {
				t.Fatalf("model call = count:%d revalidate:%d agent:%q project:%q refresh:%v", catalog.modelCalls, catalog.revalidateCalls, catalog.modelAgent, catalog.modelProject, catalog.modelRefresh)
			}
		})
	}
}

func TestRefreshAgentModelsWithoutCatalogReturnsNotImplemented(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{}, httpd.ControlDeps{}))
	defer srv.Close()

	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/agents/codex/models/refresh", "")
	if status != http.StatusNotImplemented {
		t.Fatalf("POST refresh without catalog = %d, body=%s", status, body)
	}
	if !strings.Contains(string(body), `"error"`) {
		t.Fatalf("body = %s, want API error envelope", body)
	}
}

func btoi(value bool) int {
	if value {
		return 1
	}
	return 0
}
