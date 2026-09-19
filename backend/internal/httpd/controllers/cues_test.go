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
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	cuesvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cue"
)

type fakeCueService struct {
	gotProject  domain.ProjectID
	gotCueID    domain.CueID
	gotSession  domain.SessionID
	gotCreateIn cuesvc.Input
	gotUpdateIn cuesvc.Input
	created     domain.Cue
	fetched     domain.Cue
	listed      []domain.Cue
	updated     domain.Cue
	invoked     domain.SessionID
	err         error
}

func (f *fakeCueService) Create(_ context.Context, projectID domain.ProjectID, input cuesvc.Input) (domain.Cue, error) {
	f.gotProject = projectID
	f.gotCreateIn = input
	return f.created, f.err
}

func (f *fakeCueService) Get(_ context.Context, cueID domain.CueID) (domain.Cue, error) {
	f.gotCueID = cueID
	return f.fetched, f.err
}

func (f *fakeCueService) List(_ context.Context, projectID domain.ProjectID) ([]domain.Cue, error) {
	f.gotProject = projectID
	return f.listed, f.err
}

func (f *fakeCueService) Update(_ context.Context, cueID domain.CueID, input cuesvc.Input) (domain.Cue, error) {
	f.gotCueID = cueID
	f.gotUpdateIn = input
	return f.updated, f.err
}

func (f *fakeCueService) Delete(_ context.Context, cueID domain.CueID) error {
	f.gotCueID = cueID
	return f.err
}

func (f *fakeCueService) Invoke(_ context.Context, cueID domain.CueID, sessionID domain.SessionID) (domain.SessionID, error) {
	f.gotCueID = cueID
	f.gotSession = sessionID
	return f.invoked, f.err
}

func newCueTestServer(t *testing.T, svc controllers.CueService) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{Cues: svc}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)
	return srv
}

func sampleCue() domain.Cue {
	return domain.Cue{
		ID:          "cue-def456",
		ProjectID:   "portfolio",
		Name:        "Run Tests",
		Description: "Run the test suite",
		Type:        domain.CueTypeCommand,
		Command:     "pnpm test",
		CreatedAt:   time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
	}
}

func TestCuesAPI_ListReturnsProjectCues(t *testing.T) {
	svc := &fakeCueService{listed: []domain.Cue{sampleCue()}}
	srv := newCueTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "GET", "/api/v1/projects/portfolio/cues", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if svc.gotProject != "portfolio" {
		t.Errorf("project = %q, want portfolio", svc.gotProject)
	}
	var resp struct {
		Cues []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Type    string `json:"type"`
			Command string `json:"command"`
		} `json:"cues"`
	}
	mustJSON(t, body, &resp)
	if len(resp.Cues) != 1 || resp.Cues[0].ID != "cue-def456" || resp.Cues[0].Name != "Run Tests" ||
		resp.Cues[0].Type != "command" || resp.Cues[0].Command != "pnpm test" {
		t.Fatalf("cues = %+v", resp.Cues)
	}
}

func TestCuesAPI_CreatePersistsDefinition(t *testing.T) {
	svc := &fakeCueService{created: sampleCue()}
	srv := newCueTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/projects/portfolio/cues",
		`{"name":"Run Tests","description":"Run the test suite","type":"command","command":"pnpm test"}`)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", status, body)
	}
	if svc.gotProject != "portfolio" {
		t.Errorf("project = %q, want portfolio", svc.gotProject)
	}
	if svc.gotCreateIn.Name != "Run Tests" || svc.gotCreateIn.Type != domain.CueTypeCommand ||
		svc.gotCreateIn.Command != "pnpm test" || svc.gotCreateIn.Description != "Run the test suite" {
		t.Errorf("create input = %+v", svc.gotCreateIn)
	}
	var resp struct {
		Cue struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"cue"`
	}
	mustJSON(t, body, &resp)
	if resp.Cue.ID != "cue-def456" || resp.Cue.Name != "Run Tests" {
		t.Errorf("created cue = %+v", resp.Cue)
	}
}

func TestCuesAPI_CreateRejectsMalformedBody(t *testing.T) {
	srv := newCueTestServer(t, &fakeCueService{})

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/projects/portfolio/cues", "{not json")
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", status, body)
	}
}

func TestCuesAPI_CreateSurfacesServiceErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"invalid input", apierr.Invalid("INVALID_CUE_NAME", "bad name", nil), http.StatusBadRequest},
		{"unknown project", apierr.NotFound("PROJECT_NOT_FOUND", "Unknown project"), http.StatusNotFound},
		{"duplicate name", apierr.Conflict("CUE_NAME_EXISTS", "duplicate", nil), http.StatusConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeCueService{err: tc.err, created: sampleCue()}
			srv := newCueTestServer(t, svc)

			body, status, _ := doRequest(t, srv, "POST", "/api/v1/projects/portfolio/cues",
				`{"name":"Run Tests","type":"command","command":"pnpm test"}`)
			if status != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", status, tc.want, body)
			}
		})
	}
}

func TestCuesAPI_GetReturnsCue(t *testing.T) {
	svc := &fakeCueService{fetched: sampleCue()}
	srv := newCueTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "GET", "/api/v1/cues/cue-def456", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if svc.gotCueID != "cue-def456" {
		t.Errorf("cue id = %q, want cue-def456", svc.gotCueID)
	}
	var resp struct {
		Cue struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"cue"`
	}
	mustJSON(t, body, &resp)
	if resp.Cue.ID != "cue-def456" {
		t.Errorf("cue = %+v", resp.Cue)
	}
}

func TestCuesAPI_GetUnknownCueReturnsNotFound(t *testing.T) {
	svc := &fakeCueService{err: apierr.NotFound("CUE_NOT_FOUND", "Unknown cue")}
	srv := newCueTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "GET", "/api/v1/cues/cue-ghost", "")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", status, body)
	}
}

func TestCuesAPI_UpdateReplacesDefinition(t *testing.T) {
	svc := &fakeCueService{updated: sampleCue()}
	srv := newCueTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "PATCH", "/api/v1/cues/cue-def456",
		`{"name":"Run Tests","description":"d","type":"agent","prompt":"Run the test suite and fix failures."}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if svc.gotCueID != "cue-def456" {
		t.Errorf("cue id = %q", svc.gotCueID)
	}
	if svc.gotUpdateIn.Name != "Run Tests" || svc.gotUpdateIn.Type != domain.CueTypeAgent ||
		svc.gotUpdateIn.Prompt != "Run the test suite and fix failures." {
		t.Errorf("update input = %+v", svc.gotUpdateIn)
	}
	var resp struct {
		Cue struct {
			ID string `json:"id"`
		} `json:"cue"`
	}
	mustJSON(t, body, &resp)
	if resp.Cue.ID != "cue-def456" {
		t.Errorf("cue = %+v", resp.Cue)
	}
}

func TestCuesAPI_UpdateSurfacesServiceErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"unknown cue", apierr.NotFound("CUE_NOT_FOUND", "Unknown cue"), http.StatusNotFound},
		{"duplicate name", apierr.Conflict("CUE_NAME_EXISTS", "duplicate", nil), http.StatusConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeCueService{err: tc.err, updated: sampleCue()}
			srv := newCueTestServer(t, svc)

			body, status, _ := doRequest(t, srv, "PATCH", "/api/v1/cues/cue-def456",
				`{"name":"Run Tests","type":"command","command":"pnpm test"}`)
			if status != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", status, tc.want, body)
			}
		})
	}
}

func TestCuesAPI_DeleteReturnsNoContent(t *testing.T) {
	svc := &fakeCueService{}
	srv := newCueTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "DELETE", "/api/v1/cues/cue-def456", "")
	if status != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", status, body)
	}
	if svc.gotCueID != "cue-def456" {
		t.Errorf("deleted cue id = %q", svc.gotCueID)
	}
}

func TestCuesAPI_DeleteUnknownCueReturnsNotFound(t *testing.T) {
	svc := &fakeCueService{err: apierr.NotFound("CUE_NOT_FOUND", "Unknown cue")}
	srv := newCueTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "DELETE", "/api/v1/cues/cue-ghost", "")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", status, body)
	}
}

func TestCuesAPI_InvokeMessagesSession(t *testing.T) {
	svc := &fakeCueService{invoked: "sess-123"}
	srv := newCueTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/cues/cue-def456/invoke",
		`{"sessionId":"sess-123"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if svc.gotCueID != "cue-def456" || svc.gotSession != "sess-123" {
		t.Fatalf("invoke args = cue %q session %q", svc.gotCueID, svc.gotSession)
	}
	var resp struct {
		SessionID string `json:"sessionId"`
	}
	mustJSON(t, body, &resp)
	if resp.SessionID != "sess-123" {
		t.Fatalf("sessionId = %q, want sess-123", resp.SessionID)
	}
}

func TestCuesAPI_InvokeWithoutSessionSpawnsWorker(t *testing.T) {
	svc := &fakeCueService{invoked: "sess-worker"}
	srv := newCueTestServer(t, svc)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/cues/cue-def456/invoke", `{}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if svc.gotSession != "" {
		t.Fatalf("session = %q, want empty", svc.gotSession)
	}
	var resp struct {
		SessionID string `json:"sessionId"`
	}
	mustJSON(t, body, &resp)
	if resp.SessionID != "sess-worker" {
		t.Fatalf("sessionId = %q, want sess-worker", resp.SessionID)
	}
}

func TestCuesAPI_InvokeSurfacesServiceErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"unknown cue", apierr.NotFound("CUE_NOT_FOUND", "Unknown cue"), http.StatusNotFound},
		{"spawn failure", apierr.Conflict("AGENT_BINARY_NOT_FOUND", "missing", nil), http.StatusConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeCueService{err: tc.err}
			srv := newCueTestServer(t, svc)

			body, status, _ := doRequest(t, srv, "POST", "/api/v1/cues/cue-def456/invoke", `{}`)
			if status != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", status, tc.want, body)
			}
		})
	}
}

func TestCuesAPI_InvokeRejectsPresentBlankSessionID(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"null", `{"sessionId": null}`},
		{"empty string", `{"sessionId": ""}`},
		{"whitespace", `{"sessionId": "   "}`},
		{"non-string", `{"sessionId": 42}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeCueService{}
			srv := newCueTestServer(t, svc)

			body, status, _ := doRequest(t, srv, "POST", "/api/v1/cues/cue-def456/invoke", tc.body)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", status, body)
			}
			if !strings.Contains(string(body), "INVALID_SESSION_ID") {
				t.Fatalf("missing INVALID_SESSION_ID envelope: %s", body)
			}
			if svc.gotCueID != "" || svc.gotSession != "" {
				t.Fatalf("invalid body dispatched: cue=%q session=%q", svc.gotCueID, svc.gotSession)
			}
		})
	}
}

func TestCuesAPI_InvokeRejectsTopLevelNullBody(t *testing.T) {
	svc := &fakeCueService{}
	srv := newCueTestServer(t, svc)

	for _, body := range []string{`null`, ` null `} {
		body, status, _ := doRequest(t, srv, "POST", "/api/v1/cues/cue-def456/invoke", body)
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", status, body)
		}
		if !strings.Contains(string(body), "INVALID_JSON") {
			t.Fatalf("missing INVALID_JSON envelope: %s", body)
		}
		if svc.gotCueID != "" || svc.gotSession != "" {
			t.Fatalf("invalid body dispatched: cue=%q session=%q", svc.gotCueID, svc.gotSession)
		}
	}
}

func TestCuesAPI_InvokeOmittedSessionIDSpawnsWorker(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"empty body", ""},
		{"empty object", `{}`},
		{"unrelated field", `{"unused":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeCueService{invoked: "sess-worker"}
			srv := newCueTestServer(t, svc)

			body, status, _ := doRequest(t, srv, "POST", "/api/v1/cues/cue-def456/invoke", tc.body)
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", status, body)
			}
			if svc.gotSession != "" {
				t.Fatalf("session = %q, want empty (worker spawn)", svc.gotSession)
			}
		})
	}
}
func TestCuesAPI_NotImplementedWithoutService(t *testing.T) {
	srv := newCueTestServer(t, nil)

	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/projects/portfolio/cues"},
		{"POST", "/api/v1/projects/portfolio/cues"},
		{"GET", "/api/v1/cues/cue-def456"},
		{"PATCH", "/api/v1/cues/cue-def456"},
		{"DELETE", "/api/v1/cues/cue-def456"},
		{"POST", "/api/v1/cues/cue-def456/invoke"},
	} {
		body, status, _ := doRequest(t, srv, tc.method, tc.path, "")
		if status != http.StatusNotImplemented {
			t.Errorf("%s %s status = %d, want 501; body=%s", tc.method, tc.path, status, body)
		}
	}
}

func TestCuesAPI_BoundedSingleJSONBody(t *testing.T) {
	for _, route := range []struct {
		method, path string
		limit        int
	}{
		{"POST", "/api/v1/projects/portfolio/cues", 128 << 10},
		{"PATCH", "/api/v1/cues/cue-def456", 128 << 10},
		{"POST", "/api/v1/cues/cue-def456/invoke", 4 << 10},
	} {
		for _, tc := range []struct {
			name, body string
			status     int
		}{
			{"at cap", "{}" + strings.Repeat(" ", route.limit-2), 200},
			{"over cap", "{}" + strings.Repeat(" ", route.limit-1), 413},
			{"large field", `{"unused":"` + strings.Repeat("x", route.limit) + `"}`, 413},
			{"trailing JSON", "{} {}", 400},
			{"trailing garbage", "{} x", 400},
			{"malformed", "{", 400},
		} {
			t.Run(route.method+route.path+tc.name, func(t *testing.T) {
				svc := &fakeCueService{created: sampleCue(), updated: sampleCue(), invoked: "sess-1"}
				srv := newCueTestServer(t, svc)
				body, status, _ := doRequest(t, srv, route.method, route.path, tc.body)
				want := tc.status
				if want == 200 && route.path == "/api/v1/projects/portfolio/cues" {
					want = 201
				}
				if status != want {
					t.Fatalf("status=%d want=%d body=%s", status, want, body)
				}
				if want >= 400 {
					if svc.gotCueID != "" || svc.gotProject != "" {
						t.Fatal("invalid body dispatched")
					}
					if !strings.Contains(string(body), "INVALID_JSON") && !strings.Contains(string(body), "CUE_BODY_TOO_LARGE") {
						t.Fatalf("missing error envelope: %s", body)
					}
				}
			})
		}
	}
}
