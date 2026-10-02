package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/go-chi/chi/v5"
)

func parseApprovalAttempt(r *http.Request) (int, error) {
	attempt, err := strconv.Atoi(r.URL.Query().Get("attempt"))
	if err != nil || attempt <= 0 {
		return 0, strconv.ErrSyntax
	}
	return attempt, nil
}

func (s *Server) workerCreateChatApproval(w http.ResponseWriter, r *http.Request) {
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:turn:poll") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:turn:poll scope is required.")
		return
	}
	turnID := chi.URLParam(r, "turnId")
	var request worker.ChatApproval
	if requireUUID(turnID, "turnId") != nil || decodeJSONLimit(w, r, &request, maxWorkerControlBody) != nil ||
		requireUUID(request.RequestID, "requestId") != nil || request.TurnID != turnID || request.Attempt <= 0 ||
		strings.TrimSpace(request.Summary) == "" || len(request.Summary) > 4096 || len(request.Decisions) > 16384 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The approval request is invalid.")
		return
	}
	var options []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(request.Decisions, &options) != nil || len(options) == 0 || len(options) > 16 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The approval choices are invalid.")
		return
	}
	for _, option := range options {
		if option.ID == "" || len(option.ID) > 256 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "The approval choices are invalid.")
			return
		}
	}
	if err := s.store.CreateWorkerChatApproval(r.Context(), claims.OrgID, claims.SessionID, claims.WorkerID, claims.Epoch, request); err != nil {
		s.writeWorkerStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (s *Server) workerTurnCapabilities(w http.ResponseWriter, r *http.Request) {
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:turn:poll") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:turn:poll scope is required.")
		return
	}
	turnID := chi.URLParam(r, "turnId")
	var input struct {
		Attempt  int  `json:"attempt"`
		Steering bool `json:"steering"`
	}
	if requireUUID(turnID, "turnId") != nil || decodeJSONLimit(w, r, &input, maxWorkerControlBody) != nil || input.Attempt <= 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The turn capabilities are invalid.")
		return
	}
	if err := s.store.AppendWorkerTurnCapabilities(r.Context(), claims.OrgID, claims.SessionID, claims.WorkerID, turnID, claims.Epoch, input.Attempt, input.Steering); err != nil {
		s.writeWorkerStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (s *Server) workerChatApprovalDecision(w http.ResponseWriter, r *http.Request) {
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:turn:poll") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:turn:poll scope is required.")
		return
	}
	turnID, requestID := chi.URLParam(r, "turnId"), chi.URLParam(r, "requestId")
	if requireUUID(turnID, "turnId") != nil || requireUUID(requestID, "requestId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The approval identifiers are invalid.")
		return
	}
	attempt, err := parseApprovalAttempt(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	decision, err := s.store.WorkerChatApprovalDecision(r.Context(), claims.OrgID, claims.SessionID, claims.WorkerID, claims.Epoch, turnID, attempt, requestID)
	if err != nil {
		s.writeWorkerStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"decision": decision})
}

func (s *Server) decideChatApproval(w http.ResponseWriter, r *http.Request) {
	orgID, sessionID, requestID := chi.URLParam(r, "orgId"), chi.URLParam(r, "sessionId"), chi.URLParam(r, "requestId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(sessionID, "sessionId") != nil || requireUUID(requestID, "requestId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The approval identifiers are invalid.")
		return
	}
	var request struct {
		DecisionID string `json:"decisionId"`
	}
	if decodeJSON(w, r, &request) != nil || strings.TrimSpace(request.DecisionID) == "" || len(request.DecisionID) > 256 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The approval decision is invalid.")
		return
	}
	if err := s.store.DecideChatApproval(r.Context(), principalFrom(r), orgID, sessionID, requestID, request.DecisionID); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}
