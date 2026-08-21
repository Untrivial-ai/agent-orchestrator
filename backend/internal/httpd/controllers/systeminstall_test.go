package controllers_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/systeminstall"
)

type fakeInstaller struct {
	plans   []systeminstall.AgentPlan
	started systeminstall.Target
	job     systeminstall.Job
}

func (f *fakeInstaller) AgentPlans(context.Context) ([]systeminstall.AgentPlan, error) {
	return f.plans, nil
}

func (f *fakeInstaller) Start(_ context.Context, target systeminstall.Target) (systeminstall.Job, error) {
	f.started = target
	f.job = systeminstall.Job{Target: target, Status: systeminstall.StatusRunning}
	return f.job, nil
}

func (f *fakeInstaller) Status(systeminstall.Target) (systeminstall.Job, error) {
	return f.job, nil
}

func TestAgentInstallerCatalogAndStart(t *testing.T) {
	installer := &fakeInstaller{plans: []systeminstall.AgentPlan{{
		AgentID: "codex", Available: true, Automatic: true, Method: "npm",
		DocumentationURL: "https://github.com/openai/codex",
	}}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := httpd.NewRouterWithControl(config.Config{}, log, nil,
		httpd.APIDeps{Installer: installer}, httpd.ControlDeps{})

	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/agents/installers", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("GET installers = %d, body=%s", list.Code, list.Body.String())
	}

	start := httptest.NewRecorder()
	router.ServeHTTP(start, httptest.NewRequest(http.MethodPost, "/api/v1/agents/codex/install", nil))
	if start.Code != http.StatusAccepted || installer.started != systeminstall.TargetCodex {
		t.Fatalf("POST codex install = (%d, %q), body=%s", start.Code, installer.started, start.Body.String())
	}
}

func TestAgentInstallerRejectsUnknownHarness(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := httpd.NewRouterWithControl(config.Config{}, log, nil,
		httpd.APIDeps{Installer: &fakeInstaller{}}, httpd.ControlDeps{})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/agents/rm-rf-everything/install", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("POST unknown install = %d, want %d, body=%s", response.Code, http.StatusBadRequest, response.Body.String())
	}
}
