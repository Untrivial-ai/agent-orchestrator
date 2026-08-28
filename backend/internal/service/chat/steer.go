package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Steering: guidance delivered into a turn that is already running.
//
// Why this is not "interrupt, then send again". An interrupt destroys the turn —
// its reasoning, its half-applied edits, the command it has running — and the
// resend starts from nothing. A steer adds the correction to the SAME turn and
// leaves the work in place; the agent decides what to abandon. Measured against
// codex-cli 0.146.0, the steered turn kept its id and settled `completed`, having
// abandoned the command it was running on its own. So the user's guidance costs
// them nothing they had already paid for, which is the entire reason to prefer it.
//
// Optional and feature-detected, following the Models precedent: a provider that
// cannot steer gets a typed refusal a client renders as an absent control rather
// than as a failed request.

// Typed outcomes for steering. Each distinguishes advice a client can act on:
// "this agent cannot do it", "send it as a new message instead", "wait, then try
// again".
var (
	// ErrSteerUnsupported reports a driver whose provider cannot take guidance into
	// a running turn. Permanent for that harness — a client should stop offering the
	// control rather than retry.
	ErrSteerUnsupported = errors.New("chat driver cannot steer a running turn")
	// ErrTurnNotSteerable reports a turn that is running but cannot absorb guidance.
	// Codex refuses a compaction or review turn this way. Retryable once that turn
	// finishes, which is what separates it from ErrSteerUnsupported.
	ErrTurnNotSteerable = errors.New("the running turn cannot take guidance")
	// ErrSteerTextRequired refuses an empty steer. There is no keystroke concept in
	// Chat mode: an empty body is a caller bug, not a way to nudge the agent.
	ErrSteerTextRequired = errors.New("steer text is required")
	// ErrTurnNotQueued reports a selected source that has already dispatched,
	// settled, or been claimed by another promotion.
	ErrTurnNotQueued = errors.New("turn is not queued")
	// ErrPromotionUncertain prevents automatic redelivery after the provider may
	// have accepted guidance but AO could not durably record the result.
	ErrPromotionUncertain = errors.New("queued turn promotion delivery is uncertain")
	// ErrSteerDeliveryUncertain means AO reserved an idempotency handle but cannot
	// prove whether the provider accepted it. Reusing the handle is safe and must
	// never contact the provider again; inventing a fresh handle is not.
	ErrSteerDeliveryUncertain = errors.New("steer delivery is uncertain")
	// ErrSteerIdempotencyConflict refuses reuse of one handle for different
	// guidance. Dispatching either request would make the handle lie about identity.
	ErrSteerIdempotencyConflict = errors.New("steer idempotency handle belongs to different guidance")
	// ErrSteerContentUnsupported means the provider cannot accept every structured
	// block. The whole queued message stays undelivered.
	ErrSteerContentUnsupported = errors.New("steer content is unsupported")
)

// SteerResult is what a steer did, for a client that has to attribute it.
type SteerResult struct {
	// ProviderTurnID is the turn that absorbed the guidance. Reported rather than
	// assumed to be the one asked for: the provider names it in its own response,
	// and a client matches it against the snapshot's turns.
	ProviderTurnID string
	// ActivityID is the timeline row recording the guidance, so a client can
	// reconcile an optimistic bubble with the durable one instead of showing both.
	ActivityID string
}

// PromoteQueuedTurnResult attributes a durable queue item to the running turn
// that absorbed it.
type PromoteQueuedTurnResult struct {
	SourceTurnID   string
	ProviderTurnID string
	ActivityID     string
}

// maxSteerSummaryRunes bounds the one-line label a steer lands on the timeline
// with. The full text is always kept in the activity's detail payload; this is only
// what a collapsed row shows.
const maxSteerSummaryRunes = 120

// Steer sends guidance into this session's running turn.
//
// Dispatch is decided from the persisted session mode like every other Chat
// command, so a TUI session cannot reach this path by calling the URL directly.
func (s *Service) Steer(
	ctx context.Context,
	id domain.SessionID,
	msg ports.ChatUserMessage,
) (SteerResult, error) {
	if strings.TrimSpace(msg.Text) == "" {
		return SteerResult{}, ErrSteerTextRequired
	}
	if _, err := s.requireChatSession(ctx, id); err != nil {
		return SteerResult{}, err
	}
	controller, err := s.Controller(id)
	if err != nil {
		return SteerResult{}, err
	}
	return controller.Steer(ctx, msg)
}

// PromoteQueuedTurn delivers one already queued turn into the active turn. The
// daemon reads the queued content; callers identify it but cannot replace it.
func (s *Service) PromoteQueuedTurn(
	ctx context.Context,
	id domain.SessionID,
	turnID string,
) (PromoteQueuedTurnResult, error) {
	if _, err := s.requireChatSession(ctx, id); err != nil {
		return PromoteQueuedTurnResult{}, err
	}
	controller, err := s.Controller(id)
	if err != nil {
		return PromoteQueuedTurnResult{}, err
	}
	if _, ok := controller.conv.(ports.ChatSteerer); !ok {
		return PromoteQueuedTurnResult{}, ErrSteerUnsupported
	}
	return controller.PromoteQueuedTurn(ctx, turnID)
}

// PromoteQueuedTurn reserves a durable queue item, asks the provider to absorb
// it, then records the destination and retires the source as one store operation.
func (c *Controller) PromoteQueuedTurn(
	ctx context.Context,
	turnID string,
) (PromoteQueuedTurnResult, error) {
	steerer, ok := c.conv.(ports.ChatSteerer)
	if !ok {
		return PromoteQueuedTurnResult{}, ErrSteerUnsupported
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.handoffActive() {
		return PromoteQueuedTurnResult{}, ErrControllerHandoff
	}

	source, err := c.store.TurnByID(ctx, turnID)
	if err != nil {
		return PromoteQueuedTurnResult{}, err
	}
	if source.ConversationID != c.conversation.ID {
		return PromoteQueuedTurnResult{}, fmt.Errorf("%w: %s", domain.ErrNoConversationTurn, turnID)
	}
	if source.State != domain.TurnStateQueued {
		return PromoteQueuedTurnResult{}, fmt.Errorf("%w: %s", ErrTurnNotQueued, turnID)
	}

	target, ok := c.awaitAcknowledgedTurn(ctx)
	if !ok {
		return PromoteQueuedTurnResult{}, ErrNoActiveTurn
	}
	queued, err := c.store.ReserveQueuedTurnForPromotion(ctx, c.conversation.ID, turnID, c.now())
	if err != nil {
		return PromoteQueuedTurnResult{}, fmt.Errorf("%w: %s: %w", ErrTurnNotQueued, turnID, err)
	}
	release := func() {
		if releaseErr := c.store.ReleaseQueuedTurnPromotion(
			context.WithoutCancel(ctx), c.conversation.ID, turnID); releaseErr != nil {
			c.log.Error("failed to release queued turn promotion", "turn", turnID, "error", releaseErr)
		}
	}
	if queued.Origin != domain.MessageOriginHuman {
		release()
		return PromoteQueuedTurnResult{}, fmt.Errorf("%w: %s", ErrTurnNotQueued, turnID)
	}
	settleUncertain := func(cause error) error {
		// A transport error commonly cancels the request context after the provider
		// may already have accepted the steer. The durable safety transition must
		// survive that cancellation or the source remains visibly queued forever.
		settleCtx := context.WithoutCancel(ctx)
		if settleErr := c.store.SettleTurnByID(
			settleCtx, turnID, domain.TurnStateFailed, ErrPromotionUncertain.Error(), c.now()); settleErr != nil {
			c.log.Error("failed to settle uncertain queued turn promotion",
				"turn", turnID, "cause", cause, "error", settleErr)
		}
		return fmt.Errorf("%w: %w", ErrPromotionUncertain, cause)
	}

	var content []ports.ChatContent
	if queued.DeliveryContentJSON != "" {
		if err := json.Unmarshal([]byte(queued.DeliveryContentJSON), &content); err != nil {
			release()
			return PromoteQueuedTurnResult{}, fmt.Errorf("decode queued chat content: %w", err)
		}
	}
	msg := ports.ChatUserMessage{
		Text: queued.Text, Content: content, Origin: queued.Origin,
		ClientMessageID: queued.ClientMessageID,
	}
	ref, err := steerer.Steer(ctx, target, msg)
	if err != nil {
		classified := classify(err)
		switch {
		case errors.Is(err, ports.ErrChatNoSteerableTurn):
			release()
			return PromoteQueuedTurnResult{}, ErrNoActiveTurn
		case errors.Is(err, ports.ErrChatTurnNotSteerable):
			release()
			return PromoteQueuedTurnResult{}, fmt.Errorf("%w: %w", ErrTurnNotSteerable, err)
		case errors.Is(err, ports.ErrChatSteerContentUnsupported):
			release()
			return PromoteQueuedTurnResult{}, fmt.Errorf("%w: %w", ErrSteerContentUnsupported, err)
		case errors.Is(classified, ErrProviderRefused):
			release()
			return PromoteQueuedTurnResult{}, classified
		default:
			return PromoteQueuedTurnResult{}, settleUncertain(
				fmt.Errorf("steer queued turn %s: %w", turnID, err))
		}
	}
	landed := ref.ProviderTurnID
	if landed == "" {
		landed = target
	}
	activityID := c.newID()
	activity, err := makeSteerActivity(activityID, msg, turnID)
	if err != nil {
		return PromoteQueuedTurnResult{}, settleUncertain(err)
	}
	if err := c.store.CompleteQueuedTurnPromotion(
		ctx, c.conversation.ID, turnID, landed, activity, c.now()); err != nil {
		return PromoteQueuedTurnResult{}, settleUncertain(err)
	}
	return PromoteQueuedTurnResult{
		SourceTurnID: turnID, ProviderTurnID: landed, ActivityID: activityID,
	}, nil
}

type durableSteerRefusal struct {
	kind           domain.ConversationSteerRejectionKind
	cause          error
	reserveContext string
	persistContext string
}

func (c *Controller) rejectSteerBeforeDispatch(
	ctx context.Context,
	clientMessageID, requestJSON string,
	refusal durableSteerRefusal,
) (SteerResult, error) {
	if clientMessageID == "" {
		return SteerResult{}, refusal.cause
	}
	delivery, created, err := c.store.ReserveSteerDelivery(
		ctx, c.conversation.ID, clientMessageID, requestJSON, c.now())
	if err != nil {
		return SteerResult{}, fmt.Errorf("%w: %s: %w",
			ErrSteerDeliveryUncertain, refusal.reserveContext, err)
	}
	if !created {
		return replaySteerDelivery(delivery, requestJSON)
	}
	if err := c.store.RejectSteerDelivery(
		context.WithoutCancel(ctx), c.conversation.ID, clientMessageID,
		refusal.kind, refusal.cause.Error(), c.now()); err != nil {
		return SteerResult{}, fmt.Errorf("%w: %s: %w",
			ErrSteerDeliveryUncertain, refusal.persistContext, err)
	}
	return SteerResult{}, refusal.cause
}

// Steer hands guidance to the provider for the turn currently in flight, then
// records it on that turn.
//
// The wait for acknowledgement is load-bearing, not defensive. Codex refuses a
// steer for a turn it has accepted but not yet announced — probed directly: a steer
// sent immediately after turn/start returned, carrying the id turn/start itself had
// just handed back, came back "no active turn to steer". Steering is most useful in
// exactly that window, the second after someone realizes they sent the wrong thing,
// so the same helper Interrupt uses to ride out that gap is used here.
//
// AO durably reserves a caller-supplied client handle before provider I/O. A
// definitive refusal settles that handle as rejected, success records the visible
// activity and accepted result atomically, and any gap between those facts remains
// uncertain forever. That fail-closed state is what makes a retry unable to deliver
// the same guidance twice even when the provider ignores the client handle.
func (c *Controller) Steer(ctx context.Context, msg ports.ChatUserMessage) (SteerResult, error) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	requestJSON, err := encodeSteerDeliveryRequest(msg)
	if err != nil {
		return SteerResult{}, err
	}
	if msg.ClientMessageID != "" {
		delivery, found, loadErr := c.store.SteerDelivery(
			ctx, c.conversation.ID, msg.ClientMessageID)
		if loadErr != nil {
			return SteerResult{}, fmt.Errorf("%w: load prior result: %w",
				ErrSteerDeliveryUncertain, loadErr)
		}
		if found {
			return replaySteerDelivery(delivery, requestJSON)
		}
	}
	if c.handoffActive() {
		return c.rejectSteerBeforeDispatch(ctx, msg.ClientMessageID, requestJSON,
			durableSteerRefusal{
				kind:           domain.ConversationSteerRejectedInterfaceTransition,
				cause:          ErrControllerHandoff,
				reserveContext: "reserve interface-transition refusal",
				persistContext: "persist interface-transition refusal",
			})
	}
	steerer, ok := c.conv.(ports.ChatSteerer)
	if !ok {
		return c.rejectSteerBeforeDispatch(ctx, msg.ClientMessageID, requestJSON,
			durableSteerRefusal{
				kind:           domain.ConversationSteerRejectedUnsupported,
				cause:          ErrSteerUnsupported,
				reserveContext: "reserve unsupported result",
				persistContext: "persist unsupported result",
			})
	}

	turn, ok := c.awaitAcknowledgedTurn(ctx)
	if !ok {
		// Nothing is in flight. Reusing the interrupt sentinel keeps one code for
		// "there is no turn" across every command that needs one; the endpoint says
		// what to do about it.
		return c.rejectSteerBeforeDispatch(ctx, msg.ClientMessageID, requestJSON,
			durableSteerRefusal{
				kind:           domain.ConversationSteerRejectedNoActiveTurn,
				cause:          ErrNoActiveTurn,
				reserveContext: "reserve refusal",
				persistContext: "persist no-active-turn refusal",
			})
	}
	if msg.ClientMessageID != "" {
		delivery, created, reserveErr := c.store.ReserveSteerDelivery(
			ctx, c.conversation.ID, msg.ClientMessageID, requestJSON, c.now())
		if reserveErr != nil {
			return SteerResult{}, fmt.Errorf("%w: reserve delivery: %w",
				ErrSteerDeliveryUncertain, reserveErr)
		}
		if !created {
			return replaySteerDelivery(delivery, requestJSON)
		}
	}

	ref, err := steerer.Steer(ctx, turn, msg)
	if err != nil {
		kind, definitive, refused := classifySteerRejection(err)
		if definitive {
			if msg.ClientMessageID != "" {
				if rejectErr := c.store.RejectSteerDelivery(
					context.WithoutCancel(ctx), c.conversation.ID, msg.ClientMessageID,
					kind, refused.Error(), c.now()); rejectErr != nil {
					return SteerResult{}, fmt.Errorf("%w: persist provider refusal: %w",
						ErrSteerDeliveryUncertain, rejectErr)
				}
			}
			return SteerResult{}, refused
		}
		wrapped := classify(fmt.Errorf("steer turn %s: %w", turn, err))
		if msg.ClientMessageID != "" {
			return SteerResult{}, fmt.Errorf("%w: %w", ErrSteerDeliveryUncertain, wrapped)
		}
		return SteerResult{}, wrapped
	}

	landed := ref.ProviderTurnID
	if landed == "" {
		landed = turn
	}
	if msg.ClientMessageID == "" {
		activityID, recordErr := c.recordSteer(ctx, landed, msg)
		if recordErr != nil {
			// The guidance IS with the agent; only AO's record of it failed. Reporting the
			// error rather than swallowing it, because a steer the timeline never mentions
			// is a conversation whose next answer has no visible cause.
			return SteerResult{ProviderTurnID: landed}, recordErr
		}
		return SteerResult{ProviderTurnID: landed, ActivityID: activityID}, nil
	}
	activityID := c.newID()
	activity, err := makeSteerActivity(activityID, msg, "")
	if err != nil {
		return SteerResult{}, fmt.Errorf("%w: %w", ErrSteerDeliveryUncertain, err)
	}
	if err := c.store.CompleteSteerDelivery(
		context.WithoutCancel(ctx), c.conversation.ID, msg.ClientMessageID,
		landed, activity, c.now()); err != nil {
		return SteerResult{}, fmt.Errorf("%w: %w", ErrSteerDeliveryUncertain, err)
	}
	return SteerResult{ProviderTurnID: landed, ActivityID: activityID}, nil
}

type steerDeliveryRequest struct {
	Text     string                       `json:"text"`
	Content  []ports.ChatContent          `json:"content,omitempty"`
	Origin   domain.MessageOrigin         `json:"origin"`
	Settings steerDeliveryRequestSettings `json:"settings"`
}

// Explicit JSON names freeze the durable request identity independently of the
// provider port's Go field names. A later refactor must not turn a safe retry into
// an idempotency conflict after an app upgrade.
type steerDeliveryRequestSettings struct {
	Model    string               `json:"model,omitempty"`
	Effort   string               `json:"effort,omitempty"`
	Approval ports.PermissionMode `json:"approval,omitempty"`
}

func encodeSteerDeliveryRequest(msg ports.ChatUserMessage) (string, error) {
	encoded, err := json.Marshal(steerDeliveryRequest{
		Text: msg.Text, Content: msg.Content, Origin: normalizeOrigin(msg.Origin),
		Settings: steerDeliveryRequestSettings{
			Model: msg.Settings.Model, Effort: msg.Settings.Effort, Approval: msg.Settings.Approval,
		},
	})
	if err != nil {
		return "", fmt.Errorf("encode steer delivery request: %w", err)
	}
	return string(encoded), nil
}

func replaySteerDelivery(
	delivery domain.ConversationSteerDelivery,
	requestJSON string,
) (SteerResult, error) {
	if delivery.RequestJSON != requestJSON {
		return SteerResult{}, ErrSteerIdempotencyConflict
	}
	switch delivery.State {
	case domain.ConversationSteerAccepted:
		return SteerResult{
			ProviderTurnID: delivery.ProviderTurnID,
			ActivityID:     delivery.ActivityID,
		}, nil
	case domain.ConversationSteerRejected:
		return SteerResult{}, replaySteerRejection(delivery)
	case domain.ConversationSteerReserved:
		return SteerResult{}, ErrSteerDeliveryUncertain
	default:
		return SteerResult{}, fmt.Errorf("%w: invalid durable state %q",
			ErrSteerDeliveryUncertain, delivery.State)
	}
}

type storedSteerRejection struct {
	message string
	cause   error
}

func (e storedSteerRejection) Error() string { return e.message }
func (e storedSteerRejection) Unwrap() error { return e.cause }

func replaySteerRejection(delivery domain.ConversationSteerDelivery) error {
	var cause error
	switch delivery.RejectionKind {
	case domain.ConversationSteerRejectedNoActiveTurn:
		cause = ErrNoActiveTurn
	case domain.ConversationSteerRejectedUnsupported:
		cause = ErrSteerUnsupported
	case domain.ConversationSteerRejectedTurnNotSteerable:
		cause = ErrTurnNotSteerable
	case domain.ConversationSteerRejectedContentUnsupported:
		cause = ErrSteerContentUnsupported
	case domain.ConversationSteerRejectedByProvider:
		cause = ErrProviderRefused
	case domain.ConversationSteerRejectedInterfaceTransition:
		cause = ErrControllerHandoff
	default:
		return fmt.Errorf("%w: invalid durable rejection %q",
			ErrSteerDeliveryUncertain, delivery.RejectionKind)
	}
	return storedSteerRejection{message: delivery.RejectionMessage, cause: cause}
}

func classifySteerRejection(
	err error,
) (domain.ConversationSteerRejectionKind, bool, error) {
	switch {
	case errors.Is(err, ports.ErrChatNoSteerableTurn):
		// The turn ended, or was replaced, between AO's check and the provider's.
		// The provider is the authority on that, and losing the race is ordinary.
		return domain.ConversationSteerRejectedNoActiveTurn, true, ErrNoActiveTurn
	case errors.Is(err, ports.ErrChatTurnNotSteerable):
		return domain.ConversationSteerRejectedTurnNotSteerable, true,
			fmt.Errorf("%w: %w", ErrTurnNotSteerable, err)
	case errors.Is(err, ports.ErrChatSteerContentUnsupported):
		return domain.ConversationSteerRejectedContentUnsupported, true,
			fmt.Errorf("%w: %w", ErrSteerContentUnsupported, err)
	}
	classified := classify(err)
	if errors.Is(classified, ErrProviderRefused) {
		return domain.ConversationSteerRejectedByProvider, true, classified
	}
	return "", false, classified
}

// recordSteer writes the guidance onto the turn that took it.
//
// It lands as an activity, not as a message, and the reason is structural rather
// than semantic: a steer joins a turn that is already running, and AO's only durable
// write that can attach to a turn in flight is the activity row. AppendUserMessage
// opens a NEW turn — using it here would mint a second turn row that the drain loop
// would later dispatch as its own turn, sending the user's correction twice. The row
// is tagged `event: "steer"` in its detail so a client can render it as the user's
// own words rather than as a system notice, the same way compaction entries are
// identified by their discriminator instead of by the general `system` kind.
//
// The provider's own echo is not the record. It replays the guidance as a
// `userMessage` item on the turn, but the driver drops those: AO records what the
// user said when it accepts the request, and re-emitting the echo would show it
// twice.
func (c *Controller) recordSteer(
	ctx context.Context,
	providerTurnID string,
	msg ports.ChatUserMessage,
) (string, error) {
	id := c.newID()
	activity, err := makeSteerActivity(id, msg, "")
	if err != nil {
		return "", err
	}
	if err := c.store.UpsertActivity(ctx, c.conversation.ID, providerTurnID,
		activity, c.now()); err != nil {
		return "", fmt.Errorf("record steer on turn %s: %w", providerTurnID, err)
	}
	return id, nil
}

func makeSteerActivity(
	id string,
	msg ports.ChatUserMessage,
	sourceTurnID string,
) (domain.ConversationActivity, error) {
	detail := map[string]any{
		"event":  "steer",
		"text":   msg.Text,
		"origin": string(normalizeOrigin(msg.Origin)),
	}
	if msg.ClientMessageID != "" {
		detail["clientMessageId"] = msg.ClientMessageID
	}
	if sourceTurnID != "" {
		detail["sourceTurnId"] = sourceTurnID
	}
	if len(msg.Content) > 0 {
		detail["content"] = msg.Content
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		return domain.ConversationActivity{}, fmt.Errorf("encode steer detail: %w", err)
	}
	return domain.ConversationActivity{
		ID:     id,
		Kind:   domain.ActivityKindSystem,
		Status: domain.ActivityStatusCompleted,
		// Delivered, not pending: the provider accepted it before this ran.
		Summary: steerSummary(msg.Text),
		Detail:  encoded,
		// A synthetic item key, so a client retrying with the same idempotency
		// handle updates this row instead of adding a second one. Prefixed because
		// this id is AO's, not the provider's, and the two share a column.
		ProviderItemID: steerItemID(msg.ClientMessageID),
	}, nil
}

// steerItemID is the dedupe key for a recorded steer, or empty when the caller
// supplied no idempotency handle and every request is a distinct piece of guidance.
func steerItemID(clientMessageID string) string {
	if clientMessageID == "" {
		return ""
	}
	return "steer:" + clientMessageID
}

// steerSummary reduces guidance to a one-line label. The full text stays in the
// detail payload, so this only decides what a collapsed row reads like.
func steerSummary(text string) string {
	collapsed := strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(collapsed) <= maxSteerSummaryRunes {
		return collapsed
	}
	runes := []rune(collapsed)
	return strings.TrimRight(string(runes[:maxSteerSummaryRunes]), " ") + "…"
}
