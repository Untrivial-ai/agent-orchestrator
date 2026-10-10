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
	calls             atomic.Int32
	hostStops         atomic.Int32
	sessionReads      atomic.Int32
	started           chan struct{}
	release           <-chan struct{}
	onEligibilityRead func()
	keepOpen          bool
	markerErr         error
	rejectMarker      bool
	shutdownErr       error
	hostShutdownErr   error
	backgroundRunning bool
	backgroundErr     error
	backgroundChecks  atomic.Int32
	onBackgroundCheck func(context.Context)
}

type hibernationSessionStore struct {
	*store.Store
	conversation *hibernationConversation
}

func (s *hibernationSessionStore) GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	s.conversation.sessionReads.Add(1)
	return s.Store.GetSession(ctx, id)
}

func (s *hibernationSessionStore) SetSessionHibernated(ctx context.Context, id domain.SessionID, revision int64, at *time.Time) (bool, error) {
	if at != nil && (s.conversation.markerErr != nil || s.conversation.rejectMarker) {
		return false, s.conversation.markerErr
	}
	return s.Store.SetSessionHibernated(ctx, id, revision, at)
}

func (s *hibernationSessionStore) LatestVisibleUserTurnSettled(ctx context.Context, conversationID string, sessionID domain.SessionID) (bool, error) {
	settled, err := s.Store.LatestVisibleUserTurnSettled(ctx, conversationID, sessionID)
	if err == nil && s.conversation.onEligibilityRead != nil {
		s.conversation.onEligibilityRead()
	}
	return settled, err
}

func (c *hibernationConversation) CanHibernate(ctx context.Context) (bool, error) {
	c.backgroundChecks.Add(1)
	if c.onBackgroundCheck != nil {
		c.onBackgroundCheck(ctx)
	}
	return !c.backgroundRunning, c.backgroundErr
}

func (c *hibernationConversation) Hibernate() error {
	c.calls.Add(1)
	if c.shutdownErr != nil {
		return c.shutdownErr
	}
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

func settledHibernationHarness(t *testing.T, state domain.TurnState, gate ...func() bool) (*harness, *hibernationConversation) {
	t.Helper()
	enabled := func() bool { return true }
	if len(gate) != 0 {
		enabled = gate[0]
	}
	conv := &hibernationConversation{fakeConversation: newFakeConversation()}
	st := openStore(t)
	h := &harness{st: st, conv: conv.fakeConversation, activity: &recordingActivity{}, clock: time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)}
	var nextID atomic.Int32
	h.svc = chatsvc.New(chatsvc.Options{
		Store: st, Reader: fullSnapshotReader(st), Sessions: &hibernationSessionStore{Store: st, conversation: conv},
		Drivers:  fakeRegistry{driver: fakeDriver{conv: conv}},
		Activity: h.activity, Log: slog.New(slog.DiscardHandler), Now: h.now,
		NewID:              func() string { return fmt.Sprintf("hibernate-%d", nextID.Add(1)) },
		HibernationEnabled: enabled,
		StopProviderHost: func(context.Context, domain.SessionID) error {
			conv.hostStops.Add(1)
			if conv.hostShutdownErr != nil {
				return conv.hostShutdownErr
			}
			return conv.Close()
		},
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
		// The durable receipt precedes the projector releasing its dispatch lock.
		if err := h.svc.DrainQueued(ctx, testSession); err != nil {
			t.Fatal(err)
		}
	}
	rec, found, err := h.st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("get session = %v, %v", found, err)
	}
	rec.Kind = domain.KindWorker
	rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: h.now()}
	rec.Metadata.ProviderConversationID = conv.ProviderConversationID()
	if err := h.st.UpdateSession(ctx, rec); err != nil {
		t.Fatal(err)
	}
	h.advance(30 * time.Second)
	return h, conv
}

func TestHibernationGateKeepsCompletedIdleProviderWarmUntilEnabled(t *testing.T) {
	var enabled atomic.Bool
	h, conv := settledHibernationHarness(t, domain.TurnStateCompleted, enabled.Load)
	var eligibilityReads atomic.Int32
	conv.onEligibilityRead = func() { eligibilityReads.Add(1) }
	ctx := context.Background()
	if stopped, err := h.svc.HibernateChat(ctx, testSession); err != nil || stopped || conv.calls.Load() != 0 || !h.svc.HasLiveChatController(testSession) {
		t.Fatalf("disabled hibernation: stopped=%v err=%v calls=%d live=%v", stopped, err, conv.calls.Load(), h.svc.HasLiveChatController(testSession))
	}
	if got := eligibilityReads.Load(); got != 0 {
		t.Fatalf("disabled hibernation read %d turn outcomes, want 0", got)
	}
	if got := conv.backgroundChecks.Load(); got != 0 {
		t.Fatalf("disabled hibernation checked provider background work %d times", got)
	}
	enabled.Store(true)
	if stopped, err := h.svc.HibernateChat(ctx, testSession); err != nil || !stopped || conv.calls.Load() != 1 {
		t.Fatalf("enabled hibernation: stopped=%v err=%v calls=%d", stopped, err, conv.calls.Load())
	}
	if got := eligibilityReads.Load(); got != 1 {
		t.Fatalf("enabled hibernation read %d turn outcomes, want 1", got)
	}
}

func TestHibernateChatKeepsOrchestratorAwake(t *testing.T) {
	h, conv := settledHibernationHarness(t, domain.TurnStateCompleted)
	ctx := context.Background()
	rec, found, err := h.st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("get orchestrator = %v, %v", found, err)
	}
	rec.Kind = domain.KindOrchestrator
	if err := h.st.UpdateSession(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if slept, err := h.svc.HibernateChat(ctx, testSession); err != nil || slept || conv.calls.Load() != 0 || !h.svc.HasLiveChatController(testSession) {
		t.Fatalf("orchestrator hibernation: slept=%v err=%v stops=%d live=%v", slept, err, conv.calls.Load(), h.svc.HasLiveChatController(testSession))
	}
	if got := conv.backgroundChecks.Load(); got != 0 {
		t.Fatalf("orchestrator background inventory calls = %d, want 0", got)
	}
	rec, _, err = h.st.GetSession(ctx, testSession)
	if err != nil || rec.HibernatedAt != nil {
		t.Fatalf("orchestrator shutdown intent: at=%v err=%v", rec.HibernatedAt, err)
	}
}

func TestHibernateChatKeepsBackgroundWorkAlive(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("inventory_error=%v", fail), func(t *testing.T) {
			h, conv := settledHibernationHarness(t, domain.TurnStateCompleted)
			conv.backgroundRunning = !fail
			if fail {
				conv.backgroundErr = errors.New("background inventory unavailable")
			}
			ctx := context.Background()
			stopped, err := h.svc.HibernateChat(ctx, testSession)
			if stopped || !errors.Is(err, conv.backgroundErr) || conv.calls.Load() != 0 || !h.svc.HasLiveChatController(testSession) {
				t.Fatalf("background work: stopped=%v err=%v calls=%d live=%v", stopped, err, conv.calls.Load(), h.svc.HasLiveChatController(testSession))
			}
			rec, _, err := h.st.GetSession(ctx, testSession)
			if err != nil || rec.HibernatedAt != nil {
				t.Fatalf("background work recorded shutdown intent: at=%v err=%v", rec.HibernatedAt, err)
			}
			conv.backgroundRunning, conv.backgroundErr = false, nil
			if stopped, err := h.svc.HibernateChat(ctx, testSession); err != nil || !stopped || conv.calls.Load() != 1 {
				t.Fatalf("background work ended: stopped=%v err=%v calls=%d", stopped, err, conv.calls.Load())
			}
		})
	}
}

func TestChatViewLeaveStartsGracePeriod(t *testing.T) {
	h, conv := settledHibernationHarness(t, domain.TurnStateCompleted)
	ctx := context.Background()
	if err := h.svc.SetChatView(ctx, testSession, "viewer", true); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.SetChatView(ctx, testSession, "viewer", false); err != nil {
		t.Fatal(err)
	}
	if slept, err := h.svc.HibernateChat(ctx, testSession); err != nil || slept || conv.calls.Load() != 0 {
		t.Fatalf("left Chat without grace: slept=%v err=%v stops=%d", slept, err, conv.calls.Load())
	}
	h.advance(29 * time.Second)
	if slept, err := h.svc.HibernateChat(ctx, testSession); err != nil || slept {
		t.Fatalf("slept before 30-second grace: slept=%v err=%v", slept, err)
	}
	h.advance(time.Second)
	if slept, err := h.svc.HibernateChat(ctx, testSession); err != nil || !slept {
		t.Fatalf("did not sleep after grace: slept=%v err=%v", slept, err)
	}
}

func TestKillDoesNotWaitForProviderSend(t *testing.T) {
	h, conv := settledHibernationHarness(t, domain.TurnStateCompleted)
	started, release := make(chan struct{}), make(chan struct{})
	conv.onSend = func(string) { close(started); <-release }
	sent := make(chan error, 1)
	go func() {
		_, err := h.svc.Send(context.Background(), testSession, ports.ChatUserMessage{Text: "stuck", ClientMessageID: "stuck-send"})
		sent <- err
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := h.svc.Stop(ctx, testSession)
	close(release)
	<-sent
	if err != nil {
		t.Fatalf("Kill waited for the provider call: %v", err)
	}
}

func TestHibernateMarkerFailureKeepsProviderAlive(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			h, conv := settledHibernationHarness(t, domain.TurnStateCompleted)
			conv.rejectMarker = conflict
			if !conflict {
				conv.markerErr = errors.New("database write failed")
			}
			_, _ = h.svc.HibernateChat(context.Background(), testSession)
			if conv.calls.Load() != 0 || !h.svc.HasLiveChatController(testSession) {
				t.Fatalf("provider stopped without a durable sleep marker: stops=%d live=%v", conv.calls.Load(), h.svc.HasLiveChatController(testSession))
			}
		})
	}
}

func TestHibernationGateRechecksAfterEligibilityRead(t *testing.T) {
	var enabled atomic.Bool
	enabled.Store(true)
	h, conv := settledHibernationHarness(t, domain.TurnStateCompleted, enabled.Load)
	conv.onEligibilityRead = func() { enabled.Store(false) }
	stopped, err := h.svc.HibernateChat(context.Background(), testSession)
	if err != nil || stopped || conv.calls.Load() != 0 || !h.svc.HasLiveChatController(testSession) {
		t.Fatalf("hibernation disabled during eligibility read: stopped=%v err=%v calls=%d live=%v", stopped, err, conv.calls.Load(), h.svc.HasLiveChatController(testSession))
	}
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
		if err := h.svc.SetChatView(ctx, testSession, "viewer-2", false); err != nil || conv.calls.Load() != 0 {
			t.Fatalf("release final view = %v; provider hibernations = %d", err, conv.calls.Load())
		}
		if err := h.svc.SetChatView(ctx, testSession, "viewer-1", false); err != nil {
			t.Fatalf("repeat release = %v", err)
		}
		h.advance(30 * time.Second)
		if slept, err := h.svc.HibernateChat(ctx, testSession); err != nil || !slept {
			t.Fatalf("hibernate after view-close grace = %v, %v", slept, err)
		}
	})
	t.Run("expired", func(t *testing.T) {
		h, _ := settledHibernationHarness(t, domain.TurnStateCompleted)
		ctx := context.Background()
		if err := h.svc.SetChatView(ctx, testSession, "viewer-1", true); err != nil {
			t.Fatal(err)
		}
		h.advance(61 * time.Second)
		if hibernated, err := h.svc.HibernateChat(ctx, testSession); err != nil || !hibernated {
			t.Fatalf("hibernate after expired lease = %v, %v", hibernated, err)
		}
	})
}

func TestChatViewRenewalDoesNotRetryFailedWake(t *testing.T) {
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
	if err := h.svc.SetChatView(ctx, testSession, "viewer-1", true); !errors.Is(err, wakeErr) {
		t.Fatalf("initial wake = %v, want %v", err, wakeErr)
	}
	if err := h.svc.SetChatView(ctx, testSession, "viewer-1", true); err != nil {
		t.Fatalf("view renewal = %v, want nil", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("wake calls after renewal = %d, want 1", calls.Load())
	}
	if err := h.svc.SetChatView(ctx, testSession, "viewer-1", false); err != nil {
		t.Fatalf("close view = %v", err)
	}
	if err := h.svc.SetChatView(ctx, testSession, "viewer-1", true); !errors.Is(err, wakeErr) {
		t.Fatalf("reopened view wake = %v, want %v", err, wakeErr)
	}
	if calls.Load() != 2 {
		t.Fatalf("wake calls after reopen = %d, want 2", calls.Load())
	}
}

func TestConcurrentFailedWakeSharesAttemptWithoutReadingStaleMarker(t *testing.T) {
	h, conv := settledHibernationHarness(t, domain.TurnStateCompleted)
	ctx := context.Background()
	if slept, err := h.svc.HibernateChat(ctx, testSession); err != nil || !slept {
		t.Fatalf("hibernate = %v, %v", slept, err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	wakeErr := errors.New("resume failed")
	var calls atomic.Int32
	h.svc.SetWakeCallback(func(ctx context.Context, id domain.SessionID) error {
		calls.Add(1)
		close(started)
		<-release
		rec, _, err := h.st.GetSession(ctx, id)
		if err != nil {
			return err
		}
		if applied, err := h.st.SetSessionHibernated(ctx, id, rec.Revision, nil); err != nil || !applied {
			return fmt.Errorf("clear failed wake marker: applied=%v: %w", applied, err)
		}
		return wakeErr
	})
	results := make(chan error, 2)
	go func() { results <- h.svc.SetChatView(ctx, testSession, "view-1", true) }()
	<-started
	conv.sessionReads.Store(0)
	go func() { results <- h.svc.SetChatView(ctx, testSession, "view-2", true) }()
	deadline := time.Now().Add(2 * time.Second)
	for conv.sessionReads.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
	reads := conv.sessionReads.Load()
	close(release)
	for range 2 {
		if err := <-results; !errors.Is(err, wakeErr) {
			t.Fatalf("shared wake = %v", err)
		}
	}
	if reads != 1 || calls.Load() != 1 {
		t.Fatalf("joined wake reads=%d attempts=%d; want one view read and one shared attempt", reads, calls.Load())
	}
}

func TestOpeningViewDoesNotResumeExplicitlyStoppedAgent(t *testing.T) {
	h, _ := settledHibernationHarness(t, domain.TurnStateCompleted)
	ctx := context.Background()
	if err := h.svc.Stop(ctx, testSession); err != nil {
		t.Fatal(err)
	}
	var wakeCalls atomic.Int32
	h.svc.SetWakeCallback(func(context.Context, domain.SessionID) error {
		wakeCalls.Add(1)
		return nil
	})
	if err := h.svc.SetChatView(ctx, testSession, "viewer-1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "do not restart"}); !errors.Is(err, chatsvc.ErrNoController) {
		t.Fatalf("Send after stop = %v, want no controller", err)
	}
	if _, err := h.svc.RelayChatTurnWithID(ctx, testSession, "do not restart", "stopped-relay"); !errors.Is(err, chatsvc.ErrNoController) {
		t.Fatalf("Relay after stop = %v, want no controller", err)
	}
	if _, err := h.svc.SetTitle(ctx, testSession, "do not restart"); !errors.Is(err, chatsvc.ErrNoController) {
		t.Fatalf("SetTitle after stop = %v, want no controller", err)
	}
	if got := wakeCalls.Load(); got != 0 {
		t.Fatalf("opening an exited agent triggered %d native wakes", got)
	}
}

func TestStopClearsHibernationEvenWhenHostStopFails(t *testing.T) {
	h, _ := settledHibernationHarness(t, domain.TurnStateCompleted)
	ctx := context.Background()
	if slept, err := h.svc.HibernateChat(ctx, testSession); err != nil || !slept {
		t.Fatalf("HibernateChat = %v, %v", slept, err)
	}
	hostErr := errors.New("host stop failed")
	svc := chatsvc.New(chatsvc.Options{
		Sessions:         h.st,
		StopProviderHost: func(context.Context, domain.SessionID) error { return hostErr },
	})
	if err := svc.Stop(ctx, testSession); !errors.Is(err, hostErr) {
		t.Fatalf("Stop = %v, want host error", err)
	}
	rec, _, err := h.st.GetSession(ctx, testSession)
	if err != nil || rec.HibernatedAt != nil {
		t.Fatalf("marker after failed stop = %v, err=%v", rec.HibernatedAt, err)
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
			return fmt.Errorf("read hibernated session: found=%v marker=%v err=%w", found, rec.HibernatedAt, err)
		}
		cleared, err := h.st.SetSessionHibernated(ctx, id, rec.Revision, nil)
		if err != nil || !cleared {
			return fmt.Errorf("clear hibernation: applied=%v err=%w", cleared, err)
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

func TestOpeningViewDoesNotReportStoppedDuringNativeWake(t *testing.T) {
	h, _ := settledHibernationHarness(t, domain.TurnStateCompleted)
	ctx := context.Background()
	if hibernated, err := h.svc.HibernateChat(ctx, testSession); err != nil || !hibernated {
		t.Fatalf("hibernate = %v, %v", hibernated, err)
	}
	wakeService := chatsvc.New(chatsvc.Options{
		Store: h.st, Reader: fullSnapshotReader(h.st), Sessions: h.st,
		Log: slog.New(slog.DiscardHandler), Now: h.now,
	})
	started := make(chan struct{})
	release := make(chan struct{})
	wakeService.SetWakeCallback(func(ctx context.Context, id domain.SessionID) error {
		rec, found, err := h.st.GetSession(ctx, id)
		if err != nil || !found || rec.HibernatedAt == nil {
			return fmt.Errorf("read sleeping session: found=%v err=%w", found, err)
		}
		if cleared, err := h.st.SetSessionHibernated(ctx, id, rec.Revision, nil); err != nil || !cleared {
			return fmt.Errorf("clear sleeping marker: cleared=%v err=%w", cleared, err)
		}
		close(started)
		<-release
		return errors.New("provider unavailable")
	})
	result := make(chan error, 1)
	go func() { result <- wakeService.SetChatView(ctx, testSession, "viewer-1", true) }()
	select {
	case <-started:
	case err := <-result:
		t.Fatalf("wake returned before native startup: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("native wake did not start")
	}
	snapshot, err := wakeService.Snapshot(ctx, testSession)
	close(release)
	if err != nil || snapshot.Controller != ports.ChatControllerHibernated {
		t.Fatalf("snapshot during native wake = %q, %v", snapshot.Controller, err)
	}
	if err := <-result; err == nil {
		t.Fatal("wake unexpectedly succeeded")
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
	// A concurrent opportunistic sweep must skip the occupied gate, not block
	// explicit Exit/Kill behind a hibernation operation waiting on provider I/O.
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if slept, err := h.svc.HibernateChat(probeCtx, testSession); slept || err != nil {
		t.Fatalf("concurrent hibernation = %v, %v, want skipped", slept, err)
	}
	wakeCalled := make(chan struct{}, 1)
	h.svc.SetWakeCallback(func(ctx context.Context, id domain.SessionID) error {
		rec, found, err := h.st.GetSession(ctx, id)
		if err != nil || !found || rec.HibernatedAt == nil {
			return fmt.Errorf("wake saw unfinished hibernation: found=%v marker=%v err=%w", found, rec.HibernatedAt, err)
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

func TestSendWhileHibernatedReturnsBeforeWakeAndDrainsQueue(t *testing.T) {
	testSendWhileHibernatedReturnsBeforeWakeAndDrainsQueue(t, false, false)
}

func TestSendSurvivesCancelledViewWake(t *testing.T) {
	testSendWhileHibernatedReturnsBeforeWakeAndDrainsQueue(t, true, false)
}

func TestSendAfterRecoveredHibernationDrainsQueueOnce(t *testing.T) {
	testSendWhileHibernatedReturnsBeforeWakeAndDrainsQueue(t, false, true)
}

func testSendWhileHibernatedReturnsBeforeWakeAndDrainsQueue(t *testing.T, cancelView, failedShutdown bool) {
	h, old := settledHibernationHarness(t, domain.TurnStateCompleted)
	if failedShutdown {
		old.shutdownErr = errors.New("shutdown acknowledgement lost")
	}
	ctx := context.Background()
	if hibernated, err := h.svc.HibernateChat(ctx, testSession); err != nil || !hibernated {
		t.Fatalf("hibernate = %v, %v", hibernated, err)
	}

	resumed := newFakeConversation()
	resumed.turnSeq = 1
	var resumeConfig ports.ChatResumeConfig
	wakeService := chatsvc.New(chatsvc.Options{
		Store: h.st, Reader: fullSnapshotReader(h.st), Sessions: h.st,
		Drivers:  fakeRegistry{driver: fakeDriver{conv: resumed, resumeCfg: &resumeConfig}},
		Activity: h.activity, Log: slog.New(slog.DiscardHandler), Now: h.now,
		NewID: func() string { return "background-wake" },
	})
	t.Cleanup(func() { _ = wakeService.Stop(context.Background(), testSession) })
	wakeEntered := make(chan struct{})
	releaseWake := make(chan struct{})
	wakeService.SetWakeCallback(func(ctx context.Context, id domain.SessionID) error {
		close(wakeEntered)
		select {
		case <-releaseWake:
		case <-ctx.Done():
			return ctx.Err()
		}
		rec, found, err := h.st.GetSession(ctx, id)
		if err != nil || !found || rec.HibernatedAt == nil {
			return fmt.Errorf("read hibernated session: found=%v marker=%v err=%w", found, rec.HibernatedAt, err)
		}
		cleared, err := h.st.SetSessionHibernated(ctx, id, rec.Revision, nil)
		if err != nil || !cleared {
			return fmt.Errorf("clear hibernation: applied=%v err=%w", cleared, err)
		}
		_, err = wakeService.Start(ctx, chatsvc.StartConfig{
			SessionID: id, ProjectID: testProject, Harness: domain.HarnessCodex,
			WorkspacePath: t.TempDir(), ProviderConversationID: old.ProviderConversationID(),
		})
		return err
	})

	if cancelView {
		viewCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() { _ = wakeService.SetChatView(viewCtx, testSession, "cancelled-view", true) }()
		select {
		case <-wakeEntered:
		case <-time.After(time.Second):
			t.Fatal("view wake did not start")
		}
		cancel()
	}
	started := time.Now()
	turn, err := wakeService.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "send while cold", ClientMessageID: "cold-send-1",
	})
	if err != nil || turn.State != domain.TurnStateQueued || time.Since(started) > 250*time.Millisecond {
		t.Fatalf("cold send = %+v, %v; elapsed=%s", turn, err, time.Since(started))
	}
	select {
	case <-wakeEntered:
	case <-time.After(time.Second):
		t.Fatal("background wake did not start")
	}
	if got := resumed.sentTexts(); len(got) != 0 {
		t.Fatalf("message reached provider before wake completed: %v", got)
	}
	close(releaseWake)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := resumed.sentTexts(); len(got) == 1 && got[0] == "send while cold" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("background queue was not drained: %v", resumed.sentTexts())
}

func TestFailedBackgroundWakeSettlesAllQueuedMessagesOnce(t *testing.T) {
	h, _ := settledHibernationHarness(t, domain.TurnStateCompleted)
	ctx := context.Background()
	if hibernated, err := h.svc.HibernateChat(ctx, testSession); err != nil || !hibernated {
		t.Fatalf("hibernate = %v, %v", hibernated, err)
	}
	wakeErr := errors.New("provider conversation is unavailable")
	var wakeCalls atomic.Int32
	wakeStarted := make(chan struct{}, 2)
	releaseWake := make(chan struct{})
	h.svc.SetWakeCallback(func(context.Context, domain.SessionID) error {
		wakeCalls.Add(1)
		wakeStarted <- struct{}{}
		<-releaseWake
		return wakeErr
	})
	turn, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "first cold message", ClientMessageID: "cold-fail-0",
	})
	if err != nil || turn.State != domain.TurnStateQueued {
		t.Fatalf("first cold send = %+v, %v", turn, err)
	}
	select {
	case <-wakeStarted:
	case <-time.After(time.Second):
		t.Fatal("background wake did not start")
	}
	turn, err = h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "second cold message", ClientMessageID: "cold-fail-1",
	})
	if err != nil || turn.State != domain.TurnStateQueued {
		t.Fatalf("second cold send = %+v, %v", turn, err)
	}
	close(releaseWake)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := h.svc.Snapshot(ctx, testSession)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Turns) >= 3 && snapshot.Turns[len(snapshot.Turns)-2].State == domain.TurnStateFailed && snapshot.Turns[len(snapshot.Turns)-1].State == domain.TurnStateFailed {
			if wakeCalls.Load() != 1 {
				t.Fatalf("wake attempts = %d, want one shared attempt", wakeCalls.Load())
			}
			if snapshot.Turns[len(snapshot.Turns)-2].ErrorMessage == "" || snapshot.Turns[len(snapshot.Turns)-1].ErrorMessage == "" {
				t.Fatal("failed cold turns did not retain an error")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("queued turns were not settled after wake failure; wake attempts=%d", wakeCalls.Load())
}

func TestSendAfterFailedHibernationCompletesShutdownBeforeWake(t *testing.T) {
	h, conv := settledHibernationHarness(t, domain.TurnStateCompleted)
	conv.keepOpen = true
	conv.hostShutdownErr = errors.New("host still unreachable")
	if hibernated, err := h.svc.HibernateChat(context.Background(), testSession); hibernated || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed-out hibernation = %v, %v", hibernated, err)
	}
	wakeReached := make(chan struct{}, 1)
	h.svc.SetWakeCallback(func(context.Context, domain.SessionID) error {
		if h.svc.HasLiveChatController(testSession) {
			return errors.New("wake started before the old provider stopped")
		}
		wakeReached <- struct{}{}
		return errors.New("wake failed")
	})
	conv.hostShutdownErr = nil
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	turn, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "wake", ClientMessageID: "wake-after-timeout"})
	if err != nil || turn.State != domain.TurnStateQueued {
		t.Fatalf("optimistic send during delayed shutdown = %+v, %v", turn, err)
	}
	select {
	case <-wakeReached:
		if conv.hostStops.Load() != 2 {
			t.Fatalf("host shutdown attempts = %d, want recovery on new input", conv.hostStops.Load())
		}
	case <-ctx.Done():
		t.Fatal("new input did not recover the failed shutdown")
	}
}

func TestFailedHibernateCanBeKilledThroughHostShutdown(t *testing.T) {
	h, conv := settledHibernationHarness(t, domain.TurnStateCompleted)
	conv.shutdownErr = errors.New("shutdown acknowledgement lost")
	conv.hostShutdownErr = errors.New("host still unreachable")
	ctx := context.Background()
	if slept, err := h.svc.HibernateChat(ctx, testSession); slept || !errors.Is(err, conv.shutdownErr) {
		t.Fatalf("hibernate = %v, %v", slept, err)
	}
	rec, _, err := h.st.GetSession(ctx, testSession)
	if err != nil || rec.HibernatedAt == nil {
		t.Fatalf("shutdown intent lost = %+v, %v", rec.HibernatedAt, err)
	}
	if turn, err := h.svc.QueueUserMessage(ctx, testSession, ports.ChatUserMessage{Text: "pending during shutdown"}); err != nil || turn.State != domain.TurnStateQueued {
		t.Fatalf("queue during failed shutdown = %+v, %v", turn, err)
	}
	conv.hostShutdownErr = nil
	if err := h.svc.Stop(ctx, testSession); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.NextQueuedTurn(ctx, h.ctrl.ConversationID()); !errors.Is(err, domain.ErrNoQueuedTurn) {
		t.Fatalf("explicit Kill left a queued message: %v", err)
	}
	rec, _, err = h.st.GetSession(ctx, testSession)
	if err != nil || rec.HibernatedAt != nil || h.svc.HasLiveChatController(testSession) || conv.hostStops.Load() != 2 {
		t.Fatalf("explicit Kill did not retire failed hibernation: %+v, %v", rec.HibernatedAt, err)
	}
}

func TestFailedHibernateConfirmsHostShutdownBeforeReturning(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprintf("timeout=%v", timeout), func(t *testing.T) {
			h, conv := settledHibernationHarness(t, domain.TurnStateCompleted)
			if timeout {
				conv.keepOpen = true
			} else {
				conv.shutdownErr = errors.New("shutdown acknowledgement lost")
			}
			slept, err := h.svc.HibernateChat(context.Background(), testSession)
			if err != nil || !slept || conv.hostStops.Load() != 1 || h.svc.HasLiveChatController(testSession) {
				t.Fatalf("failed shutdown did not recover: slept=%v err=%v hostStops=%d live=%v", slept, err, conv.hostStops.Load(), h.svc.HasLiveChatController(testSession))
			}
			rec, _, err := h.st.GetSession(context.Background(), testSession)
			if err != nil || rec.HibernatedAt == nil {
				t.Fatalf("confirmed shutdown lost native resume intent: %+v, %v", rec.HibernatedAt, err)
			}
		})
	}
}

func TestBackgroundInventoryDoesNotBlockChatOperations(t *testing.T) {
	for _, operation := range []string{"open", "send", "kill"} {
		t.Run(operation, func(t *testing.T) {
			h, conv := settledHibernationHarness(t, domain.TurnStateCompleted)
			started := make(chan struct{})
			release := make(chan struct{})
			conv.onBackgroundCheck = func(context.Context) {
				close(started)
				<-release
			}
			done := make(chan error, 1)
			go func() {
				_, err := h.svc.HibernateChat(context.Background(), testSession)
				done <- err
			}()
			<-started
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var err error
			switch operation {
			case "open":
				err = h.svc.SetChatView(ctx, testSession, "opened-during-inventory", true)
			case "send":
				_, err = h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "new work", ClientMessageID: "during-inventory"})
			case "kill":
				err = h.svc.Stop(ctx, testSession)
			}
			close(release)
			if shutdownErr := <-done; shutdownErr != nil {
				t.Fatal(shutdownErr)
			}
			if err != nil || conv.calls.Load() != 0 {
				t.Fatalf("operation blocked or invalidated check slept: operation=%s error=%v shutdowns=%d", operation, err, conv.calls.Load())
			}
		})
	}
}

func TestAcceptedRenameDoesNotRequireProviderNotification(t *testing.T) {
	h, conv := settledHibernationHarness(t, domain.TurnStateCompleted)
	ctx := context.Background()
	const title = "Renamed session"
	if _, err := h.svc.SetTitle(ctx, testSession, title); err != nil {
		t.Fatal(err)
	}
	snapshot, err := h.svc.Snapshot(ctx, testSession)
	if err != nil || snapshot.Conversation.ProviderTitle != title {
		t.Fatalf("accepted title not persisted: %+v err=%v", snapshot.Conversation, err)
	}
	if hibernated, err := h.svc.HibernateChat(ctx, testSession); err != nil || !hibernated || conv.calls.Load() != 1 {
		t.Fatalf("hibernate after accepted rename = %v, %v; stops=%d", hibernated, err, conv.calls.Load())
	}
}

func TestHibernateChatRechecksActivityBeforeStoppingProvider(t *testing.T) {
	h, conv := settledHibernationHarness(t, domain.TurnStateCompleted)
	conv.onEligibilityRead = func() {
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
		// A restart skips the wait for the chat to be closed and quiet for a
		// while. It skips none of the checks that the chat is doing nothing.
		for _, entry := range []string{"idle", "restart"} {
			t.Run(tc.name+"/"+entry, func(t *testing.T) {
				h, conv := settledHibernationHarness(t, tc.state)
				if tc.add != nil {
					tc.add(t, h, conv)
				}
				stop := h.svc.HibernateChat
				if entry == "restart" {
					stop = h.svc.HibernateChatForRestart
				}
				hibernated, err := stop(context.Background(), testSession)
				if err != nil || hibernated || conv.calls.Load() != 0 {
					t.Fatalf("stopped = %v, %v; provider calls = %d", hibernated, err, conv.calls.Load())
				}
				rec, found, err := h.st.GetSession(context.Background(), testSession)
				if err != nil || !found || rec.HibernatedAt != nil {
					t.Fatalf("session marker after rejected hibernation = %+v, %v, %v", rec.HibernatedAt, found, err)
				}
			})
		}
	}
}

// A restart is not idle hibernation: it does not wait for the setting, for the
// chat to be closed, or for it to have been quiet for a while, and it may stop
// an orchestrator, because the caller starts the provider again at once.
func TestHibernateChatForRestartStopsAQuietChatThatIdleHibernationWouldLeave(t *testing.T) {
	for _, kind := range []domain.SessionKind{domain.KindWorker, domain.KindOrchestrator} {
		t.Run(string(kind), func(t *testing.T) {
			h, conv := settledHibernationHarness(t, domain.TurnStateCompleted, func() bool { return false })
			ctx := context.Background()
			if err := h.svc.SetChatView(ctx, testSession, "viewer", true); err != nil {
				t.Fatal(err)
			}
			rec, _, err := h.st.GetSession(ctx, testSession)
			if err != nil {
				t.Fatal(err)
			}
			rec.Kind = kind
			rec.Activity.LastActivityAt = h.now()
			if err := h.st.UpdateSession(ctx, rec); err != nil {
				t.Fatal(err)
			}
			if stopped, err := h.svc.HibernateChat(ctx, testSession); err != nil || stopped || conv.calls.Load() != 0 {
				t.Fatalf("idle hibernation: stopped=%v err=%v calls=%d", stopped, err, conv.calls.Load())
			}
			if stopped, err := h.svc.HibernateChatForRestart(ctx, testSession); err != nil || !stopped || conv.calls.Load() != 1 {
				t.Fatalf("restart: stopped=%v err=%v calls=%d", stopped, err, conv.calls.Load())
			}
			rec, _, err = h.st.GetSession(ctx, testSession)
			if err != nil || rec.HibernatedAt == nil {
				t.Fatalf("a stopped chat was not marked resumable: at=%v err=%v", rec.HibernatedAt, err)
			}
		})
	}
}

func TestHibernateChatForRestartLeavesBackgroundWorkRunning(t *testing.T) {
	h, conv := settledHibernationHarness(t, domain.TurnStateCompleted, func() bool { return false })
	conv.backgroundRunning = true
	if stopped, err := h.svc.HibernateChatForRestart(context.Background(), testSession); err != nil || stopped || conv.calls.Load() != 0 {
		t.Fatalf("stopped=%v err=%v calls=%d", stopped, err, conv.calls.Load())
	}
}

func TestHibernateChatSettledTurn(t *testing.T) {
	for _, state := range []domain.TurnState{
		domain.TurnStateFailed,
		domain.TurnStateInterrupted,
		domain.TurnStateRecovered,
	} {
		t.Run(string(state), func(t *testing.T) {
			h, conv := settledHibernationHarness(t, state)
			hibernated, err := h.svc.HibernateChat(context.Background(), testSession)
			if err != nil || !hibernated || conv.calls.Load() != 1 {
				t.Fatalf("HibernateChat = %v, %v; provider calls = %d", hibernated, err, conv.calls.Load())
			}
			rec, found, err := h.st.GetSession(context.Background(), testSession)
			if err != nil || !found || rec.HibernatedAt == nil {
				t.Fatalf("session marker after hibernation = %+v, %v, %v", rec.HibernatedAt, found, err)
			}
		})
	}
}

func TestSendDuringHibernationAcceptsThenWakesNativeConversation(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
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
		NewID:              func() string { return fmt.Sprintf("hibernate-race-%d", nextID.Add(1)) },
		HibernationEnabled: func() bool { return true },
	})
	h.svc = svc
	start := chatsvc.StartConfig{SessionID: testSession, ProjectID: testProject, Harness: domain.HarnessCodex, WorkspacePath: t.TempDir()}
	ctrl, err := svc.Start(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	h.ctrl = ctrl
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = svc.Stop(stopCtx, testSession)
	})
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
	// Wait for projection to release the dispatch lock before testing shutdown.
	if err := svc.DrainQueued(ctx, testSession); err != nil {
		t.Fatal(err)
	}
	h.advance(6 * time.Minute)
	rec, found, err := st.GetSession(ctx, testSession)
	if err != nil || !found {
		t.Fatalf("get session = %v, %v", found, err)
	}
	rec.Kind = domain.KindWorker
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
	turn, err := svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "after sleep", ClientMessageID: "after-sleep"})
	if err != nil || turn.State != domain.TurnStateQueued {
		t.Fatalf("optimistic send during hibernation = %+v, %v", turn, err)
	}
	if got := first.sentTexts(); len(got) != 1 || len(resumed.sentTexts()) != 0 {
		t.Fatalf("message dispatched before shutdown: old=%v new=%v", got, resumed.sentTexts())
	}
	first.emit(ports.ChatEvent{Kind: ports.ChatEventControllerState, ControllerState: ports.ChatControllerStopped})
	close(release)
	if err := <-result; err != nil {
		t.Fatalf("hibernate = %v", err)
	}
	h.awaitSnapshot(t, func(store.ConversationSnapshot) bool { return len(resumed.sentTexts()) == 1 })
	if got := resumed.sentTexts(); len(got) != 1 || got[0] != "after sleep" {
		t.Fatalf("resumed provider turns = %v", got)
	}
	rec, found, err = st.GetSession(ctx, testSession)
	if err != nil || !found || rec.HibernatedAt != nil {
		t.Fatalf("session after wake = %+v, %v, %v", rec.HibernatedAt, found, err)
	}
}
