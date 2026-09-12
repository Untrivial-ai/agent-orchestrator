package controllers_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/systeminstall"
)

type fakeInstaller struct {
	startJob      systeminstall.Job
	startErr      error
	statusJob     systeminstall.Job
	statusErr     error
	plans         []systeminstall.AgentPlan
	plansErr      error
	startCalls    int
	lastTarget    systeminstall.Target
	lastMethod    string
	lastOperation systeminstall.AgentOperation
	agentJobs     []systeminstall.Job
	verifyJob     systeminstall.Job
	verifyErr     error
}

func (f *fakeInstaller) Start(_ context.Context, target systeminstall.Target) (systeminstall.Job, error) {
	f.startCalls++
	f.lastTarget = target
	return f.startJob, f.startErr
}

func (f *fakeInstaller) Status(_ context.Context, target systeminstall.Target) (systeminstall.Job, error) {
	f.lastTarget = target
	return f.statusJob, f.statusErr
}

func (f *fakeInstaller) AgentPlans(context.Context) ([]systeminstall.AgentPlan, error) {
	return f.plans, f.plansErr
}

func (f *fakeInstaller) StartAgentOperation(_ context.Context, target systeminstall.Target, method string, operation systeminstall.AgentOperation) (systeminstall.Job, error) {
	f.startCalls++
	f.lastTarget = target
	f.lastMethod = method
	f.lastOperation = operation
	return f.startJob, f.startErr
}

func (f *fakeInstaller) AgentJobs(context.Context) ([]systeminstall.Job, error) {
	return f.agentJobs, f.statusErr
}

func (f *fakeInstaller) Verify(_ context.Context, target systeminstall.Target) (systeminstall.Job, error) {
	f.lastTarget = target
	return f.verifyJob, f.verifyErr
}

func TestAgentInstallRoutes(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	installer := &fakeInstaller{
		plans:     []systeminstall.AgentPlan{{AgentID: "codex", Available: true, Automatic: true, Method: "npm"}},
		startJob:  systeminstall.Job{Target: systeminstall.TargetCodex, Status: systeminstall.StatusInstalling, Method: "npm"},
		agentJobs: []systeminstall.Job{{Target: systeminstall.TargetCodex, Status: systeminstall.StatusInterrupted, Method: "npm", Error: "AO restarted"}},
		verifyJob: systeminstall.Job{Target: systeminstall.TargetCodex, Status: systeminstall.StatusVerifying},
	}
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		Installer: installer,
	}, httpd.ControlDeps{}))
	defer srv.Close()

	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/agents/installers", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"agentId":"codex"`) {
		t.Fatalf("GET /agents/installers = %d, body=%s", status, body)
	}
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/codex/install", `{"method":"npm","operation":"reinstall"}`)
	if status != http.StatusAccepted || installer.lastTarget != systeminstall.TargetCodex || installer.lastMethod != "npm" || installer.lastOperation != systeminstall.AgentOperationReinstall {
		t.Fatalf("POST /agents/codex/install = %d, target=%q method=%q operation=%q, body=%s", status, installer.lastTarget, installer.lastMethod, installer.lastOperation, body)
	}
	body, status, _ = doRequest(t, srv, http.MethodGet, "/api/v1/agents/install-jobs", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"status":"interrupted"`) {
		t.Fatalf("GET /agents/install-jobs = %d, body=%s", status, body)
	}
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/codex/verify", "")
	if status != http.StatusAccepted || !strings.Contains(string(body), `"status":"verifying"`) {
		t.Fatalf("POST /agents/codex/verify = %d, body=%s", status, body)
	}
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/not-real/install", "")
	if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"UNKNOWN_AGENT_INSTALL_TARGET"`) {
		t.Fatalf("POST /agents/not-real/install = %d, body=%s", status, body)
	}
}

func TestAgentInstallRejectsUnknownOperation(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	installer := &fakeInstaller{}
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{Installer: installer}, httpd.ControlDeps{}))
	defer srv.Close()

	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/agents/codex/install", `{"method":"npm","operation":"repair"}`)
	if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"INVALID_INSTALL_OPERATION"`) || installer.startCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", status, installer.startCalls, body)
	}
}

func TestAgentInstallDefaultsOmittedOperationToInstall(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	installer := &fakeInstaller{startJob: systeminstall.Job{Target: systeminstall.TargetCodex, Status: systeminstall.StatusInstalling}}
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{Installer: installer}, httpd.ControlDeps{}))
	defer srv.Close()

	_, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/agents/codex/install", `{"method":"npm"}`)
	if status != http.StatusAccepted || installer.lastOperation != systeminstall.AgentOperationInstall {
		t.Fatalf("status=%d operation=%q, want install", status, installer.lastOperation)
	}
}

func TestAgentInstallMapsMethodAndActiveHarnessErrors(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tt := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "invalid method", err: systeminstall.ErrInstallMethod, wantStatus: http.StatusBadRequest, wantCode: "INSTALL_METHOD_UNAVAILABLE"},
		{name: "active droid", err: systeminstall.ErrHarnessActive, wantStatus: http.StatusConflict, wantCode: "HARNESS_ACTIVE"},
		{name: "install active", err: systeminstall.ErrInstallActive, wantStatus: http.StatusConflict, wantCode: "INSTALL_ACTIVE"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			installer := &fakeInstaller{startErr: tt.err}
			srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{Installer: installer}, httpd.ControlDeps{}))
			defer srv.Close()
			body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/agents/droid/install", `{"method":"homebrew"}`)
			if status != tt.wantStatus || !strings.Contains(string(body), `"code":"`+tt.wantCode+`"`) || !strings.Contains(string(body), `"requestId":"`) {
				t.Fatalf("status = %d body=%s", status, body)
			}
		})
	}
}

func TestPostSystemInstall(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	installer := &fakeInstaller{startJob: systeminstall.Job{
		Target:  systeminstall.TargetGH,
		Status:  systeminstall.StatusRunning,
		Command: "brew install gh",
	}}
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		Installer: installer,
	}, httpd.ControlDeps{}))
	defer srv.Close()

	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/system/install/gh", "")
	if status != http.StatusAccepted {
		t.Fatalf("POST /system/install/gh = %d, body=%s", status, body)
	}
	for _, want := range []string{`"target":"gh"`, `"status":"running"`, `"command":"brew install gh"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("body missing %s: %s", want, body)
		}
	}
	if installer.startCalls != 1 {
		t.Fatalf("startCalls = %d, want 1", installer.startCalls)
	}
}

func TestPostSystemInstallMapsActiveAgentConflict(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	installer := &fakeInstaller{startErr: systeminstall.ErrInstallActive}
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{Installer: installer}, httpd.ControlDeps{}))
	defer srv.Close()

	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/system/install/codex", "")
	if status != http.StatusConflict || !strings.Contains(string(body), `"code":"INSTALL_ACTIVE"`) {
		t.Fatalf("POST /system/install/codex = %d, body=%s", status, body)
	}
}

func TestPostSystemInstall_UnknownTarget(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	installer := &fakeInstaller{}
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		Installer: installer,
	}, httpd.ControlDeps{}))
	defer srv.Close()

	// A junk single path segment (not one of the 6 known targets) must be
	// rejected before ever reaching the service — same guard that stops a
	// path-traversal-shaped value from being passed through.
	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/system/install/rm-rf-everything", "")
	if status != http.StatusBadRequest {
		t.Fatalf("POST /system/install/<junk> = %d, want %d, body=%s", status, http.StatusBadRequest, body)
	}
	if !strings.Contains(string(body), `"code":"UNKNOWN_INSTALL_TARGET"`) {
		t.Fatalf("body missing UNKNOWN_INSTALL_TARGET code: %s", body)
	}
	if installer.startCalls != 0 {
		t.Fatalf("startCalls = %d, want 0 (unknown target must never reach the service)", installer.startCalls)
	}

	// Harness-only targets belong to /agents/{agent}/install and must not widen
	// the legacy /system/install contract beyond its documented enum.
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/system/install/cursor", "")
	if status != http.StatusBadRequest {
		t.Fatalf("POST /system/install/cursor = %d, want %d, body=%s", status, http.StatusBadRequest, body)
	}
	if installer.startCalls != 0 {
		t.Fatalf("startCalls = %d, want 0 (agent-only target must never reach the system route)", installer.startCalls)
	}
}

func TestPostSystemInstall_NotImplemented(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{}, httpd.ControlDeps{}))
	defer srv.Close()

	_, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/system/install/gh", "")
	if status != http.StatusNotImplemented {
		t.Fatalf("POST /system/install/gh = %d, want %d", status, http.StatusNotImplemented)
	}
}

func TestGetSystemInstallStatus(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	installer := &fakeInstaller{statusJob: systeminstall.Job{
		Target: systeminstall.TargetOpencode,
		Status: systeminstall.StatusSucceeded,
	}}
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		Installer: installer,
	}, httpd.ControlDeps{}))
	defer srv.Close()

	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/system/install/opencode", "")
	if status != http.StatusOK {
		t.Fatalf("GET /system/install/opencode = %d, body=%s", status, body)
	}
	if !strings.Contains(string(body), `"status":"succeeded"`) {
		t.Fatalf("body missing status: %s", body)
	}
	if installer.lastTarget != systeminstall.TargetOpencode {
		t.Fatalf("lastTarget = %q, want %q", installer.lastTarget, systeminstall.TargetOpencode)
	}
}

func TestGetSystemInstallStatus_UnknownTarget(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	installer := &fakeInstaller{}
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		Installer: installer,
	}, httpd.ControlDeps{}))
	defer srv.Close()

	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/system/install/bogus", "")
	if status != http.StatusBadRequest {
		t.Fatalf("GET /system/install/bogus = %d, want %d, body=%s", status, http.StatusBadRequest, body)
	}
}

func TestGetSystemInstallStatus_NotImplemented(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{}, httpd.ControlDeps{}))
	defer srv.Close()

	_, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/system/install/gh", "")
	if status != http.StatusNotImplemented {
		t.Fatalf("GET /system/install/gh = %d, want %d", status, http.StatusNotImplemented)
	}
}

func TestSystemInstallController_ServiceError(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	installer := &fakeInstaller{startErr: errors.New("boom")}
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		Installer: installer,
	}, httpd.ControlDeps{}))
	defer srv.Close()

	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/system/install/tmux", "")
	if status != http.StatusInternalServerError {
		t.Fatalf("POST /system/install/tmux = %d, want %d, body=%s", status, http.StatusInternalServerError, body)
	}
	for _, want := range []string{
		`"error":"internal"`,
		`"code":"INTERNAL_ERROR"`,
		`"message":"Internal server error"`,
		`"requestId":"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("body missing %s: %s", want, body)
		}
	}
	if strings.Contains(string(body), "boom") {
		t.Fatalf("body leaked internal service error: %s", body)
	}
}
