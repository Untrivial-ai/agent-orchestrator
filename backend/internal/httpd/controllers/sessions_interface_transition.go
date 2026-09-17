package controllers

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
)

const interfaceTransitionPath = "/api/v1/sessions/{sessionId}/interface-transition"
const interfaceTransitionNoticeAcknowledgementPath = "/api/v1/sessions/{sessionId}/interface-transition/{transitionId}/notice-acknowledgement"

// sessionInterfaceTransitionService is optional so unrelated controller fakes
// keep implementing the stable SessionService surface.
type sessionInterfaceTransitionService interface {
	InterfaceTransitionStatus(context.Context, domain.SessionID) (sessionsvc.InterfaceTransitionStatus, error)
	StartInterfaceTransition(context.Context, domain.SessionID, domain.SessionMode, domain.SessionInterfaceTransitionPolicy, domain.SessionInterfaceTransitionHistoryPolicy) (domain.SessionInterfaceTransition, error)
	CancelInterfaceTransition(context.Context, domain.SessionID) error
	AcknowledgeInterfaceTransitionNotice(context.Context, domain.SessionID, string) (domain.SessionInterfaceTransition, error)
}

func (c *SessionsController) interfaceTransitionService() (sessionInterfaceTransitionService, bool) {
	service, ok := c.Svc.(sessionInterfaceTransitionService)
	return service, ok
}

func (c *SessionsController) interfaceTransitionStatus(w http.ResponseWriter, r *http.Request) {
	service, ok := c.interfaceTransitionService()
	if !ok {
		apispec.NotImplemented(w, r, http.MethodGet, interfaceTransitionPath)
		return
	}
	status, err := service.InterfaceTransitionStatus(r.Context(), sessionID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	response := SessionInterfaceTransitionStatusResponse{
		Supported: status.Supported, TargetMode: status.TargetMode,
		ReasonCode: status.ReasonCode, Reason: status.Reason,
	}
	if status.Transition != nil {
		view := sessionInterfaceTransitionView(*status.Transition)
		response.Transition = &view
	}
	envelope.WriteJSON(w, http.StatusOK, response)
}

func (c *SessionsController) startInterfaceTransition(w http.ResponseWriter, r *http.Request) {
	service, ok := c.interfaceTransitionService()
	if !ok {
		apispec.NotImplemented(w, r, http.MethodPost, interfaceTransitionPath)
		return
	}
	var in StartSessionInterfaceTransitionRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	historyPolicy := in.HistoryPolicy
	if historyPolicy == "" {
		historyPolicy = domain.SessionInterfaceTransitionHistoryStrict
	}
	transition, err := service.StartInterfaceTransition(r.Context(), sessionID(r), in.TargetMode, in.Policy, historyPolicy)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, StartSessionInterfaceTransitionResponse{
		OK: true, SessionID: sessionID(r), Transition: sessionInterfaceTransitionView(transition),
	})
}

func (c *SessionsController) cancelInterfaceTransition(w http.ResponseWriter, r *http.Request) {
	service, ok := c.interfaceTransitionService()
	if !ok {
		apispec.NotImplemented(w, r, http.MethodDelete, interfaceTransitionPath)
		return
	}
	if err := service.CancelInterfaceTransition(r.Context(), sessionID(r)); err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, CancelSessionInterfaceTransitionResponse{
		OK: true, SessionID: sessionID(r),
	})
}

func (c *SessionsController) acknowledgeInterfaceTransitionNotice(w http.ResponseWriter, r *http.Request) {
	service, ok := c.interfaceTransitionService()
	if !ok {
		apispec.NotImplemented(w, r, http.MethodPut, interfaceTransitionNoticeAcknowledgementPath)
		return
	}
	transition, err := service.AcknowledgeInterfaceTransitionNotice(
		r.Context(), sessionID(r), chi.URLParam(r, "transitionId"),
	)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, InterfaceTransitionNoticeAckResponse{
		OK: true, SessionID: sessionID(r), Transition: sessionInterfaceTransitionView(transition),
	})
}

func sessionInterfaceTransitionView(transition domain.SessionInterfaceTransition) SessionInterfaceTransitionView {
	if transition.HistoryPolicy == "" {
		transition.HistoryPolicy = domain.SessionInterfaceTransitionHistoryStrict
	}
	var completedAt *time.Time
	if !transition.CompletedAt.IsZero() {
		value := transition.CompletedAt
		completedAt = &value
	}
	var noticeAcknowledgedAt *time.Time
	if !transition.NoticeAcknowledgedAt.IsZero() {
		value := transition.NoticeAcknowledgedAt
		noticeAcknowledgedAt = &value
	}
	return SessionInterfaceTransitionView{
		ID: transition.ID, SessionID: transition.SessionID,
		SourceMode: transition.SourceMode, TargetMode: transition.TargetMode,
		Policy: transition.Policy, HistoryPolicy: transition.HistoryPolicy, Phase: transition.Phase,
		ErrorCode: transition.ErrorCode, ErrorDetail: transition.ErrorDetail,
		CreatedAt: transition.CreatedAt, UpdatedAt: transition.UpdatedAt,
		CompletedAt: completedAt, NoticeAcknowledgedAt: noticeAcknowledgedAt,
	}
}
