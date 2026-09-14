package chat_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

type blockedLiveProjectionStore struct {
	chatsvc.Store
	once    sync.Once
	entered chan struct{}
	release chan struct{}
	failure error
}

func (s *blockedLiveProjectionStore) ProjectProviderEvent(
	ctx context.Context, conversationID string, session domain.SessionID,
	generation, providerEventID, method, payload string, now time.Time,
	project func(context.Context) error,
) (bool, error) {
	first := false
	if method == string(ports.ChatEventMessageDelta) {
		s.once.Do(func() {
			first = true
			close(s.entered)
			<-s.release
		})
	}
	if first && s.failure != nil {
		return false, s.failure
	}
	return s.Store.ProjectProviderEvent(ctx, conversationID, session, generation, providerEventID, method, payload, now, project)
}

func awaitLiveFrame(t *testing.T, sub *chatsvc.LiveSubscription, sequence int64) chatsvc.LiveFrame {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		frame := sub.Snapshot(0)
		if frame.Sequence >= sequence {
			return frame
		}
		select {
		case _, open := <-sub.Changed():
			if !open {
				t.Fatal("live stream ended before expected observations")
			}
		case <-timer.C:
			t.Fatalf("live sequence = %d, want at least %d", frame.Sequence, sequence)
		}
	}
}

func TestLiveTextContinuesWhileProjectionIsBlocked(t *testing.T) {
	for _, identified := range []bool{false, true} {
		t.Run(fmt.Sprintf("identified=%t", identified), func(t *testing.T) {
			blocked := &blockedLiveProjectionStore{entered: make(chan struct{}), release: make(chan struct{})}
			h := newHarnessWithConversationAndStore(t, nil, func(st *store.Store) chatsvc.Store {
				blocked.Store = st
				return blocked
			})
			// Release before the harness's cleanup waits for the controller to drain.
			var release sync.Once
			unblock := func() { release.Do(func() { close(blocked.release) }) }
			t.Cleanup(unblock)
			ctx := context.Background()
			sub, err := h.svc.SubscribeLive(ctx, testSession)
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			turn, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "go", ClientMessageID: "live-blocked"})
			if err != nil {
				t.Fatal(err)
			}
			h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: turn.ProviderTurnID})
			h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
				return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
			})
			baseline, err := h.svc.Snapshot(ctx, testSession)
			if err != nil {
				t.Fatal(err)
			}
			emitDelta := func(text string) {
				event := ports.ChatEvent{Kind: ports.ChatEventMessageDelta, ProviderTurnID: turn.ProviderTurnID, ProviderItemID: "reply", Delta: text}
				if identified {
					event.ProviderEventID, event.ProviderEventFresh = "native/"+text, true
				}
				h.conv.emit(event)
			}
			emitDelta("first")
			select {
			case <-blocked.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("projector did not reach the blocked write")
			}
			for _, text := range []string{" second", " third"} {
				emitDelta(text)
			}
			frame := awaitLiveFrame(t, sub, 4)
			if frame.Generation != baseline.LiveGeneration || len(frame.Events) != 3 || frame.Events[0].Delta+frame.Events[1].Delta+frame.Events[2].Delta != "first second third" {
				t.Fatalf("text waited for persistence or lost order: %+v", frame)
			}
			cancelCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
			defer cancel()
			if _, snapshotErr := h.svc.Snapshot(cancelCtx, testSession); !errors.Is(snapshotErr, context.DeadlineExceeded) {
				t.Fatalf("cancelled snapshot waited for blocked projection: %v", snapshotErr)
			}
			// Snapshot reads wait for the same transaction/cursor boundary, while live
			// intake above remains independent of that blocked checkpoint lock.
			snapshots := make(chan chatsvc.Snapshot, 1)
			go func() {
				snapshot, snapshotErr := h.svc.Snapshot(ctx, testSession)
				if snapshotErr != nil {
					t.Errorf("snapshot: %v", snapshotErr)
				}
				snapshots <- snapshot
			}()
			select {
			case <-snapshots:
				t.Fatal("snapshot escaped an in-flight projection checkpoint")
			case <-time.After(10 * time.Millisecond):
			}
			unblock()
			intermediate := <-snapshots
			if intermediate.LiveSequence < 2 {
				t.Fatalf("snapshot omitted processed write: %+v", intermediate)
			}
			h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: turn.ProviderTurnID, TurnState: domain.TurnStateCompleted})
			h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
				return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateCompleted
			})
			settled, err := h.svc.Snapshot(ctx, testSession)
			if err != nil {
				t.Fatal(err)
			}
			if settled.LiveSequence != 5 || len(settled.Messages) != 2 || settled.Messages[1].Text != "first second third" {
				t.Fatalf("durable catchup lost text/checkpoint: %+v", settled)
			}
			if remaining := sub.Snapshot(settled.LiveSequence); len(remaining.Events) != 0 || remaining.AfterSequence != 5 {
				t.Fatalf("durable refresh would replay saved text: %+v", remaining)
			}
		})
	}
}

func TestLiveSubscriptionRejectsStaleControllerGeneration(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.st.ClaimChatControllerGeneration(ctx, testSession, "replacement", h.now()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SubscribeLive(ctx, testSession); !errors.Is(err, chatsvc.ErrNoController) {
		t.Fatalf("subscribed to stale generation: %v", err)
	}
	snapshot, err := h.svc.Snapshot(ctx, testSession)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LiveGeneration != "" {
		t.Fatalf("stale controller stamped new-owner snapshot: %q", snapshot.LiveGeneration)
	}
}

func TestLiveProjectionFailureRequestsDurableResync(t *testing.T) {
	blocked := &blockedLiveProjectionStore{
		entered: make(chan struct{}), release: make(chan struct{}), failure: errors.New("storage failed"),
	}
	h := newHarnessWithConversationAndStore(t, nil, func(st *store.Store) chatsvc.Store {
		blocked.Store = st
		return blocked
	})
	var release sync.Once
	unblock := func() { release.Do(func() { close(blocked.release) }) }
	t.Cleanup(unblock)
	ctx := context.Background()
	sub, err := h.svc.SubscribeLive(ctx, testSession)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventMessageDelta, ProviderItemID: "failed-reply", Delta: "transient"})
	awaitLiveFrame(t, sub, 1)
	select {
	case <-blocked.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("projector did not reach failure gate")
	}
	unblock()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for sub.Snapshot(0).ResetSequence != 1 {
		select {
		case <-sub.Changed():
		case <-timer.C:
			t.Fatal("failed projection did not request durable resync")
		}
	}
	snapshot, err := h.svc.Snapshot(ctx, testSession)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LiveSequence != 1 || len(snapshot.Messages) != 0 {
		t.Fatalf("failed preview was not discarded at processed checkpoint: %+v", snapshot)
	}
}

func TestLivePermanentFailureDiscardsQueuedPreviews(t *testing.T) {
	conv := &receiptConversation{
		terminatingConversation: &terminatingConversation{fakeConversation: newFakeConversation()},
		ack:                     func(string) error { t.Error("acknowledged failed output"); return nil },
	}
	blocked := &blockedLiveProjectionStore{entered: make(chan struct{}), release: make(chan struct{})}
	h := newHarnessWithConversationAndStore(t, conv, func(st *store.Store) chatsvc.Store {
		flaky := &flakyReceiptStore{Store: st}
		flaky.failures.Store(-1)
		blocked.Store = flaky
		return blocked
	})
	var release sync.Once
	unblock := func() { release.Do(func() { close(blocked.release) }) }
	t.Cleanup(unblock)
	sub, err := h.svc.SubscribeLive(context.Background(), testSession)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	for _, text := range []string{"first", "queued"} {
		conv.emit(ports.ChatEvent{Kind: ports.ChatEventMessageDelta, ProviderItemID: "reply", ProviderEventID: text, ProviderEventFresh: true, Delta: text})
	}
	frame := awaitLiveFrame(t, sub, 2)
	if len(frame.Events) != 2 {
		t.Fatalf("missing preview: %+v", frame)
	}
	unblock()
	done := make(chan struct{})
	go func() { h.ctrl.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("failed writer did not stop intake")
	}
	frame = sub.Snapshot(0)
	snapshot, err := h.svc.Snapshot(context.Background(), testSession)
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.Events) != 0 || frame.ResetSequence != 2 || (snapshot.LiveGeneration != "" && snapshot.LiveSequence != 2) || len(snapshot.Messages) != 0 {
		t.Fatalf("failed queued text remained visible: frame=%+v snapshot=%+v", frame, snapshot)
	}
}
