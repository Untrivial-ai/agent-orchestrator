package chat_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	sqlite "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

type receiptConversation struct {
	*terminatingConversation
	ack     func(string) error
	eventMu sync.Mutex
}

func (c *receiptConversation) Close() error {
	c.eventMu.Lock()
	defer c.eventMu.Unlock()
	return c.fakeConversation.Close()
}

func (c *receiptConversation) emit(events ...ports.ChatEvent) {
	c.eventMu.Lock()
	defer c.eventMu.Unlock()
	c.fakeConversation.emit(events...)
}

func (c *receiptConversation) AcknowledgeProviderEvent(_ context.Context, id string) error {
	return c.ack(id)
}

type flakyReceiptStore struct {
	chatsvc.Store
	failures atomic.Int32 // -1 fails permanently; positive counts transient failures.
}

func (s *flakyReceiptStore) ProjectProviderEvent(
	ctx context.Context, conversationID string, session domain.SessionID,
	generation, providerEventID, method, payloadJSON string, now time.Time,
	project func(context.Context) error,
) (bool, error) {
	return s.Store.ProjectProviderEvent(ctx, conversationID, session, generation,
		providerEventID, method, payloadJSON, now, func(txCtx context.Context) error {
			if err := project(txCtx); err != nil {
				return err
			}
			if method == string(ports.ChatEventMessageDelta) && s.failures.Load() != 0 {
				if s.failures.Load() > 0 {
					s.failures.Add(-1)
				}
				return errors.New("injected event transaction rollback")
			}
			return nil
		})
}

func TestPersistentProjectionNeverAcknowledgesPastFailedOutput(t *testing.T) {
	for _, failures := range []int32{1, -1} {
		name := "transient failure retries in order"
		if failures < 0 {
			name = "permanent failure detaches without acknowledging completion"
		}
		t.Run(name, func(t *testing.T) {
			acknowledged := make(chan string, 4)
			conv := &receiptConversation{
				terminatingConversation: &terminatingConversation{fakeConversation: newFakeConversation()},
				ack:                     func(id string) error { acknowledged <- id; return nil },
			}
			h := newHarnessWithConversationAndStore(t, conv, func(st *sqlite.Store) chatsvc.Store {
				flaky := &flakyReceiptStore{Store: st}
				flaky.failures.Store(failures)
				return flaky
			})
			turn, err := h.ctrl.Send(context.Background(), ports.ChatUserMessage{Text: "first"})
			if err != nil {
				t.Fatal(err)
			}
			conv.emit(
				ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: turn.ProviderTurnID},
				ports.ChatEvent{Kind: ports.ChatEventMessageDelta, ProviderTurnID: turn.ProviderTurnID, ProviderItemID: "answer", ProviderEventID: "output", Delta: "retained answer"},
				ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: turn.ProviderTurnID, ProviderEventID: "completion", TurnState: domain.TurnStateCompleted},
			)
			if failures > 0 {
				for _, want := range []string{"output", "completion"} {
					select {
					case got := <-acknowledged:
						if got != want {
							t.Fatalf("ack %q, want %q", got, want)
						}
					case <-time.After(3 * time.Second):
						t.Fatalf("missing ack %s", want)
					}
				}
				snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
					return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateCompleted
				})
				if len(snapshot.Messages) != 2 || snapshot.Messages[1].Text != "retained answer" {
					t.Fatalf("acknowledged without exactly-once output: %+v", snapshot.Messages)
				}
				return
			}
			done := make(chan struct{})
			go func() { h.ctrl.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("failed projection did not detach")
			}
			select {
			case id := <-acknowledged:
				t.Fatalf("acknowledged past failed output: %s", id)
			default:
			}
			if conv.terminated.Load() {
				t.Fatal("projection failure destroyed replay owner")
			}
			snapshot, err := h.st.LoadConversationSnapshot(context.Background(), h.ctrl.ConversationID())
			if err != nil || len(snapshot.Turns) != 1 || snapshot.Turns[0].State != domain.TurnStateRunning {
				t.Fatalf("failed projection falsely settled turn: %+v, %v", snapshot.Turns, err)
			}
			if err := h.svc.Stop(context.Background(), testSession); err != nil || h.hostStops.Load() != 1 {
				t.Fatalf("explicit stop after failed attachment did not destroy host: calls=%d err=%v", h.hostStops.Load(), err)
			}
		})
	}
}

type flakyBatchStore struct {
	*sqlite.Store
	failures atomic.Int32
	calls    atomic.Int32
}

func (s *flakyBatchStore) ProjectCoalescedProviderEvents(
	ctx context.Context,
	conversationID string,
	session domain.SessionID,
	generation string,
	providerEventIDs, methods, payloads []string,
	now time.Time,
	projectAll func(context.Context) error,
	projectOne func(context.Context, int) error,
) (bool, error) {
	s.calls.Add(1)
	if s.failures.Load() > 0 {
		s.failures.Add(-1)
		return false, errors.New("injected batch rollback")
	}
	return s.Store.ProjectCoalescedProviderEvents(ctx, conversationID, session, generation, providerEventIDs, methods, payloads, now, projectAll, projectOne)
}

func TestProseBatchStaysReplayableUntilTheWriteCommits(t *testing.T) {
	acknowledged := make(chan string, 8)
	conv := &receiptConversation{
		terminatingConversation: &terminatingConversation{fakeConversation: newFakeConversation()},
		ack:                     func(id string) error { acknowledged <- id; return nil },
	}
	var batch *flakyBatchStore
	h := newHarnessWithConversationAndStore(t, conv, func(st *sqlite.Store) chatsvc.Store {
		batch = &flakyBatchStore{Store: st}
		batch.failures.Store(1)
		return batch
	})
	conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "pt-1"},
		ports.ChatEvent{
			Kind: ports.ChatEventActivityStarted, ProviderTurnID: "pt-1", ProviderItemID: "rs_1",
			ActivityKind: domain.ActivityKindReasoning, ActivityStatus: domain.ActivityStatusRunning,
		},
		ports.ChatEvent{Kind: ports.ChatEventReasoningDelta, ProviderItemID: "rs_1", ProviderEventID: "d1", Delta: "aa"},
		ports.ChatEvent{Kind: ports.ChatEventReasoningDelta, ProviderItemID: "rs_1", ProviderEventID: "d2", Delta: "bb"},
		ports.ChatEvent{Kind: ports.ChatEventReasoningDelta, ProviderItemID: "rs_1", ProviderEventID: "d3", Delta: "cc"},
	)
	deadline := time.Now().Add(3 * time.Second)
	got := map[string]bool{}
	for len(got) < 3 && time.Now().Before(deadline) {
		select {
		case id := <-acknowledged:
			got[id] = true
		case <-time.After(50 * time.Millisecond):
		}
	}
	if !got["d1"] || !got["d2"] || !got["d3"] {
		t.Fatalf("acks = %#v, want d1 d2 d3 after the committed batch", got)
	}
	if batch.calls.Load() < 2 {
		t.Fatalf("batch attempts = %d, want the failed attempt retried", batch.calls.Load())
	}
	snapshot, err := h.st.LoadConversationSnapshot(context.Background(), h.ctrl.ConversationID())
	if err != nil {
		t.Fatal(err)
	}
	var text string
	var revision int64
	for _, activity := range snapshot.Activities {
		if activity.ProviderItemID == "rs_1" {
			text = activity.StreamedText
			revision = activity.Revision
		}
	}
	if text != "aabbcc" || revision != 1 {
		t.Fatalf("streamed text = %q revision %d, want aabbcc written once", text, revision)
	}
}

func TestPersistentCompletionIsAcknowledgedBeforeQueuedTurnDispatch(t *testing.T) {
	for _, ackFails := range []bool{false, true} {
		name := "acknowledged"
		if ackFails {
			name = "ack failure retains queue"
		}
		t.Run(name, func(t *testing.T) {
			var acknowledged, dispatchedBeforeAck atomic.Bool
			conv := &receiptConversation{
				terminatingConversation: &terminatingConversation{fakeConversation: newFakeConversation()},
				ack: func(id string) error {
					if id == "completion" {
						if ackFails {
							return errors.New("connection lost before ack")
						}
						acknowledged.Store(true)
					}
					return nil
				},
			}
			conv.onSend = func(id string) {
				if id == "provider-turn-2" && !acknowledged.Load() {
					dispatchedBeforeAck.Store(true)
				}
			}
			h := newHarnessWithConversation(t, conv)
			ctx := context.Background()
			turn, err := h.ctrl.Send(ctx, ports.ChatUserMessage{Text: "first"})
			if err != nil {
				t.Fatal(err)
			}
			conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: turn.ProviderTurnID})
			h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
				return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
			})
			queued, err := h.ctrl.Send(ctx, ports.ChatUserMessage{Text: "second"})
			if err != nil || queued.State != domain.TurnStateQueued {
				t.Fatalf("queue: %+v, %v", queued, err)
			}
			conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: turn.ProviderTurnID, ProviderEventID: "completion", TurnState: domain.TurnStateCompleted})
			if ackFails {
				done := make(chan struct{})
				go func() { h.ctrl.Wait(); close(done) }()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("ack failure did not detach")
				}
				if len(conv.sentTexts()) != 1 || conv.terminated.Load() {
					t.Fatal("ack failure dispatched queued work or destroyed provider")
				}
			} else {
				h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
					return turnStateByText(t, s)["second"] == domain.TurnStateRunning
				})
			}
			if dispatchedBeforeAck.Load() {
				t.Fatal("queued turn dispatched before previous completion was acknowledged")
			}
		})
	}
}

func TestProviderPreservationReportRequiresLiveController(t *testing.T) {
	provider := &terminatingConversation{fakeConversation: newFakeConversation()}
	h := newHarnessWithConversation(t, provider)
	if !h.svc.PreservesProviderOnRestart(testSession) || h.svc.PreservesProviderOnRestart("missing") {
		t.Fatal("preservation report does not match established ownership")
	}
	h.svc.StopAll(context.Background())
	if h.hostStops.Load() != 0 {
		t.Fatal("daemon shutdown destroyed persistent ownership")
	}
	if h.svc.PreservesProviderOnRestart(testSession) {
		t.Fatal("missing controller advertised live ownership")
	}
	if err := h.svc.Stop(context.Background(), testSession); err != nil || h.hostStops.Load() != 1 {
		t.Fatalf("explicit stop with no registered controller: calls=%d err=%v", h.hostStops.Load(), err)
	}
	ordinary := newHarness(t)
	if ordinary.svc.PreservesProviderOnRestart(testSession) {
		t.Fatal("ordinary conversation advertised persistent ownership")
	}
}
