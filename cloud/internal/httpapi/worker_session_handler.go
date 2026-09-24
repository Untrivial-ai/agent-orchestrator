package httpapi

import (
	"net/http"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

func (s *Server) workerSession(w http.ResponseWriter, r *http.Request) {
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:session:read") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:session:read scope is required.")
		return
	}
	id, err := s.store.WorkerAgentSessionID(r.Context(), claims.OrgID, claims.SessionID, claims.WorkerID, claims.Epoch)
	if err != nil {
		s.writeWorkerStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"agentSessionId": id})
}
