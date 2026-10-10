package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	testingsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/testing"
)

type testingLegFake struct {
	testingServiceFake
	session domain.SessionID
	leg     string
}

func (f *testingLegFake) StartLeg(_ context.Context, session domain.SessionID, leg string) (testingsvc.StartLegResult, error) {
	f.calls++
	f.session, f.leg = session, leg
	return testingsvc.StartLegResult{StartAttemptResult: testingsvc.StartAttemptResult{RunID: "run", AttemptID: "attempt", WorkerSessionID: session}, Leg: leg, CommitSHA: "pinned-sha", EvidenceDir: "/evidence"}, f.fail
}
func (f *testingLegFake) ExecuteCurrent(ctx context.Context, session domain.SessionID, token, request, name string, input json.RawMessage) (testingsvc.ToolResult, error) {
	return f.Execute(ctx, "", session, token, request, name, input)
}
func legRouter(f TestingService) http.Handler {
	r := chi.NewRouter()
	(&TestingController{Svc: f}).RegisterLegs(r)
	return r
}
func TestTestingLegAndSessionToolRoutes(t *testing.T) {
	f := &testingLegFake{}
	router := legRouter(f)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/testing/sessions/worker/legs/head/start", nil)
	empty := httptest.NewRecorder()
	router.ServeHTTP(empty, req)
	if empty.Code != http.StatusCreated || f.calls != 1 || f.session != "worker" || f.leg != "head" {
		t.Fatal("bodyless leg request failed", empty.Code, empty.Body.String())
	}
	w := testingRequest(router, http.MethodPost, "/api/v1/testing/sessions/worker/legs/head/start", `{}`, "")
	if w.Code != 201 || f.session != "worker" || f.leg != "head" || !strings.Contains(w.Body.String(), `"commitSha":"pinned-sha"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, name := range testingsvc.ToolNames {
		w = testingRequest(router, http.MethodPost, "/api/v1/testing/sessions/worker/tools/"+name, `{"sessionId":"worker","requestId":"req","input":{}}`, "capability")
		if w.Code != 200 || f.tool != name || f.request != "req" {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	before := f.calls
	for _, body := range []string{`{"sessionId":"foreign","requestId":"req","input":{}}`, `{"sessionId":"worker","requestId":"req","input":{},"attemptId":"stale"}`} {
		w = testingRequest(router, http.MethodPost, "/api/v1/testing/sessions/worker/tools/observe", body, "capability")
		if w.Code != 400 || f.calls != before {
			t.Fatal("foreign target admitted", w.Code, w.Body.String())
		}
	}
	f.fail = apierr.Conflict("TEST_LEG_CLEANUP_FAILED", "Previous target remains", nil)
	w = testingRequest(router, http.MethodPost, "/api/v1/testing/sessions/worker/legs/head/start", `{}`, "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "TEST_LEG_CLEANUP_FAILED") {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestTestingLegRoutesFailClosedWithoutProviderAndRejectContentType(t *testing.T) {
	for _, svc := range []TestingService{nil, &testingServiceFake{}} {
		w := testingRequest(legRouter(svc), http.MethodPost, "/api/v1/testing/sessions/worker/legs/base/start", `{}`, "")
		if w.Code != 503 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	f := &testingLegFake{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/testing/sessions/worker/legs/base/start", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	legRouter(f).ServeHTTP(w, req)
	if w.Code != 415 || f.calls != 0 {
		t.Fatal("non-JSON leg admitted", w.Code)
	}
}
