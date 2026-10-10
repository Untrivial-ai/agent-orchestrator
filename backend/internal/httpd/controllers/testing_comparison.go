package controllers

import (
	"context"
	"net/http"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	testingsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/testing"
)

// StartTestingComparisonRequest attaches one worker to two pinned runs.
type StartTestingComparisonRequest struct {
	StartTestingAttemptRequest
	RunID     string `json:"runId" minLength:"1"`
	HeadRunID string `json:"headRunId" minLength:"1"`
}

type testingComparisonService interface {
	StartComparison(context.Context, domain.TestRunID, domain.TestRunID, testingsvc.StartAttemptInput) (testingsvc.StartAttemptResult, error)
}

func (c *TestingController) startComparison(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.Svc.(testingComparisonService)
	if !ok {
		envelope.WriteError(w, r, testingsvc.ProviderNotConfigured())
		return
	}
	var in StartTestingComparisonRequest
	if !testingJSON(w, r, &in) {
		return
	}
	if in.RunID == "" || in.HeadRunID == "" || in.TimeoutSeconds < 0 || in.TimeoutSeconds > 7200 {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_TESTING_REQUEST", "Both run IDs and a valid timeout are required", nil)
		return
	}
	result, err := svc.StartComparison(r.Context(), domain.TestRunID(in.RunID), domain.TestRunID(in.HeadRunID), testingsvc.StartAttemptInput{Harness: domain.AgentHarness(in.Harness), Model: in.Model, Effort: in.Effort, WorkerPrompt: in.WorkerPrompt, Timeout: time.Duration(in.TimeoutSeconds) * time.Second})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, TestingAttemptStartResponse{RunID: string(result.RunID), AttemptID: string(result.AttemptID), WorkerSessionID: string(result.WorkerSessionID)})
}
