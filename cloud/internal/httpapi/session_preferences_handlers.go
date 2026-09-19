package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/go-chi/chi/v5"
)

type sessionPreferencesStore interface {
	AvailableSessionReviewerHarnesses(context.Context, domain.Principal, string, string) ([]string, error)
	UpdateSessionPreferences(context.Context, domain.Principal, string, string, postgres.SessionPreferencesUpdate) (domain.Session, error)
}

type updateSessionPreferencesRequest struct {
	ReviewerHarness    *string `json:"reviewerHarness"`
	AutoInjectCI       *bool   `json:"autoInjectCI"`
	AutoInjectReview   *bool   `json:"autoInjectReview"`
	TerminateOnPRMerge *bool   `json:"terminateOnPrMerge"`
}

func (s *Server) updateSessionPreferences(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	sessionID := chi.URLParam(r, "sessionId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(sessionID, "sessionId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and sessionId must be UUIDs.")
		return
	}
	store, ok := s.store.(sessionPreferencesStore)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, "not_implemented", "Cloud session controls are unavailable.")
		return
	}
	var request updateSessionPreferencesRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The session controls request is invalid.")
		return
	}
	if request.ReviewerHarness == nil && request.AutoInjectCI == nil && request.AutoInjectReview == nil && request.TerminateOnPRMerge == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "At least one session control is required.")
		return
	}
	session, err := store.UpdateSessionPreferences(r.Context(), principalFrom(r), orgID, sessionID, postgres.SessionPreferencesUpdate{
		ReviewerHarness:    request.ReviewerHarness,
		AutoInjectCI:       request.AutoInjectCI,
		AutoInjectReview:   request.AutoInjectReview,
		TerminateOnPRMerge: request.TerminateOnPRMerge,
	})
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": toSessionResponse(session, nil)})
}
