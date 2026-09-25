package chat_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

// assertTurnStaysQueued fails if the named turn leaves queued within a short settle
// window. A drain that should not have run would move it to running almost at once.
func assertTurnStaysQueued(t *testing.T, h *harness, text string) {
	t.Helper()
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		snapshot, err := h.st.LoadConversationSnapshot(context.Background(), h.ctrl.ConversationID())
		if err != nil {
			t.Fatalf("load snapshot: %v", err)
		}
		if got := turnStateByText(t, snapshot)[text]; got != domain.TurnStateQueued {
			t.Fatalf("queued turn %q became %q; a non-success terminal turn must hold the queue", text, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A failed primary turn must not release queued work into the same outage; only a
// completed turn may. Regression for issue #4861.
func TestFailedPrimaryTurnHoldsQueuedWork(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "root", ClientMessageID: "c1", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("send root: %v", err)
	}
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["root"] == domain.TurnStateRunning
	})

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "automation", ClientMessageID: "c2", Origin: domain.MessageOriginAutomation,
	}); err != nil {
		t.Fatalf("queue automation: %v", err)
	}

	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
		TurnState: domain.TurnStateFailed,
	})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["root"] == domain.TurnStateFailed
	})

	assertTurnStaysQueued(t, h, "automation")
	if got := h.conv.sentTexts(); len(got) != 1 || got[0] != "root" {
		t.Fatalf("provider received %v, want only the root prompt", got)
	}
}

// A recovered turn is terminal but carries no portable outcome, so it is no proof
// the next turn will succeed. It holds the queue like a failed turn.
func TestRecoveredPrimaryTurnHoldsQueuedWork(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "root", ClientMessageID: "c1", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("send root: %v", err)
	}
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["root"] == domain.TurnStateRunning
	})

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "automation", ClientMessageID: "c2", Origin: domain.MessageOriginAutomation,
	}); err != nil {
		t.Fatalf("queue automation: %v", err)
	}

	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
		TurnState: domain.TurnStateRecovered,
	})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["root"] == domain.TurnStateRecovered
	})

	assertTurnStaysQueued(t, h, "automation")
	if got := h.conv.sentTexts(); len(got) != 1 || got[0] != "root" {
		t.Fatalf("provider received %v, want only the root prompt", got)
	}
}

// A held queue is the only work left in the conversation, so Stop must be able to
// cancel it. Refusing left the UI showing a turn nobody could stop.
func TestStopCancelsQueueHeldByFailedTurn(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "root", ClientMessageID: "c1", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("send root: %v", err)
	}
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["root"] == domain.TurnStateRunning
	})

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "held", ClientMessageID: "c2", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("queue held: %v", err)
	}

	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
		TurnState: domain.TurnStateFailed,
	})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["root"] == domain.TurnStateFailed
	})

	if err := h.svc.Interrupt(ctx, testSession); err != nil {
		t.Fatalf("interrupt with a held queue: %v", err)
	}
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["held"] == domain.TurnStateInterrupted
	})
	if got := h.conv.sentTexts(); len(got) != 1 || got[0] != "root" {
		t.Fatalf("provider received %v, want only the root prompt", got)
	}

	// With the queue gone there is nothing left to stop, and Stop says so.
	if err := h.svc.Interrupt(ctx, testSession); !errors.Is(err, chatsvc.ErrNoActiveTurn) {
		t.Fatalf("interrupt on an idle conversation = %v, want ErrNoActiveTurn", err)
	}
}

type failingHeldQueueCancellationStore struct {
	chatsvc.Store
	err error
}

func (s *failingHeldQueueCancellationStore) CancelQueuedTurns(
	context.Context,
	string,
	time.Time,
	time.Time,
) error {
	return s.err
}

func TestStopReportsHeldQueueCancellationFailure(t *testing.T) {
	cancelErr := errors.New("injected held queue cancellation failure")
	h := newHarnessWithConversationAndStore(t, nil, func(st *store.Store) chatsvc.Store {
		return &failingHeldQueueCancellationStore{Store: st, err: cancelErr}
	})
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "root", ClientMessageID: "c1", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("send root: %v", err)
	}
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "held", ClientMessageID: "c2", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("queue held: %v", err)
	}
	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
		TurnState: domain.TurnStateFailed,
	})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["root"] == domain.TurnStateFailed
	})

	if err := h.svc.Interrupt(ctx, testSession); !errors.Is(err, cancelErr) {
		t.Fatalf("interrupt with a cancellation failure = %v, want %v", err, cancelErr)
	}
}
