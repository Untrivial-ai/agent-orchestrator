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
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	testingsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/testing"
)

type testingIntakeFake struct {
	testingServiceFake
	intakeCalls int
	input       testingsvc.CreatePullRequestRunsInput
}

func (f *testingIntakeFake) CreatePullRequestRuns(_ context.Context, in testingsvc.CreatePullRequestRunsInput) (testingsvc.PullRequestRuns, error) {
	f.intakeCalls++
	f.input = in
	return testingsvc.PullRequestRuns{Base: domain.TestRunRecord{ID: "base", CreatedAt: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)}, Head: domain.TestRunRecord{ID: "head"}}, f.fail
}
func TestTestingURLIntakeCreatesComparisonRunsWithoutLaunching(t *testing.T) {
	f := &testingIntakeFake{}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/testing/runs", strings.NewReader(`{"projectId":"ao","prUrl":"https://github.com/o/r/pull/1","recipeId":"local-ao","requester":"worker"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	testingRouter(f).ServeHTTP(response, request)
	var result TestingRunResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != 201 || result.RunID != "base" || result.HeadRunID != "head" || f.intakeCalls != 1 || f.calls != 0 || f.input.PRURL != "https://github.com/o/r/pull/1" {
		t.Fatalf("intake response: %d %s %+v", response.Code, response.Body.String(), f.input)
	}
}
func TestTestingURLIntakeRejectsMixedManualFields(t *testing.T) {
	for _, field := range []string{"issueUrl", "issueSnapshot", "commitSha", "linkedRunId"} {
		t.Run(field, func(t *testing.T) {
			f := &testingIntakeFake{}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/testing/runs", strings.NewReader(`{"projectId":"ao","prUrl":"https://github.com/o/r/pull/1","`+field+`":"manual"}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			testingRouter(f).ServeHTTP(response, request)
			if response.Code != 400 || !strings.Contains(response.Body.String(), "INVALID_TESTING_REQUEST") || f.intakeCalls != 0 || f.calls != 0 {
				t.Fatalf("mixed input admitted: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestTestingURLIntakePreservesDaemonErrorEnvelope(t *testing.T) {
	f := &testingIntakeFake{testingServiceFake: testingServiceFake{fail: apierr.Invalid("TEST_PR_INTAKE_FAILED", "gh: fork permission denied", nil)}}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/testing/runs", strings.NewReader(`{"projectId":"ao","prUrl":"https://github.com/o/r/pull/1"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	testingRouter(f).ServeHTTP(response, request)
	if response.Code != 400 || !strings.Contains(response.Body.String(), "TEST_PR_INTAKE_FAILED") || !strings.Contains(response.Body.String(), "fork permission denied") || !strings.Contains(response.Body.String(), "requestId") {
		t.Fatalf("intake cause/envelope lost: %d %s", response.Code, response.Body.String())
	}
}
