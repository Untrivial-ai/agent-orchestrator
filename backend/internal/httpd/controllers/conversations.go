package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

// maxConversationBody bounds a chat message. Large context belongs in files the
// agent reads from the worktree, not in a request body.
const maxConversationBody = maxSpawnBodyBytes

// ConversationService is the controller-facing Chat contract.
type ConversationService interface {
	Snapshot(ctx context.Context, session domain.SessionID) (chatsvc.Snapshot, error)
	Send(ctx context.Context, session domain.SessionID, msg ports.ChatUserMessage) (domain.ConversationTurn, error)
	EditMessage(ctx context.Context, session domain.SessionID, turnID string, msg ports.ChatUserMessage) (chatsvc.EditMessageResult, error)
	ActivateBranch(ctx context.Context, session domain.SessionID, branchID string) (string, error)
	Resolve(ctx context.Context, session domain.SessionID, requestID string, decision ports.ChatDecision) error
	ResolveInput(ctx context.Context, session domain.SessionID, requestID string, response ports.ChatInputResponse) error
	Interrupt(ctx context.Context, session domain.SessionID) error
	Steer(ctx context.Context, session domain.SessionID, msg ports.ChatUserMessage) (chatsvc.SteerResult, error)
	PromoteQueuedTurn(ctx context.Context, session domain.SessionID, turnID string) (chatsvc.PromoteQueuedTurnResult, error)
	Models(ctx context.Context, session domain.SessionID) ([]ports.ChatModel, domain.ConversationSettings, error)
	ConfigOptions(ctx context.Context, session domain.SessionID) ([]ports.ChatConfigOption, error)
	SetConfigOption(ctx context.Context, session domain.SessionID, configID string, value ports.ChatConfigOptionValue) ([]ports.ChatConfigOption, error)
	Skills(ctx context.Context, session domain.SessionID) ([]ports.ChatSkill, error)
	SetTurnSettings(ctx context.Context, session domain.SessionID, settings domain.ConversationSettings) (domain.ConversationSettings, error)
	Compact(ctx context.Context, session domain.SessionID) (ports.ChatCompactionResult, error)
	Rollback(ctx context.Context, session domain.SessionID, turnID string) (int, error)
	SetTitle(ctx context.Context, session domain.SessionID, title string) (string, error)
	ReloadMCPServers(ctx context.Context, session domain.SessionID) ([]domain.ConversationMCPServer, error)
}

type pagedConversationService interface {
	SnapshotPage(ctx context.Context, session domain.SessionID, beforeSequence, limit int64) (chatsvc.Snapshot, error)
}

// ConversationsController owns the Chat routes for a session.
//
// Every route dispatches from the session's persisted mode inside the service, so
// a client cannot reach the Chat path for a session that was created in TUI mode
// even by calling these URLs directly. UI visibility is not the boundary.
type ConversationsController struct {
	Svc ConversationService
}

// Register mounts the conversation routes under a session.
func (c *ConversationsController) Register(r chi.Router) {
	r.Get("/sessions/{sessionId}/conversation", c.snapshot)
	r.Post("/sessions/{sessionId}/conversation/messages", c.send)
	r.Post("/sessions/{sessionId}/conversation/approvals/{requestId}/resolve", c.resolve)
	r.Post("/sessions/{sessionId}/conversation/inputs/{requestId}/resolve", c.resolveInput)
	r.Post("/sessions/{sessionId}/conversation/interrupt", c.interrupt)
	r.Post("/sessions/{sessionId}/conversation/steer", c.steer)
	r.Post("/sessions/{sessionId}/conversation/turns/{turnId}/steer", c.promoteQueuedTurn)
	r.Post("/sessions/{sessionId}/conversation/compact", c.compact)
	r.Get("/sessions/{sessionId}/conversation/models", c.models)
	r.Get("/sessions/{sessionId}/conversation/config-options", c.configOptions)
	r.Patch("/sessions/{sessionId}/conversation/config-options/{configId}", c.setConfigOption)
	r.Get("/sessions/{sessionId}/conversation/skills", c.skills)
	r.Patch("/sessions/{sessionId}/conversation/settings", c.setSettings)
	r.Post("/sessions/{sessionId}/conversation/turns/{turnId}/rollback", c.rollback)
	r.Post("/sessions/{sessionId}/conversation/turns/{turnId}/edit", c.editMessage)
	r.Post("/sessions/{sessionId}/conversation/branches/{branchId}/activate", c.activateBranch)
	r.Put("/sessions/{sessionId}/conversation/title", c.setTitle)
	r.Post("/sessions/{sessionId}/conversation/mcp/reload", c.reloadMCPServers)
}

func (c *ConversationsController) editMessage(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST",
			"/api/v1/sessions/{sessionId}/conversation/turns/{turnId}/edit")
		return
	}
	var req EditConversationMessageRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation",
			"CHAT_EDIT_TURN_INVALID", "edited message text is required", nil)
		return
	}
	result, err := c.Svc.EditMessage(r.Context(), domain.SessionID(chi.URLParam(r, "sessionId")),
		chi.URLParam(r, "turnId"), ports.ChatUserMessage{
			Text: req.Text, ClientMessageID: req.ClientMessageID, Origin: domain.MessageOriginHuman,
		})
	if err != nil {
		writeConversationEditError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, EditConversationMessageResponse{
		SourceBranchID: result.SourceBranchID,
		ActiveBranchID: result.ActiveBranchID,
		TurnID:         result.Turn.ID,
		ProviderTurnID: result.Turn.ProviderTurnID,
		State:          result.Turn.State,
	})
}

func (c *ConversationsController) activateBranch(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST",
			"/api/v1/sessions/{sessionId}/conversation/branches/{branchId}/activate")
		return
	}
	active, err := c.Svc.ActivateBranch(r.Context(), domain.SessionID(chi.URLParam(r, "sessionId")),
		chi.URLParam(r, "branchId"))
	if errors.Is(err, domain.ErrNoConversationBranch) {
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found",
			"CHAT_BRANCH_NOT_FOUND", "that conversation branch does not exist", nil)
		return
	}
	if errors.Is(err, chatsvc.ErrTurnRunning) || errors.Is(err, chatsvc.ErrControllerHandoff) {
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_EDIT_BUSY", "stop the current turn before switching conversation branches", nil)
		return
	}
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, ActivateConversationBranchResponse{ActiveBranchID: active})
}

func writeConversationEditError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, chatsvc.ErrForkUnsupported):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_EDIT_UNSUPPORTED", "this agent cannot branch an earlier prompt", nil)
	case errors.Is(err, chatsvc.ErrTurnRunning), errors.Is(err, chatsvc.ErrControllerHandoff):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_EDIT_BUSY", "stop the current turn before branching", nil)
	case errors.Is(err, chatsvc.ErrEditTurnInvalid) && errors.Is(err, domain.ErrNoConversationTurn):
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found",
			"CHAT_EDIT_TURN_INVALID", "that editable prompt is not in this conversation", nil)
	case errors.Is(err, chatsvc.ErrEditTurnInvalid):
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation",
			"CHAT_EDIT_TURN_INVALID", err.Error(), nil)
	default:
		writeConversationError(w, r, err)
	}
}

// configOptions serves every live control the provider advertises. Empty is a
// real capability answer and keeps native drivers free to use AO's older typed
// model/settings surface.
func (c *ConversationsController) configOptions(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/sessions/{sessionId}/conversation/config-options")
		return
	}
	options, err := c.Svc.ConfigOptions(r.Context(), domain.SessionID(chi.URLParam(r, "sessionId")))
	if err != nil {
		if errors.Is(err, chatsvc.ErrConfigOptionsUnsupported) {
			envelope.WriteJSON(w, http.StatusOK, ConversationConfigOptionsResponse{
				Options: []ConversationConfigOptionResponse{},
			})
			return
		}
		writeConversationError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, configOptionsPayload(options))
}

// setConfigOption applies one advertised provider value and returns the complete
// rebuilt catalog. This is immediate session state, not a preference to infer on
// the next turn: changing models can alter the other available controls now.
func (c *ConversationsController) setConfigOption(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "PATCH", "/api/v1/sessions/{sessionId}/conversation/config-options/{configId}")
		return
	}
	var req SetConversationConfigOptionRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}
	if (req.Value == "") == (req.Enabled == nil) {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation",
			"CHAT_CONFIG_OPTION_VALUE_REQUIRED",
			"provide exactly one of value or enabled", nil)
		return
	}
	options, err := c.Svc.SetConfigOption(
		r.Context(),
		domain.SessionID(chi.URLParam(r, "sessionId")),
		chi.URLParam(r, "configId"),
		ports.ChatConfigOptionValue{Select: req.Value, Boolean: req.Enabled},
	)
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, configOptionsPayload(options))
}

// reloadMCPServers restarts the provider's tool servers for a session.
//
// A write, not a read, and the only recovery there is for the failure it addresses:
// a tool server that failed to start stays failed for the life of the provider
// process, so without this a session loses a tool permanently and the alternative is
// throwing the conversation away.
func (c *ConversationsController) reloadMCPServers(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/conversation/mcp/reload")
		return
	}
	session := domain.SessionID(chi.URLParam(r, "sessionId"))
	servers, err := c.Svc.ReloadMCPServers(r.Context(), session)
	if errors.Is(err, chatsvc.ErrTurnRunning) {
		// Answered here rather than in writeConversationError, whose CHAT_TURN_RUNNING
		// message names rolling back. Same code and same retryable meaning; a reader
		// of this one is not undoing anything.
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_TURN_RUNNING",
			"stop the agent before reloading its tool servers: it is in the middle of a turn", nil)
		return
	}
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ReloadConversationMCPServersResponse{
		Servers: mcpServersPayload(servers),
	})
}

// rollback discards a turn and everything after it, from the agent's memory as well
// as from the timeline.
func (c *ConversationsController) rollback(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST",
			"/api/v1/sessions/{sessionId}/conversation/turns/{turnId}/rollback")
		return
	}
	discarded, err := c.Svc.Rollback(r.Context(),
		domain.SessionID(chi.URLParam(r, "sessionId")), chi.URLParam(r, "turnId"))
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, RollbackConversationResponse{TurnsDiscarded: discarded})
}

// setTitle names the provider's thread.
//
// 202 rather than 200: the provider accepts the name and then reports it back on its
// own event, and that report is what moves AO's session label. Claiming 200 would
// promise a change that has not happened yet.
func (c *ConversationsController) setTitle(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "PUT", "/api/v1/sessions/{sessionId}/conversation/title")
		return
	}
	var req SetConversationTitleRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}
	title, err := c.Svc.SetTitle(r.Context(),
		domain.SessionID(chi.URLParam(r, "sessionId")), req.Title)
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, SetConversationTitleResponse{Title: title})
}

// compact asks the provider to summarize earlier history and reclaim context.
//
// 202 rather than 200: the provider accepts the request and does the work as its
// own turn afterwards, so this reports acceptance and the reclaim lands on the
// timeline. Claiming 200 would say the compaction is done.
func (c *ConversationsController) compact(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/conversation/compact")
		return
	}
	result, err := c.Svc.Compact(r.Context(), domain.SessionID(chi.URLParam(r, "sessionId")))
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, CompactConversationResponse{
		TokensBefore: result.TokensBefore,
		TokensAfter:  result.TokensAfter,
	})
}

// models serves the provider's catalog for this session plus the current choice.
func (c *ConversationsController) models(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/sessions/{sessionId}/conversation/models")
		return
	}
	session := domain.SessionID(chi.URLParam(r, "sessionId"))
	models, selected, err := c.Svc.Models(r.Context(), session)
	if err != nil {
		if errors.Is(err, chatsvc.ErrModelsUnsupported) {
			// Not a failure: this agent simply offers no choice. An empty list with
			// the current selection lets the client hide the picker rather than
			// show an error the user cannot act on.
			envelope.WriteJSON(w, http.StatusOK, ConversationModelsResponse{
				Models:   []ConversationModelResponse{},
				Selected: turnSettingsPayload(selected),
			})
			return
		}
		writeConversationError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, conversationModelsResponse(models, selected))
}

// setSettings records the provider choices for the next turn.
func (c *ConversationsController) setSettings(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "PATCH", "/api/v1/sessions/{sessionId}/conversation/settings")
		return
	}
	var req ConversationTurnSettingsPayload
	if !decodeConversationBody(w, r, &req) {
		return
	}
	// An unknown approval mode is refused rather than normalized: silently
	// downgrading a permission choice is the one direction that must never happen
	// by accident.
	approval := domain.PermissionMode(req.ApprovalMode)
	if req.ApprovalMode != "" && !approval.Valid() {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation",
			"CHAT_APPROVAL_MODE_INVALID",
			fmt.Sprintf("unknown approval mode %q", req.ApprovalMode), nil)
		return
	}

	settings, err := c.Svc.SetTurnSettings(r.Context(),
		domain.SessionID(chi.URLParam(r, "sessionId")), domain.ConversationSettings{
			Model:           req.Model,
			ReasoningEffort: req.ReasoningEffort,
			ApprovalMode:    approval,
		})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, turnSettingsPayload(settings))
}

func conversationModelsResponse(
	models []ports.ChatModel,
	selected domain.ConversationSettings,
) ConversationModelsResponse {
	out := ConversationModelsResponse{
		Models:   make([]ConversationModelResponse, 0, len(models)),
		Selected: turnSettingsPayload(selected),
	}
	for _, model := range models {
		out.Models = append(out.Models, ConversationModelResponse{
			ID:            model.ID,
			DisplayName:   model.DisplayName,
			Description:   model.Description,
			Default:       model.Default,
			Efforts:       model.Efforts,
			DefaultEffort: model.DefaultEffort,
		})
	}
	return out
}

func configOptionsPayload(options []ports.ChatConfigOption) ConversationConfigOptionsResponse {
	out := ConversationConfigOptionsResponse{
		Options: make([]ConversationConfigOptionResponse, 0, len(options)),
	}
	for _, option := range options {
		item := ConversationConfigOptionResponse{
			ID:             option.ID,
			Name:           option.Name,
			Description:    option.Description,
			Category:       option.Category,
			Type:           string(option.Type),
			CurrentValue:   option.Current.Select,
			CurrentBoolean: option.Current.Boolean,
			Choices:        make([]ConversationConfigChoiceResponse, 0, len(option.Choices)),
		}
		for _, choice := range option.Choices {
			item.Choices = append(item.Choices, ConversationConfigChoiceResponse{
				Value:       choice.Value,
				Name:        choice.Name,
				Description: choice.Description,
				Group:       choice.Group,
				GroupName:   choice.GroupName,
			})
		}
		out.Options = append(out.Options, item)
	}
	return out
}

func turnSettingsPayload(settings domain.ConversationSettings) ConversationTurnSettingsPayload {
	return ConversationTurnSettingsPayload{
		Model:           settings.Model,
		ReasoningEffort: settings.ReasoningEffort,
		ApprovalMode:    string(settings.ApprovalMode),
	}
}

func (c *ConversationsController) snapshot(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/sessions/{sessionId}/conversation")
		return
	}
	session := domain.SessionID(chi.URLParam(r, "sessionId"))
	before, err := optionalPositiveInt64(r.URL.Query().Get("beforeSequence"))
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", "CONVERSATION_CURSOR_INVALID", err.Error(), nil)
		return
	}
	limit := int64(200)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || limit < 1 || limit > 500 {
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", "CONVERSATION_LIMIT_INVALID", "limit must be between 1 and 500", nil)
			return
		}
	}
	var snapshot chatsvc.Snapshot
	if paged, ok := c.Svc.(pagedConversationService); ok {
		snapshot, err = paged.SnapshotPage(r.Context(), session, before, limit)
	} else {
		snapshot, err = c.Svc.Snapshot(r.Context(), session)
	}
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, conversationSnapshotResponse(snapshot))
}

func optionalPositiveInt64(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 1 {
		return 0, fmt.Errorf("beforeSequence must be a positive integer")
	}
	return value, nil
}

func (c *ConversationsController) send(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/conversation/messages")
		return
	}
	var req SendConversationMessageRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}
	if req.Text == "" && len(req.Attachments) == 0 && len(req.Resources) == 0 {
		// There is no keystroke concept in Chat mode: an empty body is a client
		// bug, not a way to nudge the agent.
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation",
			"CHAT_MESSAGE_EMPTY", "message text is required", nil)
		return
	}

	content, attachmentErr := conversationContent(req)
	if attachmentErr != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation",
			attachmentErr.code, attachmentErr.message, nil)
		return
	}
	text := req.Text
	if text == "" {
		text = fmt.Sprintf("Attached %d item(s) for context", len(content))
	}
	turn, err := c.Svc.Send(r.Context(), domain.SessionID(chi.URLParam(r, "sessionId")), ports.ChatUserMessage{
		Text:            text,
		Content:         content,
		ClientMessageID: req.ClientMessageID,
		Origin:          domain.MessageOriginHuman,
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}

	// An empty turn means the client message id was already delivered. Reporting
	// the duplicate explicitly lets a retrying client stop rather than assume a
	// new turn began.
	envelope.WriteJSON(w, http.StatusAccepted, SendConversationMessageResponse{
		TurnID:         turn.ID,
		ProviderTurnID: turn.ProviderTurnID,
		State:          turn.State,
		Duplicate:      turn.ID == "",
	})
}

func conversationContent(req SendConversationMessageRequest) ([]ports.ChatContent, *attachmentError) {
	spawnInputs := make([]AttachmentInput, 0, len(req.Attachments))
	for _, attachment := range req.Attachments {
		spawnInputs = append(spawnInputs, AttachmentInput{
			MimeType: attachment.MIMEType,
			Data:     attachment.Data,
		})
	}
	if _, err := decodeSpawnAttachments(spawnInputs); err != nil {
		return nil, err
	}
	content := make([]ports.ChatContent, 0, len(req.Attachments)+len(req.Resources))
	for _, attachment := range req.Attachments {
		content = append(content, ports.ChatContent{
			Type: "image", Data: attachment.Data,
			MIMEType: strings.ToLower(strings.TrimSpace(attachment.MIMEType)),
		})
	}
	for _, resource := range req.Resources {
		if strings.TrimSpace(resource.URI) == "" || strings.TrimSpace(resource.Name) == "" {
			return nil, &attachmentError{"INVALID_RESOURCE", "resource uri and name are required"}
		}
		kind, text := "resource_link", ""
		if resource.Text != nil {
			kind = "resource"
			text = *resource.Text
		}
		content = append(content, ports.ChatContent{
			Type: kind, URI: resource.URI, Name: resource.Name,
			MIMEType: resource.MIMEType, Text: text,
		})
	}
	return content, nil
}

func (c *ConversationsController) resolve(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST",
			"/api/v1/sessions/{sessionId}/conversation/approvals/{requestId}/resolve")
		return
	}
	var req ResolveConversationApprovalRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}
	if req.DecisionID == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation",
			"CHAT_DECISION_REQUIRED", "decisionId is required", nil)
		return
	}

	err := c.Svc.Resolve(r.Context(), domain.SessionID(chi.URLParam(r, "sessionId")),
		chi.URLParam(r, "requestId"), ports.ChatDecision{ID: req.DecisionID})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *ConversationsController) resolveInput(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST",
			"/api/v1/sessions/{sessionId}/conversation/inputs/{requestId}/resolve")
		return
	}
	var req ResolveConversationInputRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}
	action := ports.ChatInputAction(req.Action)
	if !action.Valid() {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation",
			"CHAT_INPUT_ACTION_INVALID", "action must be accept, decline, or cancel", nil)
		return
	}
	if action != ports.ChatInputActionAccept && len(req.Content) > 0 {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation",
			"CHAT_INPUT_CONTENT_INVALID", "content is only allowed with accept", nil)
		return
	}
	if err := c.Svc.ResolveInput(
		r.Context(), domain.SessionID(chi.URLParam(r, "sessionId")),
		chi.URLParam(r, "requestId"),
		ports.ChatInputResponse{Action: action, Content: req.Content},
	); err != nil {
		writeConversationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *ConversationsController) interrupt(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/conversation/interrupt")
		return
	}
	err := c.Svc.Interrupt(r.Context(), domain.SessionID(chi.URLParam(r, "sessionId")))
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeConversationBody(w http.ResponseWriter, r *http.Request, into any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxConversationBody))
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation",
			"INVALID_BODY", "could not read request body", nil)
		return false
	}
	if err := json.Unmarshal(body, into); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation",
			"INVALID_BODY", "request body is not valid JSON", nil)
		return false
	}
	return true
}

// writeConversationError maps the Chat service's typed failures onto stable
// codes, so a client can tell a permanent answer from a retryable one.
func writeConversationError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ports.ErrSessionNotFound):
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found",
			"SESSION_NOT_FOUND", "session not found", nil)

	case errors.Is(err, chatsvc.ErrNotChatMode):
		// Permanent for this request: retry only after an explicit interface switch.
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"SESSION_MODE_MISMATCH",
			"this session was created in Terminal UI mode and has no chat conversation", nil)

	case errors.Is(err, chatsvc.ErrNoController):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_CONTROLLER_NOT_READY",
			"the agent controller for this session is not running", nil)

	case errors.Is(err, chatsvc.ErrControllerHandoff):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_INTERFACE_TRANSITION",
			"the session is switching interfaces; send again after the handoff completes", nil)

	case errors.Is(err, chatsvc.ErrCompactionUnsupported):
		// Permanent for this harness, and a 409 rather than a 500 because nothing
		// failed: the provider simply has no way to reclaim context. A client should
		// stop offering the control instead of retrying.
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_COMPACTION_UNSUPPORTED",
			"this agent cannot compact its conversation history", nil)

	case errors.Is(err, chatsvc.ErrCompactionWhileBusy):
		// Retryable once the turn ends, and refused rather than forwarded: the
		// provider would silently interrupt the running turn to make room.
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_COMPACTION_BUSY",
			"finish or stop the current turn before compacting", nil)

	case errors.Is(err, chatsvc.ErrNoActiveTurn):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_NO_ACTIVE_TURN", "there is no turn in flight to interrupt", nil)

	case errors.Is(err, domain.ErrNoConversationTurn):
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found",
			"CHAT_TURN_NOT_FOUND", "that turn is not in this session's conversation", nil)

	case errors.Is(err, chatsvc.ErrTurnRunning):
		// Retryable, unlike every other refusal here: the same request works once
		// the agent finishes. Rolling back mid-turn is refused rather than raced,
		// because discarding history the agent is still writing into would leave
		// AO's timeline and the agent's memory describing different conversations.
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_TURN_RUNNING",
			"stop the agent before rolling back: it is in the middle of a turn", nil)

	case errors.Is(err, chatsvc.ErrTurnNotRollbackable):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_TURN_NOT_ROLLBACKABLE",
			"that turn never reached the agent, so there is nothing to undo", nil)

	case errors.Is(err, chatsvc.ErrRollbackUnsupported):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_ROLLBACK_UNSUPPORTED", "this agent cannot discard conversation history", nil)

	case errors.Is(err, chatsvc.ErrForkUnsupported):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_FORK_UNSUPPORTED", "this agent cannot branch a conversation", nil)

	case errors.Is(err, chatsvc.ErrRenameUnsupported):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_RENAME_UNSUPPORTED", "this agent's conversation carries no title", nil)

	case errors.Is(err, chatsvc.ErrMCPReloadUnsupported):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_MCP_RELOAD_UNSUPPORTED", "this agent cannot reload its tool servers", nil)

	case errors.Is(err, chatsvc.ErrTitleRequired):
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation",
			"CHAT_TITLE_REQUIRED", "title is required", nil)

	case errors.Is(err, chatsvc.ErrProviderRefused):
		// The provider declined and the conversation is fine. Its own explanation is
		// carried through: it says something the user can act on, which a generic
		// conflict message would throw away.
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_PROVIDER_REFUSED", err.Error(), nil)

	case errors.Is(err, ports.ErrChatRequestNotPending):
		// Two clients can watch the same approval, so arriving second is ordinary.
		// The card is stale; the client should refresh rather than retry.
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_REQUEST_NOT_PENDING",
			"this request has already been answered or is no longer waiting", nil)

	case errors.Is(err, ports.ErrChatDecisionNotOffered):
		// A client asking for an option the provider never offered is a bug in the
		// client, not a server failure — and the request is still pending, so the
		// user can still answer it.
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation",
			"CHAT_DECISION_NOT_OFFERED", err.Error(), nil)

	case errors.Is(err, ports.ErrChatConfigOptionInvalid):
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation",
			"CHAT_CONFIG_OPTION_INVALID", err.Error(), nil)

	case errors.Is(err, ports.ErrChatUnsupported):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"SESSION_MODE_UNSUPPORTED", err.Error(), nil)

	case errors.Is(err, ports.ErrChatDriverUnavailable):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_DRIVER_UNAVAILABLE", err.Error(), nil)

	case errors.Is(err, ports.ErrChatDriverIncompatible):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_DRIVER_INCOMPATIBLE", err.Error(), nil)

	case errors.Is(err, ports.ErrChatAuthRequired):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_AUTH_REQUIRED", "the agent is installed but not authenticated", nil)

	case errors.Is(err, ports.ErrChatResumeFailed):
		// Deliberately not a silent recovery: the client must offer the user a
		// choice rather than have AO invent a fresh conversation.
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_RESUME_FAILED",
			"the stored provider conversation could not be resumed", nil)

	default:
		envelope.WriteError(w, r, err)
	}
}

// conversationSnapshotResponse maps the service snapshot onto the wire shape.
// Items arrive already ordered by sequence, so nothing is re-sorted here.
func conversationSnapshotResponse(s chatsvc.Snapshot) ConversationSnapshotResponse {
	out := ConversationSnapshotResponse{
		ConversationID:             s.Conversation.ID,
		ActiveBranchID:             s.Conversation.ActiveBranchID,
		BranchedFromEarlierMessage: s.BranchedFromEarlierMessage,
		SessionID:                  string(s.SessionID),
		Harness:                    string(s.Harness),
		Mode:                       string(s.Mode),
		Controller:                 string(s.Controller),
		LatestSequence:             s.Conversation.LatestSequence,
		OldestSequence:             s.OldestSequence,
		HasMoreBefore:              s.HasMoreBefore,
		Turns:                      make([]ConversationTurnResponse, 0, len(s.Turns)),
		Messages:                   make([]ConversationMessageResponse, 0, len(s.Messages)),
		Activities:                 make([]ConversationActivityResponse, 0, len(s.Activities)),
		BranchPoints:               make([]ConversationBranchPointResponse, 0, len(s.BranchPoints)),
		Settings:                   turnSettingsPayload(s.Conversation.Settings),
		Usage:                      usagePayload(s.Usage),
		RateLimits:                 rateLimitsPayload(s.RateLimits),
		CompactedAt:                optionalTimestamp(s.Conversation.CompactedAt),
		Title:                      s.Conversation.ProviderTitle,
		ModelReroute:               modelReroutePayload(s.Conversation.ModelReroute),
		Account:                    accountPayload(s.Conversation.Account),
		ThreadState:                threadStatePayload(s.Conversation.ThreadState),
		MCPServers:                 mcpServersPayload(s.Conversation.MCPServers),
		Capabilities:               capabilityNames(s.Capabilities),
	}

	for _, turn := range s.Turns {
		out.Turns = append(out.Turns, ConversationTurnResponse{
			ID:             turn.ID,
			State:          string(turn.State),
			ProviderTurnID: turn.ProviderTurnID,
			ErrorMessage:   turn.ErrorMessage,
			RequestedAt:    turn.RequestedAt.UTC().Format(time.RFC3339),
			StartedAt:      optionalTimestamp(turn.StartedAt),
			CompletedAt:    optionalTimestamp(turn.CompletedAt),
			Diff:           turnDiffPayload(turn.Diff),
			Plan:           turnPlanPayload(turn.Plan),
			RolledBack:     turn.RolledBackAt != nil,
		})
	}

	for _, msg := range s.Messages {
		message := ConversationMessageResponse{
			Kind:      "message",
			ID:        msg.ID,
			TurnID:    msg.TurnID,
			Sequence:  msg.Sequence,
			Revision:  msg.Revision,
			Role:      string(msg.Role),
			Origin:    string(msg.Origin),
			Text:      msg.Text,
			Streaming: msg.Streaming,
			CreatedAt: msg.CreatedAt.UTC().Format(time.RFC3339),
		}
		message.Content, message.EditAvailable = conversationContentSummary(msg)
		out.Messages = append(out.Messages, message)
	}
	for _, point := range s.BranchPoints {
		out.BranchPoints = append(out.BranchPoints, ConversationBranchPointResponse{
			TurnID: point.TurnID, Position: point.Position, Total: point.Total,
			PreviousBranchID: point.PreviousBranchID, NextBranchID: point.NextBranchID,
		})
	}

	for _, activity := range s.Activities {
		out.Activities = append(out.Activities, ConversationActivityResponse{
			Kind:           "activity",
			ID:             activity.ID,
			TurnID:         activity.TurnID,
			Sequence:       activity.Sequence,
			Revision:       activity.Revision,
			ActivityKind:   string(activity.Kind),
			Status:         string(activity.Status),
			Summary:        activity.Summary,
			Detail:         activityDetailPayload(activity),
			RequestID:      activity.RequestID,
			ProviderItemID: activity.ProviderItemID,
			CreatedAt:      activity.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return out
}

func conversationContentSummary(msg domain.ConversationMessage) ([]ConversationContentSummaryResponse, bool) {
	if msg.Role != domain.MessageRoleUser || msg.Origin != domain.MessageOriginHuman {
		return nil, false
	}
	if msg.DeliveryContentJSON == "" {
		return nil, true
	}
	var content []ports.ChatContent
	if err := json.Unmarshal([]byte(msg.DeliveryContentJSON), &content); err != nil {
		return nil, false
	}
	summaries := make([]ConversationContentSummaryResponse, 0, len(content))
	for _, block := range content {
		if block.Type == "text" {
			continue
		}
		name := block.Name
		if name == "" && block.Type != "image" && block.Type != "resource" && block.Type != "resource_link" {
			name = block.Type
		}
		summaries = append(summaries, ConversationContentSummaryResponse{
			Type: block.Type, MIMEType: block.MIMEType, URI: block.URI, Name: name,
		})
	}
	return summaries, true
}

// usagePayload maps the conversation's token position onto the wire shape. A nil
// snapshot value stays nil: the field being absent is how a client learns the
// provider has not reported yet, which is not the same as a conversation that has
// used nothing.
func usagePayload(usage *domain.ConversationUsage) *ConversationUsagePayload {
	if usage == nil {
		return nil
	}
	return &ConversationUsagePayload{
		ContextUsed:   usage.ContextUsed,
		ContextWindow: usage.ContextWindow,
		InputTokens:   usage.InputTokens,
		OutputTokens:  usage.OutputTokens,
		CachedTokens:  usage.CachedTokens,
		TotalTokens:   usage.TotalTokens,
		Cost:          usage.Cost,
		Currency:      usage.Currency,
	}
}

// rateLimitsPayload maps the account's quota position onto the wire shape.
func rateLimitsPayload(limits *domain.ConversationRateLimits) *ConversationRateLimitsPayload {
	if limits == nil {
		return nil
	}
	return &ConversationRateLimitsPayload{
		PrimaryUsedPercent:       limits.PrimaryUsedPercent,
		SecondaryUsedPercent:     limits.SecondaryUsedPercent,
		PrimaryResetsInSeconds:   limits.PrimaryResetsInSeconds,
		SecondaryResetsInSeconds: limits.SecondaryResetsInSeconds,
		PlanLabel:                limits.PlanLabel,
	}
}

// turnDiffPayload maps a turn's changed-file summary onto the wire shape.
func turnDiffPayload(diff *domain.ConversationTurnDiff) *ConversationTurnDiffResponse {
	if diff == nil {
		return nil
	}
	out := &ConversationTurnDiffResponse{
		Truncated: diff.Truncated,
		Files:     make([]ConversationDiffFileResponse, 0, len(diff.Files)),
	}
	for _, file := range diff.Files {
		out.Files = append(out.Files, ConversationDiffFileResponse{
			Path:      file.Path,
			Additions: file.Additions,
			Deletions: file.Deletions,
			Status:    file.Status,
			OldPath:   file.OldPath,
		})
	}
	return out
}

// turnPlanPayload maps a turn's plan onto the wire shape. A nil plan stays nil: the
// agent made none, which is not the same as making an empty one.
func turnPlanPayload(plan *domain.ConversationPlan) *ConversationPlanResponse {
	if plan == nil {
		return nil
	}
	out := &ConversationPlanResponse{
		Explanation: plan.Explanation,
		Steps:       make([]ConversationPlanStepResponse, 0, len(plan.Steps)),
	}
	for _, step := range plan.Steps {
		out.Steps = append(out.Steps, ConversationPlanStepResponse{
			Text:   step.Text,
			Status: string(step.Status),
		})
	}
	return out
}

// modelReroutePayload maps a model substitution onto the wire shape.
//
// Absent means the provider never swapped the model, which is why it is not folded
// into the settings payload: settings say what AO asked for, and this says what
// answered. Collapsing them would leave a client unable to tell "I chose this" from
// "this replied".
func modelReroutePayload(reroute *domain.ConversationModelReroute) *ConversationModelReroutePayload {
	if reroute == nil {
		return nil
	}
	return &ConversationModelReroutePayload{
		FromModel:      reroute.FromModel,
		ToModel:        reroute.ToModel,
		Reason:         reroute.Reason,
		ProviderTurnID: reroute.ProviderTurnID,
		At:             reroute.At.UTC().Format(time.RFC3339),
	}
}

// accountPayload maps the provider account onto the wire shape.
func accountPayload(account *domain.ConversationAccount) *ConversationAccountPayload {
	if account == nil {
		return nil
	}
	return &ConversationAccountPayload{
		AuthMode:         account.AuthMode,
		PlanLabel:        account.PlanLabel,
		ReauthRequiredAt: optionalTimestamp(account.ReauthRequiredAt),
		ReauthReason:     account.ReauthReason,
	}
}

// threadStatePayload maps the provider's thread lifecycle onto the wire shape.
func threadStatePayload(state *domain.ConversationThreadState) *ConversationThreadStatePayload {
	if state == nil {
		return nil
	}
	return &ConversationThreadStatePayload{
		Status:     string(state.Status),
		WaitingOn:  state.WaitingOn,
		ArchivedAt: optionalTimestamp(state.ArchivedAt),
		ClosedAt:   optionalTimestamp(state.ClosedAt),
	}
}

// mcpServersPayload maps the tool servers onto the wire shape, preserving the order
// the daemon holds them in so a client's list does not reshuffle between polls.
func mcpServersPayload(servers []domain.ConversationMCPServer) []ConversationMCPServerPayload {
	if len(servers) == 0 {
		return nil
	}
	out := make([]ConversationMCPServerPayload, 0, len(servers))
	for _, server := range servers {
		out = append(out, ConversationMCPServerPayload{
			Name:          server.Name,
			Status:        server.Status,
			Error:         server.Error,
			FailureReason: server.FailureReason,
		})
	}
	return out
}

// activityDetailPayload folds accumulated command output into the typed payload.
//
// The two output sources are not equivalent and the client is told which it has.
// The streamed accumulation exists while the command runs and survives a command
// that never completes; the provider's own aggregate only appears on completion.
// Neither is complete -- measured on codex-cli 0.146.0, a command printing
// tick-1..tick-8 lost tick-1 from the delta stream and from the aggregate alike --
// so outputMayBePartial stays set either way. `outputSource` exists so the UI can
// explain WHY it is partial instead of hedging identically about both.
func activityDetailPayload(activity domain.ConversationActivity) map[string]any {
	detail := decodeDetail(activity.Detail)
	if activity.CommandOutput != "" {
		if detail == nil {
			detail = map[string]any{}
		}
		// The stream wins over the aggregate: it is the only one that exists
		// mid-command, and once both exist they carry the same text.
		detail["output"] = activity.CommandOutput
		detail["outputSource"] = "stream"
		detail["outputMayBePartial"] = true
		if activity.CommandOutputTruncated {
			detail["outputTruncated"] = true
		}
	}
	if activity.StreamedText != "" {
		if detail == nil {
			detail = map[string]any{}
		}
		key, truncatedKey := streamedTextKeys(activity.Kind)
		detail[key] = activity.StreamedText
		if activity.StreamedTextTruncated {
			detail[truncatedKey] = true
		}
	}
	return detail
}

// streamedTextKeys names the accumulated provider prose for the kind of activity it
// belongs to.
//
// One column, several meanings, because each activity kind has exactly one such
// stream: a command's is the keystrokes the agent sent into its terminal, a
// reasoning item's is the model's own summary. Naming them apart on the wire is what
// keeps a client from having to guess which it is looking at -- and specifically
// keeps terminalInput out of `output`, which the PTY already echoes.
func streamedTextKeys(kind domain.ActivityKind) (key, truncatedKey string) {
	switch kind {
	case domain.ActivityKindReasoning:
		// The same key the settled summary lands under, so a client reads one field
		// whether the item is still streaming or already finished.
		return "text", "textTruncated"
	case domain.ActivityKindCommand:
		return "terminalInput", "terminalInputTruncated"
	case domain.ActivityKindMCPTool:
		return "progress", "progressTruncated"
	case domain.ActivityKindFileChange:
		return "patchOutput", "patchOutputTruncated"
	default:
		return "streamedText", "streamedTextTruncated"
	}
}

// decodeDetail turns the stored payload into a JSON object. A payload this build
// cannot parse is dropped rather than emitted as a string the client would have
// to guess at.
func decodeDetail(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var detail map[string]any
	if err := json.Unmarshal(raw, &detail); err != nil {
		return nil
	}
	return detail
}

func optionalTimestamp(t *time.Time) *string {
	if t == nil {
		return nil
	}
	formatted := t.UTC().Format(time.RFC3339)
	return &formatted
}
