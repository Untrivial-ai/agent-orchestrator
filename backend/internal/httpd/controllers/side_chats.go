package controllers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

func (c *ConversationsController) sideService(w http.ResponseWriter, r *http.Request) (sideChatConversationService, bool) {
	svc, ok := c.Svc.(sideChatConversationService)
	if !ok {
		apispec.NotImplemented(w, r, r.Method, r.URL.Path)
	}
	return svc, ok
}

func sideSession(r *http.Request) domain.SessionID {
	return domain.SessionID(chi.URLParam(r, "sessionId"))
}
func sideID(r *http.Request) string { return chi.URLParam(r, "sideId") }

func writeSideChatError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, chatsvc.ErrSideNotReady):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "CHAT_SIDE_NOT_READY", err.Error(), nil)
	case errors.Is(err, chatsvc.ErrSideUnavailable), errors.Is(err, chatsvc.ErrSideLaunchUnclaimed):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "CHAT_SIDE_UNAVAILABLE", err.Error(), nil)
	case errors.Is(err, chatsvc.ErrSideProviderUnsupported), errors.Is(err, chatsvc.ErrForkUnsupported):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "CHAT_SIDE_UNSUPPORTED", err.Error(), nil)
	case errors.Is(err, chatsvc.ErrSideAnchorUnavailable):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "CHAT_SIDE_ANCHOR_UNAVAILABLE", err.Error(), nil)
	case errors.Is(err, chatsvc.ErrSideClosed):
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "CHAT_SIDE_CLOSED", err.Error(), nil)
	case errors.Is(err, chatsvc.ErrSideIdempotencyConflict):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "CHAT_SIDE_IDEMPOTENCY_CONFLICT", err.Error(), nil)
	case errors.Is(err, chatsvc.ErrSideCreateKeyRequired), errors.Is(err, chatsvc.ErrSideQuestionInvalid), errors.Is(err, chatsvc.ErrSideDraftTooLarge):
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", "CHAT_SIDE_INVALID_REQUEST", err.Error(), nil)
	default:
		writeConversationError(w, r, err)
	}
}

func (c *ConversationsController) listSideChats(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.sideService(w, r)
	if !ok {
		return
	}
	sides, err := svc.ListIndependentSideChats(r.Context(), sideSession(r))
	if err != nil {
		writeSideChatError(w, r, err)
		return
	}
	if sides == nil {
		sides = []domain.SideConversation{}
	}
	envelope.WriteJSON(w, http.StatusOK, SideChatListResponse{Sides: sides})
}

func (c *ConversationsController) sideSnapshot(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.sideService(w, r)
	if !ok {
		return
	}
	var before time.Time
	if raw := r.URL.Query().Get("before"); raw != "" {
		var err error
		before, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", "CHAT_SIDE_CURSOR_INVALID", err.Error(), nil)
			return
		}
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 500 {
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", "CHAT_SIDE_LIMIT_INVALID", "limit must be between 1 and 500", nil)
			return
		}
		limit = n
	}
	snapshot, err := svc.SideSnapshot(r.Context(), sideSession(r), sideID(r), before, limit)
	if err != nil {
		writeSideChatError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, SideChatSnapshotResponse{Snapshot: snapshot})
}

func (c *ConversationsController) streamSideChat(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.sideService(w, r)
	if !ok {
		return
	}
	generation, changes, cancel, err := svc.WatchSideChat(r.Context(), sideSession(r), sideID(r))
	if err != nil {
		writeSideChatError(w, r, err)
		return
	}
	defer cancel()
	flusher, ok := w.(http.Flusher)
	if !ok {
		envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal", "CHAT_SIDE_STREAM_UNAVAILABLE", "streaming is unavailable", nil)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	payload, _ := json.Marshal(map[string]string{"sideId": sideID(r), "generation": generation})
	write := func(kind string) bool {
		_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, payload)
		if err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !write("snapshot") {
		return
	}
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-changes:
			if !write("changed") {
				return
			}
		case <-ticker.C:
			if !write("heartbeat") {
				return
			}
		}
	}
}

func (c *ConversationsController) sendSideQuestion(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.sideService(w, r)
	if !ok {
		return
	}
	var req SendSideQuestionRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}
	content, attachmentErr := conversationContent(SendConversationMessageRequest{Attachments: req.Attachments, Resources: req.Resources})
	if attachmentErr != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", attachmentErr.code, attachmentErr.message, nil)
		return
	}
	excerpts := make([]ports.ChatExcerptReference, 0, len(req.References))
	for _, ref := range req.References {
		excerpts = append(excerpts, ports.ChatExcerptReference{ConversationID: ref.ConversationID, MessageID: ref.MessageID, Revision: ref.Revision, Text: ref.Text})
	}
	turn, err := svc.SendSideQuestion(r.Context(), sideSession(r), sideID(r), ports.ChatUserMessage{
		Text: req.Text, ClientMessageID: req.ClientMessageID, Content: content, Excerpts: excerpts, Origin: domain.MessageOriginHuman})
	if err != nil {
		writeSideChatError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, SideQuestionResponse{Turn: turn})
}

func (c *ConversationsController) editSideQuestion(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.sideService(w, r)
	if !ok {
		return
	}
	var req SendSideQuestionRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}
	if err := svc.EditQueuedSideQuestion(r.Context(), sideSession(r), sideID(r), chi.URLParam(r, "turnId"), req.Text); err != nil {
		writeSideChatError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *ConversationsController) retrySideQuestion(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.sideService(w, r)
	if !ok {
		return
	}
	if err := svc.RetrySideQuestion(r.Context(), sideSession(r), sideID(r), chi.URLParam(r, "turnId")); err != nil {
		writeSideChatError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (c *ConversationsController) interruptSideQuestion(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.sideService(w, r)
	if !ok {
		return
	}
	if err := svc.InterruptSideQuestion(r.Context(), sideSession(r), sideID(r)); err != nil {
		writeSideChatError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *ConversationsController) compactSideChat(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.sideService(w, r)
	if !ok {
		return
	}
	result, err := svc.CompactSideChat(r.Context(), sideSession(r), sideID(r))
	if err != nil {
		writeSideChatError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, CompactConversationResponse{
		TokensBefore: result.TokensBefore, TokensAfter: result.TokensAfter,
	})
}

func (c *ConversationsController) resolveSideApproval(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.sideService(w, r)
	if !ok {
		return
	}
	var req ResolveConversationApprovalRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}
	if req.DecisionID == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", "CHAT_DECISION_REQUIRED", "decisionId is required", nil)
		return
	}
	if err := svc.ResolveSideApproval(r.Context(), sideSession(r), sideID(r), chi.URLParam(r, "requestId"), req.DecisionID); err != nil {
		writeSideChatError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *ConversationsController) resolveSideInput(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.sideService(w, r)
	if !ok {
		return
	}
	var req ResolveConversationInputRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}
	response := ports.ChatInputResponse{Action: ports.ChatInputAction(req.Action), Content: req.Content}
	if !response.Action.Valid() {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", "CHAT_INPUT_ACTION_INVALID", "action must be accept, decline, or cancel", nil)
		return
	}
	if err := svc.ResolveSideInput(r.Context(), sideSession(r), sideID(r), chi.URLParam(r, "requestId"), response); err != nil {
		writeSideChatError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *ConversationsController) sideSettings(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.sideService(w, r)
	if !ok {
		return
	}
	var req SideSettingsRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}
	if err := svc.UpdateSideSettings(r.Context(), sideSession(r), sideID(r), req.Model, req.Effort); err != nil {
		writeSideChatError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *ConversationsController) getSideDraft(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.sideService(w, r)
	if !ok {
		return
	}
	draft, err := svc.SideDraft(r.Context(), sideSession(r), sideID(r))
	if err != nil {
		writeSideChatError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, SideDraftResponse{ContentJSON: draft})
}

func (c *ConversationsController) putSideDraft(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.sideService(w, r)
	if !ok {
		return
	}
	var req SideDraftRequest
	if !decodeConversationBodyLimit(w, r, &req, 42<<20) {
		return
	}
	if err := svc.SaveSideDraft(r.Context(), sideSession(r), sideID(r), req.ContentJSON); err != nil {
		writeSideChatError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *ConversationsController) closeSideChat(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.sideService(w, r)
	if !ok {
		return
	}
	if err := svc.CloseIndependentSideChat(r.Context(), sideSession(r), sideID(r)); err != nil {
		writeSideChatError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *ConversationsController) claimSideLaunch(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.sideService(w, r)
	if !ok {
		return
	}
	var req struct {
		AppRunID string `json:"appRunId"`
	}
	if !decodeConversationBody(w, r, &req) {
		return
	}
	if err := svc.ClaimSideChatLaunch(r.Context(), req.AppRunID); err != nil {
		writeSideChatError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *ConversationsController) exportSideLaunch(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.sideService(w, r)
	if !ok {
		return
	}
	runID := r.URL.Query().Get("appRunId")
	sides, err := svc.ExportSideChatLaunch(r.Context(), runID)
	if err != nil {
		writeSideChatError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, SideChatLaunchState{AppRunID: runID, Sides: sides})
}

func (c *ConversationsController) recoverSideLaunch(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.sideService(w, r)
	if !ok {
		return
	}
	var req SideChatLaunchState
	if !decodeConversationBody(w, r, &req) {
		return
	}
	if err := svc.RecoverSideChatLaunch(r.Context(), req.AppRunID, req.Sides); err != nil {
		writeSideChatError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *ConversationsController) retireSideLaunch(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.sideService(w, r)
	if !ok {
		return
	}
	var req SideChatLaunchClaimRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}
	if err := svc.RetireSideChatLaunch(r.Context(), req.AppRunID); err != nil {
		writeSideChatError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *ConversationsController) recoverSideChat(w http.ResponseWriter, r *http.Request) {
	svc, ok := c.sideService(w, r)
	if !ok {
		return
	}
	if err := svc.RetrySideConnection(r.Context(), sideSession(r), sideID(r)); err != nil {
		writeSideChatError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, nil)
}
