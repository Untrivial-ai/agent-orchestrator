package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/go-chi/chi/v5"
)

var supportedCloudReviewerHarnesses = []string{"claude-code", "codex", "cursor"}

func (s *Server) inspectSessionReviewerHarnesses(w http.ResponseWriter, r *http.Request) {
	orgID, sessionID, ok := workspaceRoute(w, r)
	if !ok {
		return
	}
	payload, _ := json.Marshal(worker.HarnessInspectRequest{Harnesses: supportedCloudReviewerHarnesses})
	result, ok := s.runWorkerRequest(w, r, orgID, sessionID, "harness.inspect", payload, 20*time.Second)
	if !ok {
		return
	}
	var response worker.HarnessInspectResponse
	if json.Unmarshal(result, &response) != nil {
		writeError(w, r, http.StatusBadGateway, "INVALID_WORKER_RESPONSE", "The worker returned invalid reviewer availability.")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) installSessionReviewerHarness(w http.ResponseWriter, r *http.Request) {
	orgID, sessionID, ok := workspaceRoute(w, r)
	if !ok {
		return
	}
	harness := chi.URLParam(r, "harness")
	if !containsString(supportedCloudReviewerHarnesses, harness) {
		writeError(w, r, http.StatusUnprocessableEntity, "UNSUPPORTED_REVIEWER_HARNESS", "Cloud reviewers support only Claude, Cursor, and Codex.")
		return
	}
	if err := s.store.CheckSessionWriteAccess(r.Context(), principalFrom(r), orgID, sessionID); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	payload, _ := json.Marshal(worker.HarnessInstallRequest{Harness: harness})
	result, ok := s.runWorkerRequest(w, r, orgID, sessionID, "harness.install", payload, 3*time.Minute)
	if !ok {
		return
	}
	var status worker.HarnessStatus
	if json.Unmarshal(result, &status) != nil || status.Harness != harness {
		writeError(w, r, http.StatusBadGateway, "INVALID_WORKER_RESPONSE", "The worker returned an invalid harness installation result.")
		return
	}
	writeJSON(w, http.StatusOK, status)
}
