package controllers_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/devimport"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	devimportsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/devimport"
)

type fakeDevImportService struct {
	lastInput devimportsvc.RunInput
}

func (f *fakeDevImportService) RunProjects(_ context.Context, in devimportsvc.RunInput) (devimport.Report, error) {
	f.lastInput = in
	return devimport.Report{Inserted: 1}, nil
}

func TestDevImportProjects_RequestDecoding(t *testing.T) {
	importSvc := &fakeDevImportService{}
	log := slog.New(slog.NewTextHandler(ioDiscard{}, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(
		config.Config{},
		log,
		nil,
		httpd.APIDeps{DevImport: importSvc},
		httpd.ControlDeps{},
	))
	defer srv.Close()

	// 1. Valid request
	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/dev/import-projects", `{"sourceDataDir":"/test/dir"}`)
	if status != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", status, body)
	}

	// 2. Trailing non-JSON junk is rejected
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/dev/import-projects", `{"sourceDataDir":"/test/dir"} trailing junk`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")

	// 3. Concatenated second JSON document is rejected
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/dev/import-projects", `{"sourceDataDir":"/test/dir"} {"second":"doc"}`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")

	// 4. Unknown fields are rejected
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/dev/import-projects", `{"sourceDataDir":"/test/dir","unknownField":true}`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")

	// 5. Empty body is rejected
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/dev/import-projects", "")
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")

	// 6. Whitespace-only body is rejected
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/dev/import-projects", "   \r\n\t  ")
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")

	// 7. Oversized body (> 1 MiB) is rejected
	oversized := `{"sourceDataDir":"/test/dir","padding":"` + strings.Repeat("A", 2<<20) + `"}`
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/dev/import-projects", oversized)
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")

	// 8. Valid request with trailing whitespace is accepted
	validWithWS := `{"sourceDataDir":"/test/dir"}   ` + "\r\n  "
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/dev/import-projects", validWithWS)
	if status != http.StatusOK {
		t.Fatalf("expected 200 OK for valid request with trailing whitespace, got %d: %s", status, body)
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}
