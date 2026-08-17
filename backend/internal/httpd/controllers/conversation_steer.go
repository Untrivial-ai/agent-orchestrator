package controllers

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

// steerPath is the one route this file owns, named once so the not-implemented
// answer and the spec cannot drift apart.
const steerPath = "/api/v1/sessions/{sessionId}/conversation/steer"
const queuedTurnSteerPath = "/api/v1/sessions/{sessionId}/conversation/turns/{turnId}/steer"

// SteerConversationRequest is guidance for a turn that is already running.
type SteerConversationRequest struct {
	// Text is the correction to hand the agent mid-turn.
	Text string `json:"text"`
	// ClientMessageID makes a retry idempotent: the same handle updates the recorded
	// guidance instead of adding a second copy of it, and the provider echoes it back
	// on the item it replays so a client can recognize its own steer.
	ClientMessageID string `json:"clientMessageId,omitempty"`
}

// SteerConversationResponse reports the turn the guidance joined.
type SteerConversationResponse struct {
	// ProviderTurnID is the turn that absorbed it. Against Codex this is the turn
	// that was already running — steering does not open a new one — so a client
	// matches it against the turn it is already rendering.
	ProviderTurnID string `json:"providerTurnId"`
	// ActivityID is the timeline row recording the guidance, so an optimistic bubble
	// can be reconciled with the durable one rather than shown twice.
	ActivityID string `json:"activityId,omitempty"`
}

// PromoteQueuedTurnResponse reports where one durable queued turn landed.
type PromoteQueuedTurnResponse struct {
	SourceTurnID   string `json:"sourceTurnId"`
	ProviderTurnID string `json:"providerTurnId"`
	ActivityID     string `json:"activityId"`
}

// steer sends guidance into the in-flight turn.
//
// 202 rather than 200: the provider accepts the guidance and then acts on it at its
// next model-request boundary — measured about five seconds later against
// codex-cli 0.146.0. What changed by the time this returns is that the agent has the
// correction, not that it has done anything with it, and 200 would claim otherwise.
//
// Strictly better than the interrupt-and-resend it replaces: the turn keeps its id,
// its reasoning and its in-flight work, and settles `completed` rather than
// `interrupted`. That is why it is a separate route instead of a flag on send — the
// two do different things to work the user has already waited for.
func (c *ConversationsController) steer(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", steerPath)
		return
	}
	var req SteerConversationRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}

	result, err := c.Svc.Steer(r.Context(), domain.SessionID(chi.URLParam(r, "sessionId")),
		ports.ChatUserMessage{
			Text:            req.Text,
			ClientMessageID: req.ClientMessageID,
			Origin:          domain.MessageOriginHuman,
		})
	if err != nil {
		writeSteerError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, SteerConversationResponse{
		ProviderTurnID: result.ProviderTurnID,
		ActivityID:     result.ActivityID,
	})
}

func (c *ConversationsController) promoteQueuedTurn(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", queuedTurnSteerPath)
		return
	}
	result, err := c.Svc.PromoteQueuedTurn(
		r.Context(),
		domain.SessionID(chi.URLParam(r, "sessionId")),
		chi.URLParam(r, "turnId"),
	)
	if err != nil {
		writeQueuedTurnSteerError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, PromoteQueuedTurnResponse{
		SourceTurnID: result.SourceTurnID, ProviderTurnID: result.ProviderTurnID,
		ActivityID: result.ActivityID,
	})
}

func writeQueuedTurnSteerError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, chatsvc.ErrTurnNotQueued):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_TURN_NOT_QUEUED", "that message is no longer queued", nil)
	case errors.Is(err, chatsvc.ErrPromotionUncertain):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_PROMOTION_UNCERTAIN",
			"the provider may have received this guidance; it will not be queued again automatically", nil)
	default:
		writeSteerError(w, r, err)
	}
}

// writeSteerError maps the refusals that are specific to steering, then falls
// through to the shared Chat mapping for everything a steer shares with the other
// conversation commands (unknown session, TUI session, no controller, provider
// refusal).
//
// The steer-specific cases are handled here rather than added to the shared mapper
// because two of them say something a generic message cannot. "There is no turn in
// flight" needs to advise sending an ordinary message, which is the opposite of what
// the same condition means to an interrupt.
func writeSteerError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, chatsvc.ErrSteerTextRequired):
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation",
			"CHAT_STEER_TEXT_REQUIRED", "steer text is required", nil)

	case errors.Is(err, chatsvc.ErrSteerUnsupported):
		// Permanent for this harness and a conflict rather than a 500: nothing failed,
		// the provider simply has no way to take guidance mid-turn. A client should
		// hide the control instead of retrying.
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_STEER_UNSUPPORTED",
			"this agent cannot take guidance while it is working", nil)

	case errors.Is(err, chatsvc.ErrTurnNotSteerable):
		// Retryable: a compaction or review turn is running, and the same request
		// works once it ends. The provider's own explanation is carried through
		// because it names which kind of turn is in the way.
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_TURN_NOT_STEERABLE", err.Error(), nil)

	case errors.Is(err, chatsvc.ErrSteerContentUnsupported):
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation",
			"CHAT_UNSUPPORTED_STEER_CONTENT",
			"this agent cannot steer every attachment in that message", nil)

	case errors.Is(err, chatsvc.ErrNoActiveTurn):
		// Ordinary: the turn finished while the user was typing. Steering has nothing
		// to join, and the guidance belongs in a new message — which is a different
		// instruction from the one the interrupt route gives for the same sentinel.
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict",
			"CHAT_NO_ACTIVE_TURN",
			"there is no turn in flight to steer; send this as a message instead", nil)

	default:
		writeConversationError(w, r, err)
	}
}
