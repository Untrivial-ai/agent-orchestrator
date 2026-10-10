package controllers

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	testingsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/testing"
)

// TestingService exposes management and target-bound tools to loopback HTTP.
type TestingService interface {
	CreateRun(context.Context, testingsvc.CreateRunInput) (domain.TestRunRecord, error)
	StartAttempt(context.Context, domain.TestRunID, testingsvc.StartAttemptInput) (testingsvc.StartAttemptResult, error)
	Cancel(context.Context, domain.TestAttemptID) (domain.TestAttemptRecord, error)
	Execute(context.Context, domain.TestAttemptID, domain.SessionID, string, string, string, json.RawMessage) (testingsvc.ToolResult, error)
	ListEvidence(context.Context, domain.TestAttemptID) ([]domain.TestEvidenceReceipt, error)
}

// TestingController serves the testing API without provider knowledge.
type TestingController struct{ Svc TestingService }

// Register mounts the exact testing routes on the loopback router.
func (c *TestingController) Register(r chi.Router) {
	r.Route("/api/v1/testing", func(r chi.Router) {
		r.Use(testingContentTypeMiddleware)
		r.Post("/runs", c.create)
		r.Post("/runs/{runId}/attempts", c.start)
		r.Post("/attempts/{attemptId}/cancel", c.cancel)
		r.Get("/attempts/{attemptId}/evidence", c.evidence)
		for _, name := range testingsvc.ToolNames {
			r.Post("/attempts/{attemptId}/tools/"+name, func(w http.ResponseWriter, r *http.Request) { c.tool(w, r, name) })
		}
	})
}
func testingContentTypeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.Body != nil && r.Body != http.NoBody {
			mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mediaType != "application/json" {
				envelope.WriteAPIError(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "UNSUPPORTED_MEDIA_TYPE", "Content-Type must be application/json", nil)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func (c *TestingController) available(w http.ResponseWriter, r *http.Request) bool {
	if c.Svc != nil {
		return true
	}
	envelope.WriteError(w, r, testingsvc.ProviderNotConfigured())
	return false
}
func testingJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512*1024))
	dec.DisallowUnknownFields()
	if dec.Decode(out) != nil || dec.Decode(new(any)) != io.EOF {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return false
	}
	return true
}
func (c *TestingController) create(w http.ResponseWriter, r *http.Request) {
	if !c.available(w, r) {
		return
	}
	var in CreateTestingRunRequest
	if !testingJSON(w, r, &in) {
		return
	}
	if in.PRURL != "" {
		if in.IssueURL != "" || in.IssueSnapshot != "" || in.CommitSHA != "" || in.LinkedRunID != "" {
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_TESTING_REQUEST", "prUrl cannot be combined with manual issue, commit or linked-run fields", nil)
			return
		}
		intake, ok := c.Svc.(interface {
			CreatePullRequestRuns(context.Context, testingsvc.CreatePullRequestRunsInput) (testingsvc.PullRequestRuns, error)
		})
		if !ok {
			envelope.WriteError(w, r, testingsvc.ProviderNotConfigured())
			return
		}
		runs, err := intake.CreatePullRequestRuns(r.Context(), testingsvc.CreatePullRequestRunsInput{ProjectID: domain.ProjectID(in.ProjectID), PRURL: in.PRURL, RecipeID: in.RecipeID, Requester: in.Requester})
		if err != nil {
			envelope.WriteError(w, r, err)
			return
		}
		envelope.WriteJSON(w, http.StatusCreated, TestingRunResponse{RunID: string(runs.Base.ID), HeadRunID: string(runs.Head.ID), CreatedAt: runs.Base.CreatedAt})
		return
	}
	run, err := c.Svc.CreateRun(r.Context(), testingsvc.CreateRunInput{LinkedRunID: domain.TestRunID(in.LinkedRunID), ProjectID: domain.ProjectID(in.ProjectID), IssueURL: in.IssueURL, IssueSnapshot: in.IssueSnapshot, CommitSHA: in.CommitSHA, RecipeID: in.RecipeID, Requester: in.Requester})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, TestingRunResponse{RunID: string(run.ID), CreatedAt: run.CreatedAt})
}
func (c *TestingController) start(w http.ResponseWriter, r *http.Request) {
	if !c.available(w, r) {
		return
	}
	var in StartTestingAttemptRequest
	if !testingJSON(w, r, &in) {
		return
	}
	if in.TimeoutSeconds < 0 || in.TimeoutSeconds > 7200 {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_TESTING_REQUEST", "Invalid timeout", nil)
		return
	}
	result, err := c.Svc.StartAttempt(r.Context(), domain.TestRunID(chi.URLParam(r, "runId")), testingsvc.StartAttemptInput{Harness: domain.AgentHarness(in.Harness), Model: in.Model, Effort: in.Effort, WorkerPrompt: in.WorkerPrompt, Timeout: time.Duration(in.TimeoutSeconds) * time.Second})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, TestingAttemptStartResponse{RunID: string(result.RunID), AttemptID: string(result.AttemptID), WorkerSessionID: string(result.WorkerSessionID)})
}
func (c *TestingController) cancel(w http.ResponseWriter, r *http.Request) {
	if !c.available(w, r) {
		return
	}
	rec, err := c.Svc.Cancel(r.Context(), domain.TestAttemptID(chi.URLParam(r, "attemptId")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, TestingAttemptResponse{AttemptID: string(rec.ID), Phase: string(rec.Phase), Outcome: string(rec.Outcome), CleanupState: string(rec.CleanupState), RecordingGap: rec.RecordingGap})
}
func (c *TestingController) evidence(w http.ResponseWriter, r *http.Request) {
	if !c.available(w, r) {
		return
	}
	receipts, err := c.Svc.ListEvidence(r.Context(), domain.TestAttemptID(chi.URLParam(r, "attemptId")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	if receipts == nil {
		receipts = []domain.TestEvidenceReceipt{}
	}
	envelope.WriteJSON(w, http.StatusOK, TestingEvidenceResponse{Evidence: receipts})
}

// A bound worker whose controller/capability was lost at supervisor shutdown
// returns TEST_WORKER_NOT_RUNNING as HTTP 409 through the standard envelope.
func (c *TestingController) tool(w http.ResponseWriter, r *http.Request, name string) {
	if !c.available(w, r) {
		return
	}
	var in TestingToolRequest
	if !testingJSON(w, r, &in) {
		return
	}
	if in.SessionID == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_TESTING_REQUEST", "Worker session ID is required", nil)
		return
	}
	result, err := c.Svc.Execute(r.Context(), domain.TestAttemptID(chi.URLParam(r, "attemptId")), domain.SessionID(in.SessionID), r.Header.Get(testingsvc.CapabilityHeader), in.RequestID, name, in.Input)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, TestingToolResponse(result))
}
