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
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/systeminstall"
)

type fakeCodexUpdater struct {
	fakeInstaller
	token   string
	refresh bool
}

func (f *fakeCodexUpdater) CodexUpdate(_ context.Context, refresh bool) (systeminstall.CodexUpdateAdvisory, error) {
	f.refresh = refresh
	return systeminstall.CodexUpdateAdvisory{Token: "owner", CanUpdate: true, Version: "1.0.0", AvailableVersion: "1.1.0"}, nil
}
func (f *fakeCodexUpdater) StartCodexUpdate(_ context.Context, token string) (systeminstall.Job, error) {
	f.token = token
	f.startCalls++
	return systeminstall.Job{Target: systeminstall.TargetCodex, Status: systeminstall.StatusQueued}, f.startErr
}

func TestCodexUpdateRoutes(t *testing.T) {
	f := &fakeCodexUpdater{}
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, httpd.APIDeps{Installer: f}, httpd.ControlDeps{}))
	defer srv.Close()
	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/agents/codex/update?refresh=true", "")
	if status != http.StatusOK || !f.refresh || f.startCalls != 0 {
		t.Fatalf("%d %s", status, body)
	}
	for _, invalid := range []string{`{}`, `{"token":"owner","command":"npm install"}`, `{"token":"owner","prefix":"/other"}`} {
		_, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/codex/update", invalid)
		if status != http.StatusBadRequest {
			t.Fatal(status)
		}
	}
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/codex/update", `{"token":"owner"}`)
	if status != http.StatusAccepted || f.token != "owner" || !strings.Contains(string(body), `"queued"`) {
		t.Fatalf("%d %s", status, body)
	}
	f.startErr = systeminstall.ErrInstallationChanged
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/codex/update", `{"token":"old-owner"}`)
	if status != http.StatusConflict || !strings.Contains(string(body), "INSTALLATION_CHANGED") {
		t.Fatalf("%d %s", status, body)
	}
}
