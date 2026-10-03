package chat_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

type enospcOnceStore struct {
	chatsvc.Store
	failed bool
}

func (s *enospcOnceStore) AppendUserMessage(ctx context.Context, conversationID string, session domain.SessionID, generation string, msg domain.ConversationMessage, turnID string, now time.Time) (bool, error) {
	if !s.failed {
		s.failed = true
		return false, syscall.ENOSPC
	}
	return s.Store.AppendUserMessage(ctx, conversationID, session, generation, msg, turnID, now)
}

type enospcBindStore struct {
	chatsvc.Store
	failed bool
}

type reconnectedReceiptConversation struct{ *receiptConversation }

func (*reconnectedReceiptConversation) ReconnectedLive() bool { return true }

func (s *enospcBindStore) BindTurnToProvider(ctx context.Context, turnID, providerTurnID string, now time.Time) error {
	if !s.failed {
		s.failed = true
		return syscall.ENOSPC
	}
	return s.Store.BindTurnToProvider(ctx, turnID, providerTurnID, now)
}

func TestSendRetryAfterENOSPCDoesNotDuplicateProviderTurn(t *testing.T) {
	h := newHarnessWithConversationAndStore(t, nil, func(st *store.Store) chatsvc.Store {
		return &enospcOnceStore{Store: st}
	})
	ctx := context.Background()
	msg := ports.ChatUserMessage{Text: "inspect this", ClientMessageID: "enospc-retry", Origin: domain.MessageOriginHuman}

	if _, err := h.svc.Send(ctx, testSession, msg); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("first Send error = %v, want ENOSPC", err)
	}
	if got := h.conv.sendCallCount(); got != 0 {
		t.Fatalf("provider calls after failed write = %d, want 0", got)
	}
	before, err := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
	if err != nil || len(before.Turns) != 0 || len(before.Messages) != 0 {
		t.Fatalf("snapshot after failed write: turns=%d messages=%d err=%v", len(before.Turns), len(before.Messages), err)
	}

	if _, err := h.svc.Send(ctx, testSession, msg); err != nil {
		t.Fatalf("retry after recovery: %v", err)
	}
	if _, err := h.svc.Send(ctx, testSession, msg); err != nil {
		t.Fatalf("duplicate retry: %v", err)
	}
	if got := h.conv.sendCallCount(); got != 1 {
		t.Fatalf("provider calls after retries = %d, want 1", got)
	}
	after, err := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
	if err != nil || len(after.Turns) != 1 || len(after.Messages) != 1 {
		t.Fatalf("snapshot after retries: turns=%d messages=%d err=%v", len(after.Turns), len(after.Messages), err)
	}
}

func TestSendRetryAfterProviderAcceptedButBindENOSPC(t *testing.T) {
	h := newHarnessWithConversationAndStore(t, nil, func(st *store.Store) chatsvc.Store {
		return &enospcBindStore{Store: st}
	})
	ctx := context.Background()
	msg := ports.ChatUserMessage{Text: "inspect this", ClientMessageID: "enospc-bind", Origin: domain.MessageOriginHuman}

	if _, err := h.svc.Send(ctx, testSession, msg); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("first Send error = %v, want ENOSPC", err)
	}
	if _, err := h.svc.Send(ctx, testSession, msg); err != nil {
		t.Fatalf("retry after bind failure: %v", err)
	}
	if got := h.conv.sendCallCount(); got != 1 {
		t.Fatalf("provider calls after bind failure and retry = %d, want 1", got)
	}
	snapshot, err := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
	if err != nil || len(snapshot.Turns) != 1 || len(snapshot.Messages) != 1 {
		t.Fatalf("snapshot after bind failure: turns=%d messages=%d err=%v", len(snapshot.Turns), len(snapshot.Messages), err)
	}
}

func TestSendRetryAfterBindENOSPCAndDaemonRestart(t *testing.T) {
	ctx := context.Background()
	first := newFakeConversation()
	h := newHarnessWithConversationAndStore(t, &terminatingConversation{fakeConversation: first}, func(st *store.Store) chatsvc.Store {
		return &enospcBindStore{Store: st}
	})
	msg := ports.ChatUserMessage{Text: "inspect this", ClientMessageID: "enospc-restart", Origin: domain.MessageOriginHuman}
	if _, err := h.svc.Send(ctx, testSession, msg); !errors.Is(err, syscall.ENOSPC) || first.sendCallCount() != 1 {
		t.Fatalf("first Send: err=%v provider calls=%d, want ENOSPC after one acceptance", err, first.sendCallCount())
	}
	before, err := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
	if err != nil || len(before.Turns) != 1 || before.Turns[0].State != domain.TurnStateRunning || before.Turns[0].ProviderTurnID != "" {
		t.Fatalf("pre-restart turn = %+v, err=%v; want one unbound running turn", before.Turns, err)
	}
	originalTurnID := before.Turns[0].ID
	h.svc.StopAll(ctx)
	h.ctrl.Wait()

	acks := make(chan string, 4)
	second := &reconnectedReceiptConversation{receiptConversation: &receiptConversation{
		terminatingConversation: &terminatingConversation{fakeConversation: newFakeConversation()},
		ack:                     func(id string) error { acks <- id; return nil },
	}}
	second.turnSeq = 1
	var ids atomic.Int64
	svc := chatsvc.New(chatsvc.Options{
		Store: h.st, Sessions: h.st, Reader: fullSnapshotReader(h.st),
		Drivers: fakeRegistry{driver: fakeDriver{conv: second}},
		Log:     slog.New(slog.DiscardHandler),
		NewID:   func() string { return fmt.Sprintf("restart-%d", ids.Add(1)) },
		Now:     h.now,
	})
	t.Cleanup(func() { svc.StopAll(ctx) })
	if _, err := svc.Start(ctx, chatsvc.StartConfig{
		SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex,
		WorkspacePath: t.TempDir(), ProviderConversationID: "thread-1",
	}); err != nil {
		t.Fatalf("restart Chat service: %v", err)
	}
	if _, err := svc.Send(ctx, testSession, msg); err != nil {
		t.Fatalf("same-ID retry after restart: %v", err)
	}
	if second.sendCallCount() != 0 {
		t.Fatal("retry itself sent a second provider turn")
	}
	second.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1", ProviderEventID: "started"},
		ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1", ProviderEventID: "completed", TurnState: domain.TurnStateCompleted},
		ports.ChatEvent{Kind: ports.ChatEventControllerState, ControllerState: ports.ChatControllerReady, ProviderEventID: "barrier"},
	)
	for {
		select {
		case id := <-acks:
			if id == "barrier" {
				if got := first.sendCallCount() + second.sendCallCount(); got != 1 {
					t.Fatalf("provider accepted %d turns for one client ID across restart, want 1", got)
				}
				snapshot, err := h.st.LoadConversationSnapshot(ctx, h.ctrl.ConversationID())
				if err != nil {
					t.Fatalf("load post-reconnect snapshot: %v", err)
				}
				for _, turn := range snapshot.Turns {
					if turn.ID == originalTurnID {
						if !turn.State.Terminal() {
							t.Fatalf("original turn remains visibly %s after replayed provider completion", turn.State)
						}
						return
					}
				}
				t.Fatalf("original turn %s disappeared after reconnect", originalTurnID)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("reconnected provider events were not projected")
		}
	}
}
