package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/go-chi/chi/v5"
)

const (
	workerRequestLease       = 10 * time.Second
	maxWorkerTransportResult = 2 << 20
	maxTerminalFrame         = 16 << 10
	workerAgentTerminalTTL   = 24 * time.Hour
)

func (s *Server) workerEnsureAgentTerminal(w http.ResponseWriter, r *http.Request) {
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:transport") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:transport scope is required.")
		return
	}
	terminal, err := s.store.EnsureWorkerAgentTerminal(
		r.Context(), claims.OrgID, claims.SessionID, claims.WorkerID,
		claims.Epoch, workerAgentTerminalTTL,
	)
	if err != nil {
		s.writeWorkerTransportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, worker.AgentTerminalResponse{
		TerminalID: terminal.ID,
	})
}

func (s *Server) workerClaimTransport(w http.ResponseWriter, r *http.Request) {
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:transport") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:transport scope is required.")
		return
	}
	request, ok, err := s.store.ClaimWorkerRequest(
		r.Context(), claims.OrgID, claims.SessionID, claims.WorkerID,
		claims.Epoch, workerRequestLease,
	)
	if err != nil {
		s.writeWorkerStoreError(w, r, err)
		return
	}
	response := worker.ClaimTransportResponse{}
	if ok {
		response.Request = &worker.TransportRequest{
			ID: request.ID, Kind: request.Kind,
			Attempt: request.Attempt, Payload: request.Payload,
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) workerCompleteTransport(w http.ResponseWriter, r *http.Request) {
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:transport") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:transport scope is required.")
		return
	}
	requestID := chi.URLParam(r, "requestId")
	if requireUUID(requestID, "requestId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "requestId must be a UUID.")
		return
	}
	var input struct {
		Attempt  int             `json:"attempt"`
		Response json.RawMessage `json:"response"`
	}
	if err := decodeJSONLimit(w, r, &input, maxWorkerTransportResult); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var object map[string]any
	if len(input.Response) == 0 ||
		input.Attempt <= 0 ||
		json.Unmarshal(input.Response, &object) != nil ||
		object == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "response must be a JSON object.")
		return
	}
	if err := s.store.CompleteWorkerRequest(
		r.Context(), claims.OrgID, claims.SessionID, claims.WorkerID,
		requestID, claims.Epoch, input.Attempt, input.Response,
	); err != nil {
		s.writeWorkerTransportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) workerFailTransport(w http.ResponseWriter, r *http.Request) {
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:transport") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:transport scope is required.")
		return
	}
	requestID := chi.URLParam(r, "requestId")
	if requireUUID(requestID, "requestId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "requestId must be a UUID.")
		return
	}
	var input worker.FailTransportRequest
	if err := decodeJSONLimit(w, r, &input, maxWorkerControlBody); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	input.Code = strings.TrimSpace(input.Code)
	input.Message = strings.TrimSpace(input.Message)
	if input.Code == "" || len(input.Code) > 100 ||
		input.Attempt <= 0 ||
		input.Message == "" || len(input.Message) > maxWorkerError {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "The worker failure is invalid.")
		return
	}
	if err := s.store.FailWorkerRequest(
		r.Context(), claims.OrgID, claims.SessionID, claims.WorkerID,
		requestID, claims.Epoch, input.Attempt, input.Code, input.Message,
	); err != nil {
		s.writeWorkerTransportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) workerTerminalOutput(w http.ResponseWriter, r *http.Request) {
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:transport") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:transport scope is required.")
		return
	}
	terminalID := chi.URLParam(r, "terminalId")
	if requireUUID(terminalID, "terminalId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "terminalId must be a UUID.")
		return
	}
	var input worker.TerminalOutputRequest
	if err := decodeJSONLimit(w, r, &input, maxTerminalFrame*2); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(input.Data) == 0 || len(input.Data) > maxTerminalFrame {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "Terminal output must contain at most 16 KiB.")
		return
	}
	sequence, err := s.store.AppendTerminalOutput(
		r.Context(), claims.OrgID, claims.SessionID, claims.WorkerID,
		terminalID, claims.Epoch, input.Data,
	)
	if err != nil {
		s.writeWorkerTransportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"sequence": sequence})
}

func (s *Server) workerTerminalExit(w http.ResponseWriter, r *http.Request) {
	claims := workerFrom(r)
	if !worker.HasScope(claims, "worker:transport") {
		writeError(w, r, http.StatusForbidden, "SCOPE_REQUIRED", "The worker:transport scope is required.")
		return
	}
	terminalID := chi.URLParam(r, "terminalId")
	if requireUUID(terminalID, "terminalId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "terminalId must be a UUID.")
		return
	}
	var input worker.TerminalExitRequest
	if err := decodeJSONLimit(w, r, &input, maxWorkerControlBody); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := s.store.MarkTerminalExited(
		r.Context(), claims.OrgID, claims.SessionID, claims.WorkerID,
		terminalID, claims.Epoch, input.ExitCode,
	); err != nil {
		s.writeWorkerTransportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) writeWorkerTransportError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, postgres.ErrTransportExpired) {
		writeError(w, r, http.StatusConflict, "TRANSPORT_EXPIRED", "The worker request is no longer active.")
		return
	}
	s.writeWorkerStoreError(w, r, err)
}
