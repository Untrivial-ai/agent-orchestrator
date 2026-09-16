package chat_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

func TestStreamingReplyCoalescesDeltaPersistence(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	turn, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "Reply briefly", ClientMessageID: "batched-reply", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: turn.ProviderTurnID})
	const chunks = 110
	for range chunks {
		h.conv.emit(ports.ChatEvent{
			Kind: ports.ChatEventMessageDelta, ProviderTurnID: turn.ProviderTurnID,
			ProviderItemID: "reply", Delta: "hello world ",
		})
	}
	// Completing the turn must flush pending text even when the provider does
	// not send a separate settled-message event.
	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: turn.ProviderTurnID,
		TurnState: domain.TurnStateCompleted,
	})
	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateCompleted
	})
	want := strings.Repeat("hello world ", chunks)
	if len(snapshot.Messages) != 2 || snapshot.Messages[1].Text != want || snapshot.Messages[1].Streaming {
		t.Fatalf("completed reply = %+v", snapshot.Messages)
	}
	events, err := h.st.ProviderEventsSince(ctx, h.ctrl.ConversationID(), 0, chunks+10)
	if err != nil {
		t.Fatal(err)
	}
	// A synchronous burst should need only a few projections, not one archive
	// transaction and CDC invalidation for every provider transport fragment.
	if len(events) > 5 {
		t.Fatalf("archived %d events for one short reply, want at most 5", len(events))
	}
}

func TestStreamingReplyPreservesNativeEventDeduplication(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	turn, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "Reply briefly", ClientMessageID: "replayed-delta", Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatal(err)
	}
	delta := ports.ChatEvent{
		Kind: ports.ChatEventMessageDelta, ProviderTurnID: turn.ProviderTurnID,
		ProviderItemID: "reply", ProviderEventID: "native-event", Delta: "once",
	}
	h.conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: turn.ProviderTurnID},
		delta, delta,
		ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: turn.ProviderTurnID, TurnState: domain.TurnStateCompleted},
	)
	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateCompleted
	})
	if len(snapshot.Messages) != 2 || snapshot.Messages[1].Text != "once" {
		t.Fatalf("replayed delta changed the reply: %+v", snapshot.Messages)
	}
}
