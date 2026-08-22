package chat_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

// Retry scenarios.
//
// The promise under test is the issue #4215 contract: a failed human turn can be
// re-dispatched as a NEW turn whose content the daemon reads from its own durable
// rows — never from a caller-supplied payload — while the original failed attempt
// stays failed and visible. Refusals are typed, not silent.

func failedTurnSnapshot(t *testing.T, h *harness, turnID string) store.ConversationSnapshot {
	t.Helper()
	return h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		for _, turn := range s.Turns {
			if turn.ID == turnID && turn.State == domain.TurnStateFailed {
				return true
			}
		}
		return false
	})
}

func turnByID(s store.ConversationSnapshot, id string) (domain.ConversationTurn, bool) {
	for _, turn := range s.Turns {
		if turn.ID == id {
			return turn, true
		}
	}
	return domain.ConversationTurn{}, false
}

func TestRetryTurnDispatchesFailedPromptAsNewTurn(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	turn, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text:            "summarize the failing CI run",
		ClientMessageID: "cm-1",
		Origin:          domain.MessageOriginHuman,
		Content: []ports.ChatContent{{
			Type: "resource", URI: "file:///worktree/ci-log.txt", Name: "ci-log.txt",
		}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if turn.State != domain.TurnStateRunning {
		t.Fatalf("turn state after dispatch = %q, want running", turn.State)
	}

	// The provider fails the turn asynchronously after a transient network loss.
	h.conv.emit(ports.ChatEvent{
		Kind:           ports.ChatEventTurnCompleted,
		ProviderTurnID: turn.ProviderTurnID,
		TurnState:      domain.TurnStateFailed,
		Err:            errors.New("stream disconnected before completion"),
	})
	snapshot := failedTurnSnapshot(t, h, turn.ID)
	failed, _ := turnByID(snapshot, turn.ID)
	if failed.ErrorMessage != "stream disconnected before completion" {
		t.Fatalf("failed turn error = %q, want the transport error", failed.ErrorMessage)
	}

	// Retry re-dispatches the same durable prompt as a brand-new turn.
	retried, err := h.svc.RetryTurn(ctx, testSession, turn.ID)
	if err != nil {
		t.Fatalf("RetryTurn: %v", err)
	}
	if retried.ID == "" || retried.ID == turn.ID {
		t.Fatalf("retried turn id = %q, want a new turn distinct from %q", retried.ID, turn.ID)
	}
	if retried.State != domain.TurnStateRunning {
		t.Fatalf("retried turn state = %q, want running", retried.State)
	}

	// The provider received exactly two sends, with the same prompt text.
	sent := h.conv.sentTexts()
	if len(sent) != 2 {
		t.Fatalf("provider received %d sends, want 2: %v", len(sent), sent)
	}
	if sent[1] != "summarize the failing CI run" {
		t.Fatalf("second send text = %q, want the original prompt", sent[1])
	}

	// Both attempts are durable: the original is still failed, the retry is a
	// separate turn.
	after := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		_, ok := turnByID(s, retried.ID)
		return ok && len(s.Turns) == 2
	})
	original, ok := turnByID(after, turn.ID)
	if !ok || original.State != domain.TurnStateFailed {
		t.Fatalf("original turn after retry = %+v, want it to remain failed", original)
	}
	if original.ErrorMessage != "stream disconnected before completion" {
		t.Fatalf("original turn error changed to %q, want it preserved", original.ErrorMessage)
	}
}

func TestRetryTurnRefusesNonFailedTurn(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	turn, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text:            "what changed?",
		ClientMessageID: "cm-2",
		Origin:          domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Complete the turn, so it is terminal but not failed.
	h.conv.emit(ports.ChatEvent{
		Kind:           ports.ChatEventTurnCompleted,
		ProviderTurnID: turn.ProviderTurnID,
		TurnState:      domain.TurnStateCompleted,
	})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		t, ok := turnByID(s, turn.ID)
		return ok && t.State == domain.TurnStateCompleted
	})

	if _, err := h.svc.RetryTurn(ctx, testSession, turn.ID); !errors.Is(err, chatsvc.ErrTurnNotRetryable) {
		t.Fatalf("RetryTurn on a completed turn = %v, want ErrTurnNotRetryable", err)
	}
	// Nothing was re-dispatched.
	if sent := h.conv.sentTexts(); len(sent) != 1 {
		t.Fatalf("provider received %d sends after refused retry, want 1", len(sent))
	}
}

func TestRetryTurnRefusesWhileBusy(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// First turn fails, leaving it eligible for retry.
	turn, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text:            "first",
		ClientMessageID: "cm-3",
		Origin:          domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	h.conv.emit(ports.ChatEvent{
		Kind:           ports.ChatEventTurnCompleted,
		ProviderTurnID: turn.ProviderTurnID,
		TurnState:      domain.TurnStateFailed,
		Err:            errors.New("boom"),
	})
	failedTurnSnapshot(t, h, turn.ID)

	// A second turn is now running.
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text:            "second",
		ClientMessageID: "cm-4",
		Origin:          domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("Send second: %v", err)
	}

	// Retrying the failed first turn is refused while another turn runs.
	if _, err := h.svc.RetryTurn(ctx, testSession, turn.ID); !errors.Is(err, chatsvc.ErrTurnRunning) {
		t.Fatalf("RetryTurn while busy = %v, want ErrTurnRunning", err)
	}
}

func TestRetryTurnRefusesNonHumanPrompt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	turn, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text:            "automated relay",
		ClientMessageID: "cm-5",
		Origin:          domain.MessageOriginAutomation,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	h.conv.emit(ports.ChatEvent{
		Kind:           ports.ChatEventTurnCompleted,
		ProviderTurnID: turn.ProviderTurnID,
		TurnState:      domain.TurnStateFailed,
		Err:            errors.New("boom"),
	})
	failedTurnSnapshot(t, h, turn.ID)

	if _, err := h.svc.RetryTurn(ctx, testSession, turn.ID); !errors.Is(err, chatsvc.ErrTurnNotRetryable) {
		t.Fatalf("RetryTurn on automation-origin turn = %v, want ErrTurnNotRetryable", err)
	}
}

func TestRetryTurnDerivesDistinctIdempotencyKeys(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	turn, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text:            "retry me",
		ClientMessageID: "cm-6",
		Origin:          domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	h.conv.emit(ports.ChatEvent{
		Kind:           ports.ChatEventTurnCompleted,
		ProviderTurnID: turn.ProviderTurnID,
		TurnState:      domain.TurnStateFailed,
		Err:            errors.New("boom"),
	})
	failedTurnSnapshot(t, h, turn.ID)

	first, err := h.svc.RetryTurn(ctx, testSession, turn.ID)
	if err != nil {
		t.Fatalf("first RetryTurn: %v", err)
	}
	// The first retry attempt also fails, so the source is still eligible and a
	// second retry must derive a different key and open a third turn.
	h.conv.emit(ports.ChatEvent{
		Kind:           ports.ChatEventTurnCompleted,
		ProviderTurnID: first.ProviderTurnID,
		TurnState:      domain.TurnStateFailed,
		Err:            errors.New("still down"),
	})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		t, ok := turnByID(s, first.ID)
		return ok && t.State == domain.TurnStateFailed
	})

	second, err := h.svc.RetryTurn(ctx, testSession, turn.ID)
	if err != nil {
		t.Fatalf("second RetryTurn: %v", err)
	}
	if second.ID == first.ID || second.ID == turn.ID {
		t.Fatalf("second retry id = %q, want a new turn (first=%q, source=%q)", second.ID, first.ID, turn.ID)
	}
	if got := h.conv.sentTexts(); len(got) != 3 {
		t.Fatalf("provider received %d sends, want 3: %v", len(got), got)
	}
}
