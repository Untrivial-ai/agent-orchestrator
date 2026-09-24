package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// A Chat spawn can answer the API before its worktree and controller exist, so
// the session is on screen and typeable while it is still being built. The
// durable turn queue — not a client-side buffer and not a second intake path —
// is what holds those messages: NextQueuedTurn is keyed on the conversation, so
// a turn recorded before any controller existed is drained by the controller
// that arrives later, in the order the user typed it.

// ErrNotProvisioning reports a queue-without-controller attempt against a
// session that is not starting up. Such a session has a real reason for having
// no controller (stopped, switching interfaces, failed), and silently accepting
// a message it will never dispatch would be worse than refusing it.
var ErrNotProvisioning = errors.New("session is not provisioning")

// QueueUserMessage records a turn for a session whose controller does not exist
// yet. It is the opening prompt's delivery path for an asynchronous spawn, and
// the fallback for anything the user types before the agent finishes starting.
func (s *Service) QueueUserMessage(
	ctx context.Context,
	id domain.SessionID,
	msg ports.ChatUserMessage,
) (domain.ConversationTurn, error) {
	record, err := s.requireChatSession(ctx, id)
	if err != nil {
		return domain.ConversationTurn{}, err
	}
	return s.queueWithoutController(ctx, record, msg)
}

func (s *Service) queueWithoutController(
	ctx context.Context,
	record domain.SessionRecord,
	msg ports.ChatUserMessage,
) (domain.ConversationTurn, error) {
	if !record.ProvisionState.IsProvisioning() {
		return domain.ConversationTurn{}, ErrNotProvisioning
	}
	conversation, err := s.ensureConversation(ctx, record)
	if err != nil {
		return domain.ConversationTurn{}, err
	}

	now := s.now()
	turnID := s.newID()
	deliveryContent := ""
	if len(msg.Content) > 0 {
		encoded, marshalErr := json.Marshal(msg.Content)
		if marshalErr != nil {
			return domain.ConversationTurn{}, fmt.Errorf("encode chat delivery content: %w", marshalErr)
		}
		deliveryContent = string(encoded)
	}
	// The generation is empty on purpose: no controller has claimed this turn.
	// Drain selects by conversation, so the controller that starts next owns it.
	created, err := s.store.AppendUserMessage(ctx, conversation.ID, record.ID, "", domain.ConversationMessage{
		ID:                  s.newID(),
		Text:                msg.Text,
		Origin:              normalizeOrigin(msg.Origin),
		ClientMessageID:     msg.ClientMessageID,
		DeliveryContentJSON: deliveryContent,
	}, turnID, now)
	if err != nil {
		return domain.ConversationTurn{}, fmt.Errorf("queue message for %s: %w", record.ID, err)
	}
	if !created {
		// Same client message id as an earlier delivery: already queued.
		return domain.ConversationTurn{}, nil
	}
	turn := domain.ConversationTurn{
		ID:                 turnID,
		ConversationID:     conversation.ID,
		HandledBySessionID: record.ID,
		State:              domain.TurnStateQueued,
		RequestedAt:        now,
	}
	// The controller may have appeared after Send read the provisioning row, or
	// after a prior drain found the queue empty. Kicking the same serialized drain
	// here closes both races; NextQueuedTurn still owns ordering.
	if controller, controllerErr := s.Controller(record.ID); controllerErr == nil {
		_ = controller.drain(ctx) // The message is already accepted; drain logs failures.
	}
	return turn, nil
}

// ensureConversation opens the session's conversation before its controller
// exists. CreateConversation returns the existing row for a session that
// already has one, so this stays correct when Start opens the same
// conversation later.
func (s *Service) ensureConversation(
	ctx context.Context,
	record domain.SessionRecord,
) (domain.ConversationRecord, error) {
	conversation, err := s.store.ConversationForSession(ctx, record.ID)
	if err == nil {
		return conversation, nil
	}
	if !errors.Is(err, domain.ErrNoConversation) {
		return domain.ConversationRecord{}, fmt.Errorf("read conversation for %s: %w", record.ID, err)
	}
	conversation, err = s.store.CreateConversation(
		ctx, s.newID(), domain.ConversationScopeSession, record.ProjectID, record.ID, s.now())
	if err != nil {
		return domain.ConversationRecord{}, fmt.Errorf("open conversation for %s: %w", record.ID, err)
	}
	return conversation, nil
}

// DrainQueued dispatches whatever was queued while the session had no
// controller. Called once the controller is live; a session with an empty queue
// is a no-op.
func (s *Service) DrainQueued(ctx context.Context, id domain.SessionID) error {
	controller, err := s.Controller(id)
	if err != nil {
		return err
	}
	return controller.drain(ctx)
}
