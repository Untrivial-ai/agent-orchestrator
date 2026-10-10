package controllers

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	testingsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/testing"
)

type testingComparisonFake struct {
	testingServiceFake
	base, head domain.TestRunID
	input      testingsvc.StartAttemptInput
}

func (f *testingComparisonFake) StartComparison(_ context.Context, base, head domain.TestRunID, in testingsvc.StartAttemptInput) (testingsvc.StartAttemptResult, error) {
	f.calls++
	f.base, f.head, f.input = base, head, in
	return testingsvc.StartAttemptResult{RunID: base, AttemptID: "pending-base", WorkerSessionID: "same-worker"}, f.fail
}
func TestTestingComparisonRoute(t *testing.T) {
	f := &testingComparisonFake{}
	route := "/api/v1/testing/comparisons"
	router := legRouter(f)
	w := testingRequest(router, http.MethodPost, route, `{"runId":"base","headRunId":"head","workerPrompt":"test diff","harness":"codex","timeoutSeconds":120}`, "")
	if w.Code != 201 || f.base != "base" || f.head != "head" || f.input.Timeout != 2*time.Minute || f.input.WorkerPrompt != "test diff" || f.input.Harness != domain.HarnessCodex || !strings.Contains(w.Body.String(), `"workerSessionId":"same-worker"`) {
		t.Fatal(w.Code, w.Body.String(), f.input)
	}
	before := f.calls
	for _, body := range []string{`{}`, `{"runId":"base","headRunId":"head","timeoutSeconds":-1}`, `{"runId":"base","headRunId":"head","timeoutSeconds":7201}`, `{"runId":"base","headRunId":"head","targetId":"foreign"}`} {
		w = testingRequest(router, http.MethodPost, route, body, "")
		if w.Code != 400 || f.calls != before {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	f.fail = apierr.Conflict("TEST_ATTEMPT_START_FAILED", "Cannot create", nil)
	w = testingRequest(router, http.MethodPost, route, `{"runId":"base","headRunId":"head","workerPrompt":"test"}`, "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "TEST_ATTEMPT_START_FAILED") {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, svc := range []TestingService{nil, &testingServiceFake{}} {
		w = testingRequest(legRouter(svc), http.MethodPost, route, `{}`, "")
		if w.Code != 503 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
