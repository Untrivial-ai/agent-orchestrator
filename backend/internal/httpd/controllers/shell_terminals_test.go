package controllers_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	shelltermsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
)

type fakeShellTerminalService struct {
	gotOpenInput   shelltermsvc.OpenShellTerminalInput
	gotCloseID     string
	gotRenameID    string
	gotRenameTitle string
	opened         shelltermsvc.ShellTerminal
	renamed        shelltermsvc.ShellTerminal
	listed         []shelltermsvc.ShellTerminal
	err            error
}

func (f *fakeShellTerminalService) OpenShellTerminal(_ context.Context, in shelltermsvc.OpenShellTerminalInput) (shelltermsvc.ShellTerminal, error) {
	f.gotOpenInput = in
	return f.opened, f.err
}

func (f *fakeShellTerminalService) ListShellTerminalsForCurrentAppRun(context.Context) ([]shelltermsvc.ShellTerminal, error) {
	return f.listed, f.err
}

func (f *fakeShellTerminalService) RenameShellTerminal(_ context.Context, handleID, title string) (shelltermsvc.ShellTerminal, error) {
	f.gotRenameID = handleID
	f.gotRenameTitle = title
	return f.renamed, f.err
}

func (f *fakeShellTerminalService) CloseShellTerminal(_ context.Context, handleID string) error {
	f.gotCloseID = handleID
	return f.err
}

func newShellTerminalTestServer(t *testing.T, svc controllers.ShellTerminalService) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{ShellTerminals: svc}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)
	return srv
}

func sampleShellTerminal() shelltermsvc.ShellTerminal {
	return shelltermsvc.ShellTerminal{
		HandleID:   "shellterm-abc123",
		ProjectID:  "portfolio",
		WorkingDir: "/repos/portfolio",
		Title:      "portfolio",
		CreatedAt:  time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
	}
}

func TestShellTerminalsAPI_OpenReturnsHandleForMuxAttach(t *testing.T) {
	svc := &fakeShellTerminalService{opened: sampleShellTerminal()}
	srv := newShellTerminalTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/shell-terminals", `{"projectId":"portfolio","shell":"git-bash"}`)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", status, body)
	}
	if svc.gotOpenInput.ProjectID != "portfolio" {
		t.Errorf("project id = %q, want portfolio", svc.gotOpenInput.ProjectID)
	}
	if svc.gotOpenInput.Shell != "git-bash" {
		t.Errorf("shell = %q, want git-bash", svc.gotOpenInput.Shell)
	}
	var resp struct {
		ShellTerminal struct {
			HandleID   string `json:"handleId"`
			WorkingDir string `json:"workingDir"`
			Title      string `json:"title"`
		} `json:"shellTerminal"`
	}
	mustJSON(t, body, &resp)
	if resp.ShellTerminal.HandleID != "shellterm-abc123" {
		t.Errorf("handle id = %q, want the runtime handle the mux attaches to", resp.ShellTerminal.HandleID)
	}
	if resp.ShellTerminal.Title != "portfolio" {
		t.Errorf("title = %q", resp.ShellTerminal.Title)
	}
}

// The bug this guards: a shell opened from a session view must reach the
// service with the session id intact, not just the project id, since only the
// session id can resolve to the session's own worktree.
func TestShellTerminalsAPI_OpenPassesSessionScopeThrough(t *testing.T) {
	svc := &fakeShellTerminalService{opened: sampleShellTerminal()}
	srv := newShellTerminalTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/shell-terminals", `{"projectId":"portfolio","sessionId":"portfolio-3"}`)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", status, body)
	}
	if svc.gotOpenInput.ProjectID != "portfolio" {
		t.Errorf("project id = %q, want portfolio", svc.gotOpenInput.ProjectID)
	}
	if svc.gotOpenInput.SessionID != "portfolio-3" {
		t.Errorf("session id = %q, want portfolio-3", svc.gotOpenInput.SessionID)
	}
}

// The topbar action fires with no project selected and sends no body; that must
// open a shell rather than 400.
func TestShellTerminalsAPI_OpenAcceptsEmptyBody(t *testing.T) {
	svc := &fakeShellTerminalService{opened: sampleShellTerminal()}
	srv := newShellTerminalTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/shell-terminals", "")
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", status, body)
	}
	if svc.gotOpenInput.ProjectID != "" {
		t.Errorf("project id = %q, want empty", svc.gotOpenInput.ProjectID)
	}
}

func TestShellTerminalsAPI_OpenRejectsMalformedBody(t *testing.T) {
	srv := newShellTerminalTestServer(t, &fakeShellTerminalService{opened: sampleShellTerminal()})

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/shell-terminals", "{not json")
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", status, body)
	}
}

func TestShellTerminalsAPI_List(t *testing.T) {
	svc := &fakeShellTerminalService{listed: []shelltermsvc.ShellTerminal{sampleShellTerminal()}}
	srv := newShellTerminalTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "GET", "/api/v1/shell-terminals", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	var resp struct {
		ShellTerminals []struct {
			HandleID string `json:"handleId"`
		} `json:"shellTerminals"`
	}
	mustJSON(t, body, &resp)
	if len(resp.ShellTerminals) != 1 || resp.ShellTerminals[0].HandleID != "shellterm-abc123" {
		t.Fatalf("terminals = %+v", resp.ShellTerminals)
	}
}

func TestShellTerminalsAPI_CloseReturnsNoContent(t *testing.T) {
	svc := &fakeShellTerminalService{}
	srv := newShellTerminalTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "DELETE", "/api/v1/shell-terminals/shellterm-abc123", "")
	if status != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", status, body)
	}
	if svc.gotCloseID != "shellterm-abc123" {
		t.Errorf("closed handle = %q", svc.gotCloseID)
	}
}

func TestShellTerminalsAPI_CloseDecodesDirectHostHandle(t *testing.T) {
	svc := &fakeShellTerminalService{}
	srv := newShellTerminalTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "DELETE", "/api/v1/shell-terminals/ptyhost-v1%3Ashellterm-abc123", "")
	if status != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", status, body)
	}
	if svc.gotCloseID != "ptyhost-v1:shellterm-abc123" {
		t.Errorf("closed handle = %q", svc.gotCloseID)
	}
}

func TestShellTerminalsAPI_CloseUnknownHandleReturnsNotFoundEnvelope(t *testing.T) {
	svc := &fakeShellTerminalService{err: apierr.NotFound("SHELL_TERMINAL_NOT_FOUND", "No such shell terminal")}
	srv := newShellTerminalTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "DELETE", "/api/v1/shell-terminals/shellterm-ghost", "")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", status, body)
	}
}

func TestShellTerminalsAPI_RenameReturnsUpdatedTerminal(t *testing.T) {
	renamed := sampleShellTerminal()
	renamed.Title = "deploy"
	svc := &fakeShellTerminalService{renamed: renamed}
	srv := newShellTerminalTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "PATCH", "/api/v1/shell-terminals/shellterm-abc123", `{"title":"deploy"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if svc.gotRenameID != "shellterm-abc123" || svc.gotRenameTitle != "deploy" {
		t.Errorf("rename args = (%q, %q)", svc.gotRenameID, svc.gotRenameTitle)
	}
	var resp struct {
		ShellTerminal struct {
			Title string `json:"title"`
		} `json:"shellTerminal"`
	}
	mustJSON(t, body, &resp)
	if resp.ShellTerminal.Title != "deploy" {
		t.Errorf("response title = %q, want deploy", resp.ShellTerminal.Title)
	}
}

func TestShellTerminalsAPI_RenameDecodesDirectHostHandle(t *testing.T) {
	renamed := sampleShellTerminal()
	renamed.Title = "deploy"
	svc := &fakeShellTerminalService{renamed: renamed}
	srv := newShellTerminalTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "PATCH", "/api/v1/shell-terminals/ptyhost-v1%3Ashellterm-abc123", `{"title":"deploy"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if svc.gotRenameID != "ptyhost-v1:shellterm-abc123" {
		t.Errorf("renamed handle = %q", svc.gotRenameID)
	}
}

func TestShellTerminalsAPI_RenameRejectsMalformedBody(t *testing.T) {
	srv := newShellTerminalTestServer(t, &fakeShellTerminalService{})

	body, status, _ := doRequest(t, srv, "PATCH", "/api/v1/shell-terminals/shellterm-abc123", "{not json")
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", status, body)
	}
}

// A daemon built without the service must answer the locked 501 envelope, not
// panic on a nil interface.
func TestShellTerminalsAPI_NotImplementedWithoutService(t *testing.T) {
	srv := newShellTerminalTestServer(t, nil)

	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/shell-terminals"},
		{"POST", "/api/v1/shell-terminals"},
		{"PATCH", "/api/v1/shell-terminals/shellterm-abc123"},
		{"DELETE", "/api/v1/shell-terminals/shellterm-abc123"},
	} {
		body, status, _ := doRequest(t, srv, tc.method, tc.path, "")
		if status != http.StatusNotImplemented {
			t.Errorf("%s %s status = %d, want 501; body=%s", tc.method, tc.path, status, body)
		}
	}
}
