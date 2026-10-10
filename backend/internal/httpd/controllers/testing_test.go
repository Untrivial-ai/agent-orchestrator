package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	testingsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/testing"
)

type testingServiceFake struct {
	tool, token, request string
	input                json.RawMessage
	fail                 error
	calls                int
	startInput           testingsvc.StartAttemptInput
}

func (f *testingServiceFake) CreateRun(_ context.Context, in testingsvc.CreateRunInput) (domain.TestRunRecord, error) {
	f.calls++
	return domain.TestRunRecord{ID: "run", ProjectID: in.ProjectID, CreatedAt: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)}, f.fail
}
func (f *testingServiceFake) StartAttempt(_ context.Context, id domain.TestRunID, in testingsvc.StartAttemptInput) (testingsvc.StartAttemptResult, error) {
	f.calls++
	f.startInput = in
	return testingsvc.StartAttemptResult{RunID: id, AttemptID: "attempt", WorkerSessionID: "worker"}, f.fail
}
func (f *testingServiceFake) Cancel(_ context.Context, id domain.TestAttemptID) (domain.TestAttemptRecord, error) {
	f.calls++
	return domain.TestAttemptRecord{ID: id, Phase: domain.TestAttemptFinished, Outcome: domain.TestOutcomeCancelled, CleanupState: domain.TestCleanupPending, RecordingGap: "no video"}, f.fail
}
func (f *testingServiceFake) Execute(_ context.Context, _ domain.TestAttemptID, session domain.SessionID, token, request, name string, input json.RawMessage) (testingsvc.ToolResult, error) {
	f.calls++
	f.tool = name
	f.token = token
	f.request = request
	f.input = input
	if session != "worker" || token != "capability" {
		return testingsvc.ToolResult{}, apierr.Forbidden("INVALID_TEST_CAPABILITY", "Wrong capability")
	}
	return testingsvc.ToolResult{Action: &domain.TestActionResult{Delivered: true}, Evidence: []domain.TestEvidenceReceipt{}}, f.fail
}
func (f *testingServiceFake) ListEvidence(_ context.Context, _ domain.TestAttemptID) ([]domain.TestEvidenceReceipt, error) {
	f.calls++
	return nil, f.fail
}
func testingRouter(f TestingService) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	(&TestingController{Svc: f}).Register(r)
	return r
}
func testingRequest(h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set(testingsvc.CapabilityHeader, token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestTestingManagementAndSevenExactToolRoutes(t *testing.T) {
	f := &testingServiceFake{}
	router := testingRouter(f)
	for _, test := range []struct {
		method, path, body string
		status             int
	}{{http.MethodPost, "/api/v1/testing/runs", `{"projectId":"ao","issueUrl":"url","issueSnapshot":"issue","commitSha":"abc","recipeId":"native","requester":"maintainer"}`, 201}, {http.MethodPost, "/api/v1/testing/runs/run/attempts", `{"workerPrompt":"investigate","timeoutSeconds":60}`, 201}, {http.MethodPost, "/api/v1/testing/attempts/attempt/cancel", "", 200}, {http.MethodGet, "/api/v1/testing/attempts/attempt/evidence", "", 200}} {
		w := testingRequest(router, test.method, test.path, test.body, "")
		if w.Code != test.status {
			t.Fatalf("%s: %d %s", test.path, w.Code, w.Body.String())
		}
	}
	w := testingRequest(router, http.MethodGet, "/api/v1/testing/attempts/attempt/evidence", "", "")
	if !strings.Contains(w.Body.String(), `"evidence":[]`) {
		t.Fatal("nil evidence array reached wire")
	}
	for _, name := range testingsvc.ToolNames {
		w := testingRequest(router, http.MethodPost, "/api/v1/testing/attempts/attempt/tools/"+name, `{"sessionId":"worker","requestId":"unique","input":{}}`, "capability")
		if w.Code != 200 || f.tool != name || f.token != "capability" || f.request != "unique" || string(f.input) != "{}" {
			t.Fatal("tool forwarding", name, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "capability") {
			t.Fatal("capability leaked in response")
		}
	}
	w = testingRequest(router, http.MethodPost, "/api/v1/testing/attempts/attempt/tools/other", `{}`, "capability")
	if w.Code != 404 {
		t.Fatal("unknown tool route registered")
	}
}
func TestTestingErrorsStrictBodiesAndProviderMissing(t *testing.T) {
	for _, path := range []string{"/api/v1/testing/runs", "/api/v1/testing/runs/run/attempts", "/api/v1/testing/attempts/attempt/cancel", "/api/v1/testing/attempts/attempt/tools/screenshot"} {
		w := testingRequest(testingRouter(nil), http.MethodPost, path, `{}`, "")
		if w.Code != 503 || !strings.Contains(w.Body.String(), "TESTING_PROVIDER_NOT_CONFIGURED") || !strings.Contains(w.Body.String(), "requestId") {
			t.Fatal("missing provider envelope", w.Code, w.Body.String())
		}
	}
	f := &testingServiceFake{}
	router := testingRouter(f)
	for _, body := range []string{`{"sessionId":"worker","requestId":"id","input":{},"target":"host"}`, `{"sessionId":"worker","requestId":"id","input":{}} {}`, `null`, `{"sessionId":"","requestId":"id","input":{}}`} {
		before := f.calls
		w := testingRequest(router, http.MethodPost, "/api/v1/testing/attempts/attempt/tools/screenshot", body, "capability")
		if w.Code != 400 || f.calls != before {
			t.Fatal("bad body admitted", w.Code, w.Body.String())
		}
	}
	w := testingRequest(router, http.MethodPost, "/api/v1/testing/attempts/attempt/tools/screenshot", `{"sessionId":"worker","requestId":"id","input":{}}`, "")
	if w.Code != 403 || !strings.Contains(w.Body.String(), "INVALID_TEST_CAPABILITY") {
		t.Fatal("header was optional")
	}
	for _, test := range []struct {
		err    error
		status int
	}{{apierr.Conflict("TEST_TARGET_CHANGED", "changed", nil), 409}, {apierr.Internal("TEST_EVIDENCE_WRITE_FAILED", "disk full"), 500}, {apierr.NotFound("TEST_ATTEMPT_NOT_FOUND", "unknown"), 404}} {
		f.fail = test.err
		w := testingRequest(router, http.MethodGet, "/api/v1/testing/attempts/attempt/evidence", "", "")
		if w.Code != test.status || !strings.Contains(w.Body.String(), "requestId") {
			t.Fatal("service error envelope", w.Code, w.Body.String())
		}
	}
}

func TestTestingPostBodiesRequireJSONContentType(t *testing.T) {
	paths := []string{"/api/v1/testing/runs", "/api/v1/testing/runs/run/attempts", "/api/v1/testing/attempts/attempt/cancel"}
	for _, name := range testingsvc.ToolNames {
		paths = append(paths, "/api/v1/testing/attempts/attempt/tools/"+name)
	}
	for _, path := range paths {
		for _, contentType := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=test", "application/json; charset"} {
			f := &testingServiceFake{}
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", contentType)
			w := httptest.NewRecorder()
			testingRouter(f).ServeHTTP(w, req)
			if w.Code != http.StatusUnsupportedMediaType || f.calls != 0 || !strings.Contains(w.Body.String(), "UNSUPPORTED_MEDIA_TYPE") {
				t.Fatalf("%s content type %q: %d %s, calls %d", path, contentType, w.Code, w.Body.String(), f.calls)
			}
		}
	}
	f := &testingServiceFake{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/testing/runs", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	w := httptest.NewRecorder()
	testingRouter(f).ServeHTTP(w, req)
	if w.Code != http.StatusCreated || f.calls != 1 {
		t.Fatalf("JSON with charset rejected: %d %s", w.Code, w.Body.String())
	}
}

func TestTestingWorkerErrorsKeepCodeCauseAndRequestID(t *testing.T) {
	for _, tc := range []struct {
		path, body, code, message string
		err                       error
		status                    int
	}{
		{path: "/api/v1/testing/runs/run/attempts", body: `{"workerPrompt":"investigate"}`, code: "TEST_WORKER_START_FAILED", message: "DEFAULT_BRANCH_UNRESOLVED: Scratch repository has no default branch", err: apierr.Unavailable("TEST_WORKER_START_FAILED", "Investigator worker start failed: DEFAULT_BRANCH_UNRESOLVED: Scratch repository has no default branch"), status: 503},
		{path: "/api/v1/testing/attempts/attempt/tools/screenshot", body: `{"sessionId":"worker","requestId":"shot","input":{}}`, code: "TEST_WORKER_NOT_RUNNING", message: "supervisor shutdown", err: testingsvc.WorkerNotRunning(), status: 409},
	} {
		t.Run(tc.code, func(t *testing.T) {
			w := testingRequest(testingRouter(&testingServiceFake{fail: tc.err}), http.MethodPost, tc.path, tc.body, "capability")
			var result struct {
				Code, Message, RequestID string
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != tc.status || result.Code != tc.code || !strings.Contains(result.Message, tc.message) || result.RequestID == "" {
				t.Fatal("testing worker error lost its API details", w.Code, w.Body.String(), err)
			}
		})
	}
}

func TestTestingStartForwardsExplicitInvestigatorProfile(t *testing.T) {
	f := &testingServiceFake{}
	w := testingRequest(testingRouter(f), http.MethodPost, "/api/v1/testing/runs/run/attempts", `{"harness":"claude-code","model":"claude-opus-5-5","effort":"medium","workerPrompt":"inspect","timeoutSeconds":60}`, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if f.startInput.Harness != domain.HarnessClaudeCode || f.startInput.Model != "claude-opus-5-5" || f.startInput.Effort != "medium" {
		t.Fatalf("lost investigator profile: %+v", f.startInput)
	}
}
