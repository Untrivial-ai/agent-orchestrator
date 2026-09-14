package chat_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

// A turn-scoped cancellation is intentionally narrower than Stop: it settles
// only the selected durable prompt and neither interrupts the provider nor
// changes the relative order of queue siblings.
func TestCancelSelectedQueuedTurnPreservesActiveTurnAndSiblingQueue(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	running, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "active work", ClientMessageID: "active", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("start active turn: %v", err)
	}
	cancelled, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "cancel me", ClientMessageID: "cancel", Origin: domain.MessageOriginAutomation,
	})
	if err != nil {
		t.Fatalf("queue selected turn: %v", err)
	}
	kept, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "keep me", ClientMessageID: "keep", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("queue sibling turn: %v", err)
	}

	if err := h.svc.CancelQueuedTurn(ctx, testSession, cancelled.ID); err != nil {
		t.Fatalf("cancel selected queued turn: %v", err)
	}

	snapshot, err := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	states := turnStateByText(t, snapshot)
	if states["active work"] != domain.TurnStateRunning {
		t.Errorf("active turn = %q, want running", states["active work"])
	}
	cancelledTurn, err := h.st.TurnByID(ctx, cancelled.ID)
	if err != nil || cancelledTurn.State != domain.TurnStateCancelled {
		t.Errorf("selected turn = %v, %v, want cancelled", cancelledTurn, err)
	}
	if _, visible := states["cancel me"]; visible {
		t.Error("cancelled queue message remained in visible transcript")
	}
	if states["keep me"] != domain.TurnStateQueued {
		t.Errorf("sibling turn = %q, want queued", states["keep me"])
	}
	if got := h.conv.sentTexts(); len(got) != 1 || got[0] != "active work" {
		t.Fatalf("provider received %v, want only active work", got)
	}
	next, err := h.st.NextQueuedTurn(ctx, h.ctrl.ConversationID())
	if err != nil || next.TurnID != kept.ID {
		t.Fatalf("remaining queue head = %+v, %v; want %s", next, err, kept.ID)
	}

	if err := h.svc.CancelQueuedTurn(ctx, testSession, running.ID); !errors.Is(err, chatsvc.ErrTurnNotQueued) {
		t.Fatalf("cancel running turn error = %v, want ErrTurnNotQueued", err)
	}
	if err := h.svc.CancelQueuedTurn(ctx, testSession, "missing-turn"); !errors.Is(err, domain.ErrNoConversationTurn) {
		t.Fatalf("cancel missing turn error = %v, want ErrNoConversationTurn", err)
	}
	if err := h.svc.CancelQueuedTurn(ctx, "wrong-session", kept.ID); !errors.Is(err, ports.ErrSessionNotFound) {
		t.Fatalf("cancel through wrong session error = %v, want ErrSessionNotFound", err)
	}
}

func TestRetiredProjectControllerCannotCancelReplacementQueue(t *testing.T) {
	h := newProjectHarnessWithConversation(t, nil)
	ctx := context.Background()
	const replacementTurnID = "replacement-queued-cancel"
	rebindProjectConversationAndQueue(t, h, replacementTurnID, "replacement-owned work")

	err := h.svc.CancelQueuedTurn(ctx, testSession, replacementTurnID)
	if !errors.Is(err, chatsvc.ErrControllerHandoff) {
		t.Fatalf("retired CancelQueuedTurn = %v, want ErrControllerHandoff", err)
	}
	turn, err := h.st.TurnByID(ctx, replacementTurnID)
	if err != nil {
		t.Fatalf("load replacement turn: %v", err)
	}
	if turn.State != domain.TurnStateQueued {
		t.Fatalf("replacement turn state = %q, want queued", turn.State)
	}
}

// Stop confirmation is a compare-and-set over the complete durable queue. If
// anything arrived, disappeared, or changed order while the dialog was open,
// the provider and every queued row must remain untouched until the user reviews
// the refreshed scope and confirms again.
func TestInterruptRejectsChangedQueuedScopeWithoutCancellingAnything(t *testing.T) {
	tests := []struct {
		name     string
		expected func(first, second string) []string
		mutate   func(*testing.T, *harness, string)
	}{
		{
			name:     "automation arrived",
			expected: func(first, _ string) []string { return []string{first} },
		},
		{
			name:     "confirmed item disappeared",
			expected: func(first, second string) []string { return []string{first, second} },
			mutate: func(t *testing.T, h *harness, first string) {
				t.Helper()
				if err := h.svc.CancelQueuedTurn(context.Background(), testSession, first); err != nil {
					t.Fatalf("settle confirmed item: %v", err)
				}
			},
		},
		{
			name:     "expected order changed",
			expected: func(first, second string) []string { return []string{second, first} },
		},
		{
			name:     "duplicate expected id",
			expected: func(first, _ string) []string { return []string{first, first} },
		},
		{
			name:     "expected ids omitted",
			expected: func(_, _ string) []string { return nil },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conv := newInterruptRecorder()
			h := newHarnessWithConversation(t, conv)
			ctx := context.Background()
			if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
				Text: "active work", ClientMessageID: "active", Origin: domain.MessageOriginHuman,
			}); err != nil {
				t.Fatalf("start active turn: %v", err)
			}
			conv.markActive("provider-turn-1")
			conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})

			first, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
				Text: "human follow-up", ClientMessageID: "human", Origin: domain.MessageOriginHuman,
			})
			if err != nil {
				t.Fatalf("queue human follow-up: %v", err)
			}
			second, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
				Text: "automation follow-up", ClientMessageID: "automation", Origin: domain.MessageOriginAutomation,
			})
			if err != nil {
				t.Fatalf("queue automation follow-up: %v", err)
			}
			if tt.mutate != nil {
				tt.mutate(t, h, first.ID)
			}

			err = h.svc.Interrupt(ctx, testSession, tt.expected(first.ID, second.ID))
			if !errors.Is(err, chatsvc.ErrQueueScopeChanged) {
				t.Fatalf("Interrupt error = %v, want ErrQueueScopeChanged", err)
			}
			if got := conv.attemptCount(); got != 0 {
				t.Fatalf("provider interrupt attempts = %d, want 0", got)
			}
			snapshot, err := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
			if err != nil {
				t.Fatalf("load snapshot: %v", err)
			}
			states := turnStateByText(t, snapshot)
			if states["active work"] != domain.TurnStateRunning {
				t.Fatalf("active turn = %q, want running", states["active work"])
			}
			if tt.mutate == nil && (states["human follow-up"] != domain.TurnStateQueued || states["automation follow-up"] != domain.TurnStateQueued) {
				t.Fatalf("queue states = human %q automation %q, want queued", states["human follow-up"], states["automation follow-up"])
			}
			if tt.mutate != nil && states["automation follow-up"] != domain.TurnStateQueued {
				t.Fatalf("surviving queue sibling = %q, want queued", states["automation follow-up"])
			}
		})
	}
}
