package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/sessionimport"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/sessionimportsvc"
)

type fakeImportService struct {
	discover    []sessionimport.ImportableSession
	discoverErr error
	lastOpts    sessionimport.DiscoverOptions
	lastProject domain.ProjectID
	imported    map[string]bool
}

func (f *fakeImportService) Discover(_ context.Context, opts sessionimport.DiscoverOptions, projectID domain.ProjectID) ([]sessionimport.ImportableSession, error) {
	f.lastOpts = opts
	f.lastProject = projectID
	return f.discover, f.discoverErr
}

func (f *fakeImportService) Import(_ context.Context, provider domain.AgentHarness, nativeID string, projectID domain.ProjectID) (domain.Session, bool, error) {
	already := f.imported[nativeID]
	return domain.Session{SessionRecord: domain.SessionRecord{ID: domain.SessionID("imported-1"), Harness: provider}}, already, nil
}

func TestListImportable(t *testing.T) {
	svc := &fakeImportService{
		discover: []sessionimport.ImportableSession{
			{Provider: domain.HarnessCodex, NativeSessionID: "c1", Title: "codex one", CWD: "/x", LastActivity: time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC), MessageCount: 4},
			{Provider: domain.HarnessClaudeCode, NativeSessionID: "u1", Title: "claude one", CWD: "/y", LastActivity: time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC), AlreadyImported: true},
		},
	}
	c := &SessionsController{Import: svc}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/importable?projectId=proj", nil)
	rec := httptest.NewRecorder()
	c.listImportable(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body %s", rec.Code, rec.Body.String())
	}
	var resp ListImportableSessionsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Sessions) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(resp.Sessions))
	}
	if resp.Sessions[0].Provider != "codex" || resp.Sessions[0].NativeSessionID != "c1" {
		t.Errorf("first session: %+v", resp.Sessions[0])
	}
	if resp.Sessions[0].LastActivity != "2026-08-21T09:00:00Z" {
		t.Errorf("last activity format: %q", resp.Sessions[0].LastActivity)
	}
	if !resp.Sessions[1].AlreadyImported {
		t.Errorf("second session should be flagged already imported")
	}
	// Discovery always uses the approved 15-day window.
	if svc.lastOpts.Since.IsZero() {
		t.Errorf("expected a fixed activity window")
	}
}

func TestListImportableProviderFilter(t *testing.T) {
	svc := &fakeImportService{
		discover: []sessionimport.ImportableSession{
			{Provider: domain.HarnessCodex, NativeSessionID: "c1"},
			{Provider: domain.HarnessClaudeCode, NativeSessionID: "u1"},
		},
	}
	c := &SessionsController{Import: svc}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/importable?projectId=proj&provider=claude-code", nil)
	rec := httptest.NewRecorder()
	c.listImportable(rec, req)

	var resp ListImportableSessionsResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Sessions) != 1 || resp.Sessions[0].Provider != "claude-code" {
		t.Fatalf("provider filter failed: %+v", resp.Sessions)
	}
}

func TestListImportableRequiresProject(t *testing.T) {
	c := &SessionsController{Import: &fakeImportService{}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/importable", nil)
	rec := httptest.NewRecorder()
	c.listImportable(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for missing project, got %d", rec.Code)
	}
}

func TestImportSessionValidation(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"provider":"codex","nativeSessionId":"x"}`,
		`{"projectId":"  ","provider":"codex","nativeSessionId":"x"}`,
		`{"projectId":"proj","nativeSessionId":"x"}`,
		`{"projectId":"proj","provider":"codex"}`,
	} {
		t.Run(body, func(t *testing.T) {
			c := &SessionsController{Import: &fakeImportService{}}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/import", strings.NewReader(body))
			rec := httptest.NewRecorder()
			c.importSession(rec, req)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "INVALID_BODY") {
				t.Fatalf("want missing-field error, got %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestImportSessionIdempotent(t *testing.T) {
	c := &SessionsController{Import: &fakeImportService{imported: map[string]bool{"u1": true}}}

	// Fresh import -> 201.
	fresh := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/import", strings.NewReader(`{"projectId":"proj","provider":"claude-code","nativeSessionId":"new"}`))
	frec := httptest.NewRecorder()
	c.importSession(frec, fresh)
	if frec.Code != http.StatusCreated {
		t.Fatalf("fresh import: want 201, got %d", frec.Code)
	}

	// Re-import of an existing native id -> 200 with alreadyImported.
	dup := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/import", strings.NewReader(`{"projectId":"proj","provider":"claude-code","nativeSessionId":"u1"}`))
	drec := httptest.NewRecorder()
	c.importSession(drec, dup)
	if drec.Code != http.StatusOK {
		t.Fatalf("re-import: want 200, got %d", drec.Code)
	}
	var resp ImportSessionResponse
	_ = json.Unmarshal(drec.Body.Bytes(), &resp)
	if !resp.AlreadyImported {
		t.Errorf("re-import should report alreadyImported")
	}
}

func TestImportRoutesNilService501(t *testing.T) {
	c := &SessionsController{} // Import nil
	for _, tc := range []struct {
		method, path string
		body         string
		fn           func(http.ResponseWriter, *http.Request)
	}{
		{http.MethodGet, "/api/v1/sessions/importable", "", c.listImportable},
		{http.MethodPost, "/api/v1/sessions/import", `{"projectId":"proj","provider":"codex","nativeSessionId":"x"}`, c.importSession},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		tc.fn(rec, req)
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s %s: want 501 when service nil, got %d", tc.method, tc.path, rec.Code)
		}
	}
}

func TestListImportableUnknownProject(t *testing.T) {
	c := &SessionsController{Import: &fakeImportService{discoverErr: sessionimportsvc.ErrImportProjectUnresolved}}
	rec := httptest.NewRecorder()
	c.listImportable(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/importable?projectId=deleted", nil))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "IMPORT_PROJECT_UNRESOLVED") {
		t.Fatalf("want actionable project error, got %d %s", rec.Code, rec.Body.String())
	}
}
