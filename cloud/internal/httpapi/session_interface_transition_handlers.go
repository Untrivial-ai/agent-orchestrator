package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/go-chi/chi/v5"
)

type interfaceTransitionResponse struct {
	ID                   string     `json:"id"`
	SessionID            string     `json:"sessionId"`
	SourceMode           string     `json:"sourceMode"`
	TargetMode           string     `json:"targetMode"`
	Policy               string     `json:"policy"`
	Phase                string     `json:"phase"`
	NativeConversationID string     `json:"nativeConversationId,omitempty"`
	ErrorCode            string     `json:"errorCode,omitempty"`
	ErrorDetail          string     `json:"errorDetail,omitempty"`
	NoticeAcknowledgedAt *time.Time `json:"noticeAcknowledgedAt,omitempty"`
	CreatedAt            time.Time  `json:"createdAt"`
	UpdatedAt            time.Time  `json:"updatedAt"`
	CompletedAt          time.Time  `json:"completedAt,omitempty"`
}

type interfaceTransitionStatusResponse struct {
	Supported  bool                         `json:"supported"`
	TargetMode string                       `json:"targetMode"`
	ReasonCode string                       `json:"reasonCode,omitempty"`
	Reason     string                       `json:"reason,omitempty"`
	Transition *interfaceTransitionResponse `json:"transition,omitempty"`
}

type startInterfaceTransitionRequest struct {
	TargetMode string `json:"targetMode"`
	Policy     string `json:"policy"`
}

func (s *Server) interfaceTransitionSupported(harness string) bool {
	switch harness {
	case "claude-code", "codex", "cursor":
		return true
	default:
		return false
	}
}

// getSessionInterfaceTransition reports whether the session's harness can
// switch interfaces plus the latest durable attempt. Read-only: it never
// launches a provider process.
func (s *Server) getSessionInterfaceTransition(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	sessionID := chi.URLParam(r, "sessionId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(sessionID, "sessionId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and sessionId must be UUIDs.")
		return
	}
	session, err := s.store.GetSession(r.Context(), principalFrom(r), orgID, sessionID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	status := interfaceTransitionStatusResponse{
		Supported:  s.interfaceTransitionSupported(session.Harness),
		TargetMode: string(session.Interface.Opposite()),
	}
	if session.IsTerminated {
		status.ReasonCode = "SESSION_TERMINATED"
		status.Reason = "Terminated sessions must be restored before switching interfaces."
	} else if !status.Supported {
		status.ReasonCode = "INTERFACE_HANDOFF_UNSUPPORTED"
		status.Reason = session.Harness + " does not support interface handoff."
	}
	if transition, found, err := s.store.GetLatestRelevantSessionInterfaceTransition(
		r.Context(), principalFrom(r), orgID, sessionID,
	); err != nil {
		s.writeStoreError(w, r, err)
		return
	} else if found {
		rendered := toInterfaceTransitionResponse(transition)
		status.Transition = &rendered
	}
	writeJSON(w, http.StatusOK, status)
}

// startSessionInterfaceTransition durably claims a session for an interface
// handoff and starts it in the background. The caller receives the transition
// immediately; long-running drain mode does not depend on an HTTP timeout.
func (s *Server) startSessionInterfaceTransition(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	sessionID := chi.URLParam(r, "sessionId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(sessionID, "sessionId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and sessionId must be UUIDs.")
		return
	}
	var input startInterfaceTransitionRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	target := domain.SessionInterface(input.TargetMode)
	if !target.Valid() || target.Normalized() != target {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "targetMode must be tui or chat.")
		return
	}
	policy := domain.SessionInterfaceTransitionPolicy(input.Policy)
	if !policy.Valid() {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", "policy must be drain or interrupt.")
		return
	}
	session, err := s.store.GetSession(r.Context(), principalFrom(r), orgID, sessionID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if !s.interfaceTransitionSupported(session.Harness) {
		writeError(w, r, http.StatusUnprocessableEntity, "INTERFACE_HANDOFF_UNSUPPORTED",
			session.Harness+" does not support interface handoff.")
		return
	}
	if target == session.Interface {
		writeError(w, r, http.StatusConflict, "INTERFACE_ALREADY_SELECTED",
			"The session is already in "+string(target)+" mode.")
		return
	}
	transition, err := s.store.StartSessionInterfaceTransition(
		r.Context(), principalFrom(r), orgID, sessionID,
		session.Interface, target, policy, "",
	)
	if errors.Is(err, postgres.ErrTransitionInProgress) {
		writeError(w, r, http.StatusConflict, "INTERFACE_TRANSITION_IN_PROGRESS",
			"An interface switch is already in progress for this session.")
		return
	}
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"transition": toInterfaceTransitionResponse(transition),
	})
}

// cancelSessionInterfaceTransition cancels an in-progress handoff while its
// source controller is still intact.
func (s *Server) cancelSessionInterfaceTransition(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	sessionID := chi.URLParam(r, "sessionId")
	if requireUUID(orgID, "orgId") != nil || requireUUID(sessionID, "sessionId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId and sessionId must be UUIDs.")
		return
	}
	transition, found, err := s.store.GetActiveSessionInterfaceTransition(
		r.Context(), principalFrom(r), orgID, sessionID,
	)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if !found {
		writeError(w, r, http.StatusConflict, "INTERFACE_TRANSITION_NOT_FOUND",
			"There is no interface switch to cancel.")
		return
	}
	switch transition.Phase {
	case domain.SessionInterfaceTransitionRequested,
		domain.SessionInterfaceTransitionPreflighting,
		domain.SessionInterfaceTransitionDraining:
	default:
		writeError(w, r, http.StatusConflict, "INTERFACE_TRANSITION_NOT_CANCELLABLE",
			"The interface switch can no longer be cancelled.")
		return
	}
	if err := s.store.AdvanceSessionInterfaceTransition(
		r.Context(), principalFrom(r), orgID, transition.ID, transition.Phase,
		domain.SessionInterfaceTransitionCancelled, "", "TRANSITION_CANCELLED",
		"The interface switch was cancelled.",
	); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// acknowledgeSessionInterfaceTransitionNotice records that a failed or
// recovered handoff notice has been seen without deleting its audit history.
func (s *Server) acknowledgeSessionInterfaceTransitionNotice(w http.ResponseWriter, r *http.Request) {
	orgID := chi.URLParam(r, "orgId")
	sessionID := chi.URLParam(r, "sessionId")
	transitionID := chi.URLParam(r, "transitionId")
	if requireUUID(orgID, "orgId") != nil ||
		requireUUID(sessionID, "sessionId") != nil ||
		requireUUID(transitionID, "transitionId") != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "orgId, sessionId, and transitionId must be UUIDs.")
		return
	}
	if err := s.store.AcknowledgeSessionInterfaceTransitionNotice(
		r.Context(), principalFrom(r), orgID, sessionID, transitionID,
	); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func toInterfaceTransitionResponse(transition domain.SessionInterfaceTransition) interfaceTransitionResponse {
	var completedAt time.Time
	if transition.CompletedAt != nil {
		completedAt = *transition.CompletedAt
	}
	return interfaceTransitionResponse{
		ID:                   transition.ID,
		SessionID:            transition.SessionID,
		SourceMode:           string(transition.SourceInterface),
		TargetMode:           string(transition.TargetInterface),
		Policy:               string(transition.Policy),
		Phase:                string(transition.Phase),
		NativeConversationID: transition.NativeConversationID,
		ErrorCode:            transition.ErrorCode,
		ErrorDetail:          transition.ErrorDetail,
		NoticeAcknowledgedAt: transition.NoticeAcknowledgedAt,
		CreatedAt:            transition.CreatedAt,
		UpdatedAt:            transition.UpdatedAt,
		CompletedAt:          completedAt,
	}
}
