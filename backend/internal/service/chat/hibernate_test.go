package chat_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

type hibernationConversation struct {
	*fakeConversation
	calls      atomic.Int32
	started    chan struct{}
	release    <-chan struct{}
	onSnapshot func()
	keepOpen   bool
}

func (c *hibernationConversation) Hibernate() error {
	c.calls.Add(1)
	if c.started != nil {
		close(c.started)
	}
	if c.release != nil {
		<-c.release
	}
	if c.keepOpen {
		return nil
	}
	return c.Close()
}

func (c *hibernationConversation) SetTitle(context.Context, string) error { return nil }

func (c *hibernationConversation) Compact(context.Context) (ports.ChatCompactionResult, error) {
	return ports.ChatCompactionResult{}, nil
}

func settledHibernationHarness(t *testing.T, state domain.TurnState) (*harness, *hibernationConversation) {
	t.Helper()
	conv := &hibernationConversation{fakeConversation: newFakeConversation()}
	st := openStore(t)
	h := &harness{st: st, conv: conv.fakeConversation, activity: &recordingActivity{}, clock: time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)}
	var nextID atomic.Int32
	reader := fullSnapshotReader(st)
	h.svc = chatsvc.New(chatsvc.Options{
		Store: st, Reader: chatsvc.SnapshotReaderFunc(func(ctx context.Context, id string) (chatsvc.ConversationRows, error) {
			rows, err := reader.LoadConversationSnapshot(ctx, id)
			if err == nil && conv.onSnapshot != nil {
				conv.onSnapshot()
			}
			return rows, err
		}), Sessions: st,
		Drivers:  fakeRegistry{driver: fakeDriver{conv: conv}},
		Activity: h.activity, Log: slog.New(slog.DiscardHandler), Now: h.now,
		NewID: func() string { return fmt.Sprintf("hibernate-%d", nextID.Add(1)) },
	})
	ctx := context.Background()
	ctrl, err := h.svc.Start(ctx, chatsvc.StartConfig{SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex, WorkspacePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	h.ctrl = ctrl
	t.Cleanup(func() { _ = h.svc.Stop(context.Background(), testSession) })
	if state != "" {
		if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "do work", ClientMessageID: "hibernate-1"}); err != nil {
			t.Fatal(err)
		}
		conv.emit(
			ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"},
			ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1", TurnState: state},
		)
		h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
			return len(s.Turns) == 1 && s.Turns[0].State == state
		})
	}
	rec, found, err := h.st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("get session = %v, %v", found, err)
	}
	rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: h.now()}
	rec.Metadata.ProviderConversationID = conv.ProviderConversationID()
	if err := h.st.UpdateSession(ctx, rec); err != nil {
		t.Fatal(err)
	}
	return h, conv
}

func TestHibernateChatKeepsCompletedIdleSessionResumable(t *testing.T) {
	h, conv := settledHibernationHarness(t, domain.TurnStateCompleted)
	ctx := context.Background()
	hibernated, err := h.svc.HibernateChat(ctx, testSession)
	if err != nil || !hibernated {
		rec, _, _ := h.st.GetSession(ctx, testSession)
		t.Fatalf("HibernateChat = %v, %v; controller=%q activity=%q nativeID=%q", hibernated, err,
			h.ctrl.State(), rec.Activity.State, rec.Metadata.ProviderConversationID)
	}
	rec, found, err := h.st.GetSession(ctx, testSession)
	if err != nil || !found || rec.HibernatedAt == nil || rec.Activity.State != domain.ActivityIdle || rec.IsTerminated {
		t.Fatalf("hibernated record = %+v, %v, %v", rec, found, err)
	}
	if conv.calls.Load() != 1 {
		t.Fatalf("provider hibernations = %d, want 1", conv.calls.Load())
	}
	snapshot, err := h.svc.Snapshot(ctx, testSession)
	if err != nil || snapshot.Controller != ports.ChatControllerHibernated {
		t.Fatalf("cold snapshot controller = %q, %v", snapshot.Controller, err)
	}
}

func TestChatViewKeepsCompletedSessionWarmUntilReleasedOrExpired(t *testing.T) {
	t.Run("released", func(t *testing.T) {
		h, conv := settledHibernationHarness(t, domain.TurnStateCompleted)
		ctx := context.Background()
		h.svc.SetHibernateCallback(func(ctx context.Context, id domain.SessionID) error {
			_, err := h.svc.HibernateChat(ctx, id)
			return err
		})
		if err := h.svc.SetChatView(ctx, testSession, "viewer-1", true); err != nil {
			t.Fatal(err)
		}
		if err := h.svc.SetChatView(ctx, testSession, "viewer-2", true); err != nil {
			t.Fatal(err)
		}
		if hibernated, err := h.svc.HibernateChat(ctx, testSession); err != nil || hibernated {
			t.Fatalf("hibernate viewed chat = %v, %v", hibernated, err)
		}
		if err := h.svc.SetChatView(ctx, testSession, "viewer-1", false); err != nil || conv.calls.Load() != 0 {
			t.Fatalf("release view = %v; provider hibernations = %d", err, conv.calls.Load())
		}
		if err := h.svc.SetChatView(ctx, testSession, "viewer-2", false); err != nil || conv.calls.Load() != 1 {
			t.Fatalf("release final view = %v; provider hibernations = %d", err, conv.calls.Load())
		}
		if err := h.svc.SetChatView(ctx, testSession, "viewer-1", false); err != nil {
			t.Fatalf("repeat release = %v", err)
		}
	})
	t.Run("expired", func(t *testing.T) {
		h, _ := settledHibernationHarness(t, domain.TurnStateCompleted)
		ctx := context.Background()
		if err := h.svc.SetChatView(ctx, testSession, "viewer-1", true); err != nil {
			t.Fatal(err)
		}
		h.advance(31 * time.Second)
		if hibernated, err := h.svc.HibernateChat(ctx, testSession); err != nil || !hibernated {
			t.Fatalf("hibernate after expired lease = %v, %v", hibernated, err)
		}
	})
}

func TestChatViewRetriesFailedWakeWithoutLosingLease(t *testing.T) {
	h, _ := settledHibernationHarness(t, domain.TurnStateCompleted)
	ctx := context.Background()
	if hibernated, err := h.svc.HibernateChat(ctx, testSession); err != nil || !hibernated {
		t.Fatalf("hibernate = %v, %v", hibernated, err)
	}
	wakeErr := errors.New("provider temporarily unavailable")
	var calls atomic.Int32
	h.svc.SetWakeCallback(func(context.Context, domain.SessionID) error {
		calls.Add(1)
		return wakeErr
	})
	for range 2 {
		if err := h.svc.SetChatView(ctx, testSession, "viewer-1", true); !errors.Is(err, wakeErr) {
			t.Fatalf("wake = %v, want %v", err, wakeErr)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("wake calls = %d, want 2", calls.Load())
	}
}

func TestOpeningViewWakesNativeConversation(t *testing.T) {
	h, old := settledHibernationHarness(t, domain.TurnStateCompleted)
	ctx := context.Background()
	if hibernated, err := h.svc.HibernateChat(ctx, testSession); err != nil || !hibernated {
		t.Fatalf("hibernate = %v, %v", hibernated, err)
	}
	resumed := newFakeConversation()
	var resumeConfig ports.ChatResumeConfig
	wakeService := chatsvc.New(chatsvc.Options{
		Store: h.st, Reader: fullSnapshotReader(h.st), Sessions: h.st,
		Drivers:  fakeRegistry{driver: fakeDriver{conv: resumed, resumeCfg: &resumeConfig}},
		Activity: h.activity, Log: slog.New(slog.DiscardHandler), Now: h.now,
		NewID: func() string { return "view-wake" },
	})
	t.Cleanup(func() { _ = wakeService.Stop(context.Background(), testSession) })
	wakeService.SetWakeCallback(func(ctx context.Context, id domain.SessionID) error {
		rec, found, err := h.st.GetSession(ctx, id)
		if err != nil || !found || rec.HibernatedAt == nil {
			return fmt.Errorf("read hibernated session: found=%v marker=%v err=%v", found, rec.HibernatedAt, err)
		}
		cleared, err := h.st.SetSessionHibernated(ctx, id, rec.Revision, nil)
		if err != nil || !cleared {
			return fmt.Errorf("clear hibernation: applied=%v err=%v", cleared, err)
		}
		_, err = wakeService.Start(ctx, chatsvc.StartConfig{
			SessionID: id, ProjectID: testProject, Harness: domain.HarnessCodex,
			WorkspacePath: t.TempDir(), ProviderConversationID: rec.Metadata.ProviderConversationID,
		})
		return err
	})
	if err := wakeService.SetChatView(ctx, testSession, "viewer-1", true); err != nil {
		t.Fatal(err)
	}
	if resumeConfig.ProviderConversationID != old.ProviderConversationID() || !wakeService.HasLiveChatController(testSession) {
		t.Fatalf("native wake = %q, live=%v", resumeConfig.ProviderConversationID, wakeService.HasLiveChatController(testSession))
	}
}

func TestOpeningViewWaitsForHibernationThenWakes(t *testing.T) {
	h, conv := settledHibernationHarness(t, domain.TurnStateCompleted)
	ctx := context.Background()
	conv.started = make(chan struct{})
	release := make(chan struct{})
	conv.release = release
	hibernated := make(chan error, 1)
	go func() {
		_, err := h.svc.HibernateChat(ctx, testSession)
		hibernated <- err
	}()
	<-conv.started
	wakeCalled := make(chan struct{}, 1)
	h.svc.SetWakeCallback(func(ctx context.Context, id domain.SessionID) error {
		rec, found, err := h.st.GetSession(ctx, id)
		if err != nil || !found || rec.HibernatedAt == nil {
			return fmt.Errorf("wake saw unfinished hibernation: found=%v marker=%v err=%v", found, rec.HibernatedAt, err)
		}
		wakeCalled <- struct{}{}
		return errors.New("wake reached native provider")
	})
	viewResult := make(chan error, 1)
	go func() { viewResult <- h.svc.SetChatView(ctx, testSession, "viewer-1", true) }()
	select {
	case err := <-viewResult:
		t.Fatalf("view opened before hibernation finished: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-hibernated; err != nil {
		t.Fatal(err)
	}
	if err := <-viewResult; err == nil {
		t.Fatal("opening view did not invoke native wake")
	}
	select {
	case <-wakeCalled:
	default:
		t.Fatal("native wake did not observe durable hibernation marker")
	}
}

func TestHibernatedCatalogReadsDoNotWakeProvider(t *testing.T) {
	h, _ := settledHibernationHarness(t, domain.TurnStateCompleted)
	ctx := context.Background()
	if hibernated, err := h.svc.HibernateChat(ctx, testSession); err != nil || !hibernated {
		t.Fatalf("HibernateChat = %v, %v", hibernated, err)
	}
	var wakeCalls atomic.Int32
	h.svc.SetWakeCallback(func(context.Context, domain.SessionID) error {
		wakeCalls.Add(1)
		return nil
	})
	if _, _, err := h.svc.Models(ctx, testSession); !errors.Is(err, chatsvc.ErrNoController) {
		t.Fatalf("Models error = %v, want no controller", err)
	}
	if _, err := h.svc.ConfigOptions(ctx, testSession); !errors.Is(err, chatsvc.ErrNoController) {
		t.Fatalf("ConfigOptions error = %v, want no controller", err)
	}
	rec, found, err := h.st.GetSession(ctx, testSession)
	if err != nil || !found || rec.HibernatedAt == nil || wakeCalls.Load() != 0 {
		t.Fatalf("passive reads woke provider: session=%+v found=%v readsErr=%v wakeCalls=%d", rec, found, err, wakeCalls.Load())
	}
}

func TestRelayChatTurnWithIDWakesHibernatedSession(t *testing.T) {
	h, old := settledHibernationHarness(t, domain.TurnStateCompleted)
	ctx := context.Background()
	if hibernated, err := h.svc.HibernateChat(ctx, testSession); err != nil || !hibernated {
		t.Fatalf("HibernateChat = %v, %v", hibernated, err)
	}

	resumed := newFakeConversation()
	resumed.turnSeq = 1 // Native resume must continue the original turn sequence.
	var resumeConfig ports.ChatResumeConfig
	var nextID atomic.Int32
	wakeService := chatsvc.New(chatsvc.Options{
		Store: h.st, Reader: fullSnapshotReader(h.st), Sessions: h.st,
		Drivers:  fakeRegistry{driver: fakeDriver{conv: resumed, resumeCfg: &resumeConfig}},
		Activity: h.activity, Log: slog.New(slog.DiscardHandler), Now: h.now,
		NewID: func() string { return fmt.Sprintf("relay-wake-%d", nextID.Add(1)) },
	})
	t.Cleanup(func() { _ = wakeService.Stop(context.Background(), testSession) })
	wakeService.SetWakeCallback(func(ctx context.Context, id domain.SessionID) error {
		rec, found, err := h.st.GetSession(ctx, id)
		if err != nil || !found || rec.HibernatedAt == nil {
			return fmt.Errorf("read hibernated session: found=%v, marker=%v, err=%w", found, rec.HibernatedAt, err)
		}
		cleared, err := h.st.SetSessionHibernated(ctx, id, rec.Revision, nil)
		if err != nil || !cleared {
			return fmt.Errorf("clear hibernation: applied=%v, err=%w", cleared, err)
		}
		_, err = wakeService.Start(ctx, chatsvc.StartConfig{
			SessionID: id, ProjectID: testProject, Harness: domain.HarnessCodex,
			WorkspacePath: t.TempDir(), ProviderConversationID: rec.Metadata.ProviderConversationID,
		})
		return err
	})

	turnID, err := wakeService.RelayChatTurnWithID(ctx, testSession, "CI failed; fix it", "ci-nudge-1")
	if err != nil || turnID == "" {
		t.Fatalf("RelayChatTurnWithID = %q, %v", turnID, err)
	}
	if resumeConfig.ProviderConversationID != old.ProviderConversationID() {
		t.Fatalf("native resume id = %q, want %q", resumeConfig.ProviderConversationID, old.ProviderConversationID())
	}
	messages := resumed.sentMessages()
	if len(messages) != 1 || messages[0].Text != "CI failed; fix it" ||
		messages[0].ClientMessageID != "ci-nudge-1" || messages[0].Origin != domain.MessageOriginAutomation {
		t.Fatalf("resumed provider messages = %+v", messages)
	}
	rec, found, err := h.st.GetSession(ctx, testSession)
	if err != nil || !found || rec.HibernatedAt != nil {
		t.Fatalf("session after relay wake: found=%v, marker=%v, err=%v", found, rec.HibernatedAt, err)
	}
}

func TestSendAfterTimedOutHibernationWaitsForProviderStop(t *testing.T) {
	h, conv := settledHibernationHarness(t, domain.TurnStateCompleted)
	conv.keepOpen = true
	if hibernated, err := h.svc.HibernateChat(context.Background(), testSession); hibernated || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed-out hibernation = %v, %v", hibernated, err)
	}
	wakeReached := errors.New("wake reached")
	h.svc.SetWakeCallback(func(context.Context, domain.SessionID) error { return wakeReached })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sent := make(chan error, 1)
	go func() {
		_, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "wake", ClientMessageID: "wake-after-timeout"})
		sent <- err
	}()
	select {
	case err := <-sent:
		t.Fatalf("send reached a fenced provider: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if err := conv.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-sent; !errors.Is(err, wakeReached) {
		t.Fatalf("send after provider stopped = %v, want wake callback", err)
	}
}

func TestHibernateChatWaitsForAcceptedRenameProjection(t *testing.T) {
	h, conv := settledHibernationHarness(t, domain.TurnStateCompleted)
	ctx := context.Background()
	const title = "Renamed session"
	if _, err := h.svc.SetTitle(ctx, testSession, title); err != nil {
		t.Fatal(err)
	}
	if hibernated, err := h.svc.HibernateChat(ctx, testSession); err != nil || hibernated || conv.calls.Load() != 0 {
		t.Fatalf("hibernate before rename projection = %v, %v; provider stops = %d", hibernated, err, conv.calls.Load())
	}

	// A notification for a different title does not confirm this request.
	conv.emit(ports.ChatEvent{Kind: ports.ChatEventThreadRenamed, Title: "Other title"})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return s.Conversation.ProviderTitle == "Other title" })
	if hibernated, err := h.svc.HibernateChat(ctx, testSession); err != nil || hibernated || conv.calls.Load() != 0 {
		t.Fatalf("hibernate after unrelated rename = %v, %v; provider stops = %d", hibernated, err, conv.calls.Load())
	}

	conv.emit(ports.ChatEvent{Kind: ports.ChatEventThreadRenamed, Title: title})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return s.Conversation.ProviderTitle == title })
	if hibernated, err := h.svc.HibernateChat(ctx, testSession); err != nil || !hibernated || conv.calls.Load() != 1 {
		t.Fatalf("hibernate after matching rename = %v, %v; provider stops = %d", hibernated, err, conv.calls.Load())
	}
}

func TestHibernateChatRechecksActivityBeforeStoppingProvider(t *testing.T) {
	h, conv := settledHibernationHarness(t, domain.TurnStateCompleted)
	conv.onSnapshot = func() {
		rec, found, err := h.st.GetSession(context.Background(), testSession)
		if err != nil || !found {
			t.Fatalf("get concurrent activity = %v, %v", found, err)
		}
		rec.Activity.State = domain.ActivityBlocked
		if err := h.st.UpdateSession(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
	}
	hibernated, err := h.svc.HibernateChat(context.Background(), testSession)
	if err != nil || hibernated || conv.calls.Load() != 0 {
		t.Fatalf("HibernateChat after activity change = %v, %v; provider calls = %d", hibernated, err, conv.calls.Load())
	}
}

func TestHibernateChatWaitsForAcceptedCompaction(t *testing.T) {
	h, conv := settledHibernationHarness(t, domain.TurnStateCompleted)
	caps := conv.Capabilities()
	caps[ports.ChatCapabilityCompaction] = true
	conv.setCapabilities(caps)
	ctx := context.Background()
	if _, err := h.svc.Compact(ctx, testSession); err != nil {
		t.Fatal(err)
	}
	if hibernated, err := h.svc.HibernateChat(ctx, testSession); err != nil || hibernated || conv.calls.Load() != 0 {
		t.Fatalf("hibernate during accepted compaction = %v, %v; provider stops = %d", hibernated, err, conv.calls.Load())
	}
	turn, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "after compaction", ClientMessageID: "after-compaction"})
	if err != nil || turn.State != domain.TurnStateQueued {
		t.Fatalf("send during accepted compaction = %+v, %v, want queued", turn, err)
	}
	if got := conv.sentTexts(); len(got) != 1 {
		t.Fatalf("provider sends during accepted compaction = %v", got)
	}
	conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "compact-turn"},
		ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "compact-turn", TurnState: domain.TurnStateCompleted},
	)
	h.awaitSnapshot(t, func(store.ConversationSnapshot) bool { return len(conv.sentTexts()) == 2 })
	if got := conv.sentTexts(); got[1] != "after compaction" {
		t.Fatalf("queued message after compaction = %v", got)
	}
}

func TestHibernateChatFinalGateRejectsUnfinishedWork(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state domain.TurnState
		add   func(*testing.T, *harness, *hibernationConversation)
	}{
		{name: "no turn"},
		{name: "failed", state: domain.TurnStateFailed},
		{name: "interrupted", state: domain.TurnStateInterrupted},
		{name: "queued", state: domain.TurnStateCompleted, add: func(t *testing.T, h *harness, _ *hibernationConversation) {
			t.Helper()
			created, err := h.st.AppendUserMessage(context.Background(), h.ctrl.ConversationID(), testSession, h.ctrl.Generation(),
				domain.ConversationMessage{ID: "queued-message", Text: "more work", Origin: domain.MessageOriginDaemon}, "queued-turn", h.now())
			if err != nil || !created {
				t.Fatalf("append queued turn = %v, %v", created, err)
			}
		}},
		{name: "running", state: domain.TurnStateCompleted, add: func(t *testing.T, h *harness, _ *hibernationConversation) {
			t.Helper()
			if err := h.st.AdoptProviderTurn(context.Background(), h.ctrl.ConversationID(), testSession, h.ctrl.Generation(),
				"running-turn", "provider-running", h.now()); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "pending approval", state: domain.TurnStateCompleted, add: func(t *testing.T, h *harness, c *hibernationConversation) {
			t.Helper()
			c.emit(ports.ChatEvent{Kind: ports.ChatEventApprovalRequested, ProviderTurnID: "provider-turn-1",
				ProviderItemID: "approval", RequestID: "approval", Summary: "Approve command",
				Decisions: []ports.ChatDecisionOption{{ID: "allow", Label: "Allow", Kind: ports.ChatDecisionAllowOnce}}})
			h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
				return len(s.Activities) != 0 && s.Activities[len(s.Activities)-1].Status == domain.ActivityStatusPending
			})
		}},
		{name: "pending input", state: domain.TurnStateCompleted, add: func(t *testing.T, h *harness, c *hibernationConversation) {
			t.Helper()
			c.emit(ports.ChatEvent{Kind: ports.ChatEventInputRequested, ProviderTurnID: "provider-turn-1",
				ProviderItemID: "input", RequestID: "input", Input: &ports.ChatInputRequest{Message: "Choose a value"}})
			h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
				return len(s.Activities) != 0 && s.Activities[len(s.Activities)-1].Status == domain.ActivityStatusPending
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, conv := settledHibernationHarness(t, tc.state)
			if tc.add != nil {
				tc.add(t, h, conv)
			}
			hibernated, err := h.svc.HibernateChat(context.Background(), testSession)
			if err != nil || hibernated || conv.calls.Load() != 0 {
				t.Fatalf("HibernateChat = %v, %v; provider calls = %d", hibernated, err, conv.calls.Load())
			}
			rec, found, err := h.st.GetSession(context.Background(), testSession)
			if err != nil || !found || rec.HibernatedAt != nil {
				t.Fatalf("session marker after rejected hibernation = %+v, %v, %v", rec.HibernatedAt, found, err)
			}
		})
	}
}

func TestSendWaitsForHibernationThenWakesNativeConversation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st := openStore(t)
	started := make(chan struct{})
	release := make(chan struct{})
	first := &hibernationConversation{fakeConversation: newFakeConversation(), started: started, release: release}
	resumed := newFakeConversation()
	resumed.turnSeq = 1 // Native resume continues the same provider turn-id sequence.
	h := &harness{st: st, activity: &recordingActivity{}, clock: time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)}
	var nextID atomic.Int32
	svc := chatsvc.New(chatsvc.Options{
		Store: st, Reader: fullSnapshotReader(st), Sessions: st,
		Drivers:  fakeRegistry{driver: &sequenceDriver{conversations: []ports.ChatConversation{first, resumed}}},
		Activity: h.activity, Log: slog.New(slog.DiscardHandler), Now: h.now,
		NewID: func() string { return fmt.Sprintf("hibernate-race-%d", nextID.Add(1)) },
	})
	h.svc = svc
	start := chatsvc.StartConfig{SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex, WorkspacePath: t.TempDir()}
	ctrl, err := svc.Start(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	h.ctrl = ctrl
	t.Cleanup(func() { _ = svc.Stop(context.Background(), testSession) })
	if _, err := svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "first", ClientMessageID: "first"}); err != nil {
		t.Fatal(err)
	}
	first.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"},
		ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1", TurnState: domain.TurnStateCompleted},
	)
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateCompleted
	})
	h.advance(6 * time.Minute)
	rec, found, err := st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("get session = %v, %v", found, err)
	}
	rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: h.now().Add(-6 * time.Minute)}
	rec.Metadata.ProviderConversationID = first.ProviderConversationID()
	if err := st.UpdateSession(ctx, rec); err != nil {
		t.Fatal(err)
	}

	svc.SetWakeCallback(func(ctx context.Context, id domain.SessionID) error {
		current, found, err := st.GetSession(ctx, id)
		if err != nil {
			return fmt.Errorf("read cold session: %w", err)
		}
		if !found {
			return fmt.Errorf("cold session %s not found", id)
		}
		if current.HibernatedAt == nil {
			return fmt.Errorf("wake called without durable hibernation marker")
		}
		cleared, err := st.SetSessionHibernated(ctx, id, current.Revision, nil)
		if err != nil {
			return fmt.Errorf("clear hibernation: %w", err)
		}
		if !cleared {
			return fmt.Errorf("clear hibernation for %s was not applied", id)
		}
		start.ProviderConversationID = current.Metadata.ProviderConversationID
		_, err = svc.Start(ctx, start)
		return err
	})
	result := make(chan error, 1)
	go func() {
		_, err := svc.HibernateChat(ctx, testSession)
		result <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	sent := make(chan error, 1)
	go func() {
		_, err := svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "after sleep", ClientMessageID: "after-sleep"})
		sent <- err
	}()
	select {
	case err := <-sent:
		t.Fatalf("send escaped hibernation fence: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatalf("hibernate = %v", err)
	}
	if err := <-sent; err != nil {
		t.Fatalf("send after wake = %v", err)
	}
	if got := resumed.sentTexts(); len(got) != 1 || got[0] != "after sleep" {
		t.Fatalf("resumed provider turns = %v", got)
	}
	rec, found, err = st.GetSession(ctx, testSession)
	if err != nil || !found || rec.HibernatedAt != nil {
		t.Fatalf("session after wake = %+v, %v, %v", rec.HibernatedAt, found, err)
	}
}
