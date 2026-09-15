package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type shutdownGuardTransitionChat struct {
	*transitionChat
	startErrors []error
	stopErr     error
}

type failedShutdownMarkerStore struct {
	*transitionStore
	err error
}

type cancelDuringRecoveryStore struct {
	*transitionStore
	cancel func() error
}

func (s *cancelDuringRecoveryStore) AdvanceSessionInterfaceTransition(ctx context.Context, id string, expected, next domain.SessionInterfaceTransitionPhase, nativeID, code, detail string, at time.Time) (bool, error) {
	if next == domain.SessionInterfaceTransitionRecovery {
		if err := s.cancel(); err != nil {
			return false, err
		}
	}
	return s.transitionStore.AdvanceSessionInterfaceTransition(ctx, id, expected, next, nativeID, code, detail, at)
}

func TestDeferredInterfaceRecoveryReleasesCancelledTransition(t *testing.T) {
	ctx := context.Background()
	m, st, _, _, _ := newTransitionManager(t, domain.SessionModeTUI)
	_, _, err := st.CreateSessionInterfaceTransition(ctx, domain.SessionInterfaceTransition{
		ID: "cancel-race", SessionID: "session-1", SourceMode: domain.SessionModeTUI, TargetMode: domain.SessionModeChat,
		Phase: domain.SessionInterfaceTransitionPreflighting, NativeConversationID: "native-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	m.store = &cancelDuringRecoveryStore{transitionStore: st, cancel: func() error {
		return m.CancelInterfaceTransition(ctx, "session-1")
	}}
	if _, err := m.recoverInterfaceTransitions(ctx, "cancel-race"); err != nil {
		t.Fatalf("cancelled recovery failed: %v", err)
	}
	if m.SessionMutationInProgress("session-1") {
		t.Fatal("durably cancelled recovery retained its input fence")
	}
}

func (s *failedShutdownMarkerStore) AdvanceSessionInterfaceTransition(ctx context.Context, id string, expected, next domain.SessionInterfaceTransitionPhase, nativeID, code, detail string, at time.Time) (bool, error) {
	if code == "TARGET_STOP_UNCONFIRMED" {
		return false, s.err
	}
	return s.transitionStore.AdvanceSessionInterfaceTransition(ctx, id, expected, next, nativeID, code, detail, at)
}

func TestStartupReportsUnpersistedShutdownMarker(t *testing.T) {
	m, st, _, chat, _ := newTransitionManager(t, domain.SessionModeChat)
	m.chat = &shutdownGuardTransitionChat{transitionChat: chat, stopErr: errors.New("host remains alive")}
	want := errors.New("marker write failed")
	m.store = &failedShutdownMarkerStore{transitionStore: st, err: want}
	_, _, err := st.CreateSessionInterfaceTransition(context.Background(), domain.SessionInterfaceTransition{
		ID: "unmarked", SessionID: "session-1", SourceMode: domain.SessionModeTUI, TargetMode: domain.SessionModeChat,
		Phase: domain.SessionInterfaceTransitionTargetStarting, NativeConversationID: "native-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ReconcileStartupSafety(context.Background()); !errors.Is(err, want) {
		t.Fatalf("startup swallowed unpersisted recovery marker: %v", err)
	}
	if release, ok := m.AcquireSessionInput("session-1"); ok {
		release()
		t.Fatal("persistence failure released target fence")
	}
}

func TestStartupDefersInterfaceRecoveryBehindAccountOperation(t *testing.T) {
	ctx := context.Background()
	m, st, _, _, _ := newTransitionManager(t, domain.SessionModeChat)
	_, created, err := st.CreateSessionInterfaceTransition(ctx, domain.SessionInterfaceTransition{
		ID: "interrupted", SessionID: "session-1", SourceMode: domain.SessionModeTUI, TargetMode: domain.SessionModeChat,
		Phase: domain.SessionInterfaceTransitionTargetStarting, NativeConversationID: "native-1",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	if err != nil || !created {
		t.Fatalf("seed transition: %v %v", created, err)
	}
	if err := m.beginOrReclaimCodexAccountSwitchOperation(ctx, "session-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.recoverInterruptedInterfaceTransitions(ctx); err != nil {
		t.Fatalf("account recovery collision aborted startup: %v", err)
	}
	if release, ok := m.AcquireSessionInput("session-1"); ok {
		release()
		t.Fatal("deferred transition admitted input")
	}
	// This handoff started after startup. A deferred recovery must not sweep it.
	other := st.sessions["session-1"]
	other.ID = "live-session"
	st.sessions[other.ID] = other
	_, _, err = st.CreateSessionInterfaceTransition(ctx, domain.SessionInterfaceTransition{
		ID: "live-handoff", SessionID: other.ID, SourceMode: domain.SessionModeChat, TargetMode: domain.SessionModeTUI,
		Phase: domain.SessionInterfaceTransitionRequested, NativeConversationID: "native-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	m.endAgentOperation("session-1", agentOperationCodexAccountSwitch)
	// Even immediately after releasing the account gate, the durable handoff
	// must either be recovered or still protected from input/reaper/restore.
	if !m.SessionMutationInProgress("session-1") {
		if _, active, err := st.GetActiveSessionInterfaceTransition(ctx, "session-1"); err != nil || active {
			t.Fatalf("account release exposed unrecovered handoff: %v %v", active, err)
		}
	}
	m.agentSwitchWorkers.Wait()
	if _, active, err := st.GetActiveSessionInterfaceTransition(ctx, "session-1"); err != nil || active {
		t.Fatalf("deferred handoff never recovered: %v %v", active, err)
	}
	if current, found, err := st.GetSessionInterfaceTransition(ctx, "live-handoff"); err != nil || !found || current.Phase != domain.SessionInterfaceTransitionRequested {
		t.Fatalf("deferred recovery changed a live unrelated handoff: %+v %v", current, err)
	}
}

func TestDeferredInterfaceRecoveryRespectsWorkerShutdown(t *testing.T) {
	m, _, _, _, _ := newTransitionManager(t, domain.SessionModeChat)
	m.deferredInterfaceRecovery = map[domain.SessionID]string{"session-1": "interrupted"}
	m.agentOperations["session-1"] = agentOperationCodexAccountSwitch
	if err := m.WaitAgentSwitchWorkers(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.endAgentOperation("session-1", agentOperationCodexAccountSwitch)
	if !m.SessionMutationInProgress("session-1") {
		t.Fatal("shutdown release removed the deferred input fence")
	}
	m.agentSwitchWorkers.Wait()
}

func TestStartupQuarantinesUnconfirmedTargetWithoutBlockingOtherSessions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, st, _, chat, _ := newTransitionManager(t, domain.SessionModeChat)
	guard := &shutdownGuardTransitionChat{transitionChat: chat, stopErr: errors.New("host remains alive")}
	m.chat = guard
	_, created, err := st.CreateSessionInterfaceTransition(ctx, domain.SessionInterfaceTransition{
		ID: "interrupted", SessionID: "session-1", SourceMode: domain.SessionModeTUI, TargetMode: domain.SessionModeChat,
		Phase: domain.SessionInterfaceTransitionTargetStarting, NativeConversationID: "native-1",
		ErrorCode: "TARGET_STOP_UNCONFIRMED", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	if err != nil || !created {
		t.Fatalf("seed transition: created=%v err=%v", created, err)
	}
	healthy := st.sessions["session-1"]
	healthy.ID = "healthy"
	st.sessions[healthy.ID] = healthy
	_, created, err = st.CreateSessionInterfaceTransition(ctx, domain.SessionInterfaceTransition{
		ID: "healthy-interrupted", SessionID: healthy.ID, SourceMode: domain.SessionModeChat, TargetMode: domain.SessionModeTUI,
		Phase: domain.SessionInterfaceTransitionPreflighting, NativeConversationID: "native-1",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	if err != nil || !created {
		t.Fatalf("seed healthy transition: created=%v err=%v", created, err)
	}
	if err := m.ReconcileStartupSafety(ctx); err != nil {
		t.Fatalf("one uncertain session prevented daemon startup: %v", err)
	}
	if _, active, err := st.GetActiveSessionInterfaceTransition(ctx, healthy.ID); err != nil || active {
		t.Fatalf("unrelated transition was not recovered: active=%v err=%v", active, err)
	}
	if release, ok := m.AcquireSessionInput("session-1"); ok {
		release()
		t.Fatal("quarantined target accepted input")
	}
	if !m.SessionMutationInProgress("session-1") {
		t.Fatal("quarantined target is open to the reaper")
	}
	acquired, err := m.beginAgentOperations(ctx, []domain.SessionID{"session-1", "healthy"}, agentOperationReconcile)
	if err != nil || len(acquired) != 1 || acquired[0] != healthy.ID {
		t.Fatalf("background restore admission = %v, %v; want only healthy", acquired, err)
	}
	m.endAgentOperation(healthy.ID, agentOperationReconcile)
	if release, ok := m.AcquireSessionInput(healthy.ID); !ok {
		t.Fatal("unrelated session cannot accept input")
	} else {
		release()
	}
	guard.stopErr = nil
	if _, err := m.recoverInterruptedInterfaceTransitions(ctx); err != nil {
		t.Fatalf("conclusive recovery: %v", err)
	}
	if m.SessionMutationInProgress("session-1") || st.sessions["session-1"].Mode != domain.SessionModeTUI {
		t.Fatal("conclusive shutdown did not release quarantine and restore source ownership")
	}
}

func (c *shutdownGuardTransitionChat) StartChat(ctx context.Context, cfg ChatStart) (ChatStarted, error) {
	if len(c.startErrors) > 0 {
		c.startErr = c.startErrors[0]
		c.startErrors = c.startErrors[1:]
	} else {
		c.startErr = nil
	}
	return c.transitionChat.StartChat(ctx, cfg)
}

func (c *shutdownGuardTransitionChat) StopChat(ctx context.Context, id domain.SessionID) error {
	_ = c.transitionChat.StopChat(ctx, id)
	return c.stopErr
}

func TestInterfaceTransitionConfirmsTargetShutdownBeforeHistoryRetry(t *testing.T) {
	m, st, _, chat, log := newTransitionManager(t, domain.SessionModeTUI)
	useFastInterfaceTransitionTimings(m)
	m.chat = &shutdownGuardTransitionChat{
		transitionChat: chat, startErrors: []error{ports.ErrChatHistoryUnsettled, nil},
	}
	tr, err := m.StartInterfaceTransition(context.Background(), "session-1", domain.SessionModeChat,
		domain.SessionInterfaceTransitionDrain, domain.SessionInterfaceTransitionHistoryStrict)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, st, tr.ID)
	if settled.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("transition = %+v, want successful fresh observation", settled)
	}
	if got := fmt.Sprint(*log); got != "[stop:tui:runtime-1 start:chat stop:chat start:chat]" {
		t.Fatalf("target retry order = %s", got)
	}
}

func TestInterfaceTransitionRetainsFenceWhenTargetShutdownIsUnconfirmed(t *testing.T) {
	for _, startErr := range []error{ports.ErrChatHistoryUnsettled, errors.New("provider admission failed")} {
		t.Run(startErr.Error(), func(t *testing.T) {
			m, st, runtime, chat, log := newTransitionManager(t, domain.SessionModeTUI)
			useFastInterfaceTransitionTimings(m)
			guard := &shutdownGuardTransitionChat{
				transitionChat: chat, startErrors: []error{startErr}, stopErr: errors.New("host remains alive"),
			}
			m.chat = guard
			tr, err := m.StartInterfaceTransition(context.Background(), "session-1", domain.SessionModeChat,
				domain.SessionInterfaceTransitionDrain, domain.SessionInterfaceTransitionHistoryStrict)
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				current, found, err := st.GetSessionInterfaceTransition(context.Background(), tr.ID)
				if err != nil || !found {
					t.Fatalf("read transition: found=%v err=%v", found, err)
				}
				if current.ErrorCode == "TARGET_STOP_UNCONFIRMED" {
					if !current.Active() || current.Phase != domain.SessionInterfaceTransitionTargetStarting {
						t.Fatalf("shutdown uncertainty released transition fence: %+v", current)
					}
					break
				}
				if current.Phase.Terminal() || time.Now().After(deadline) {
					t.Fatalf("failed target was retried or rolled back without shutdown proof: %+v", current)
				}
				time.Sleep(time.Millisecond)
			}
			if runtime.created != 0 || st.sessions["session-1"].Mode != domain.SessionModeChat {
				t.Fatalf("source relaunched despite surviving target: creates=%d mode=%s", runtime.created, st.sessions["session-1"].Mode)
			}
			if got := fmt.Sprint(*log); got != "[stop:tui:runtime-1 start:chat stop:chat stop:chat]" {
				t.Fatalf("unconfirmed target retried or source restarted: %s", got)
			}

			if _, err := m.recoverInterruptedInterfaceTransitions(context.Background()); err != nil {
				t.Fatalf("startup could not quarantine unconfirmed target: %v", err)
			}
			if st.sessions["session-1"].Mode != domain.SessionModeChat {
				t.Fatal("startup changed ownership before target shutdown")
			}
			guard.stopErr = nil
			if _, err := m.recoverInterruptedInterfaceTransitions(context.Background()); err != nil {
				t.Fatalf("startup after confirmed target shutdown: %v", err)
			}
			if st.sessions["session-1"].Mode != domain.SessionModeTUI {
				t.Fatal("startup failed to restore TUI after shutdown became conclusive")
			}
		})
	}
}

func TestInterfaceTransitionRollsBackInconclusiveHistoryCleanupWithoutRetry(t *testing.T) {
	m, st, runtime, chat, log := newTransitionManager(t, domain.SessionModeTUI)
	useFastInterfaceTransitionTimings(m)
	m.chat = &shutdownGuardTransitionChat{
		transitionChat: chat,
		startErrors:    []error{errors.Join(ports.ErrChatHistoryUnsettled, ports.ErrChatRecoveryInconclusive)},
	}
	tr, err := m.StartInterfaceTransition(context.Background(), "session-1", domain.SessionModeChat,
		domain.SessionInterfaceTransitionDrain, domain.SessionInterfaceTransitionHistoryStrict)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, st, tr.ID)
	if settled.Phase != domain.SessionInterfaceTransitionFailed || runtime.created != 1 {
		t.Fatalf("inconclusive startup = %+v, source restarts=%d", settled, runtime.created)
	}
	if got := fmt.Sprint(*log); got != "[stop:tui:runtime-1 start:chat stop:chat stop:tui:runtime-1 start:tui]" {
		t.Fatalf("inconclusive startup retried instead of safely restoring source: %s", got)
	}
}

// Fail the final restore read after MarkSpawned has committed the real target
// runtime handle. This models a transient store failure after a successful TUI
// launch, rather than inventing an otherwise unreachable retained transition.
type postTargetLaunchReadFailureStore struct {
	*transitionStore
	failRead bool
}

func (s *postTargetLaunchReadFailureStore) GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	rec, found, err := s.transitionStore.GetSession(ctx, id)
	if err == nil && found && s.failRead && rec.Mode == domain.SessionModeTUI && rec.Metadata.RuntimeHandleID == "h1" {
		s.failRead = false
		return domain.SessionRecord{}, false, errors.New("read failed after target launch committed")
	}
	return rec, found, err
}

func TestInterfaceTransitionChatToTUIRetainsShutdownFenceAcrossRestart(t *testing.T) {
	ctx := context.Background()
	m, st, runtime, _, log := newTransitionManager(t, domain.SessionModeChat)
	m.store = &postTargetLaunchReadFailureStore{transitionStore: st, failRead: true}
	runtime.destroyErr = errors.New("terminal target remains alive")
	tr, err := m.StartInterfaceTransition(ctx, "session-1", domain.SessionModeTUI,
		domain.SessionInterfaceTransitionInterrupt, domain.SessionInterfaceTransitionHistoryStrict)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		current, found, err := st.GetSessionInterfaceTransition(ctx, tr.ID)
		if err != nil || !found {
			t.Fatalf("read transition: found=%v err=%v", found, err)
		}
		if current.ErrorCode == "TARGET_STOP_UNCONFIRMED" {
			if !current.Active() || current.Phase != domain.SessionInterfaceTransitionTargetStarting {
				t.Fatalf("failed shutdown released transition fence: %+v", current)
			}
			break
		}
		if current.Phase.Terminal() || time.Now().After(deadline) {
			t.Fatalf("failed TUI target was not retained: %+v", current)
		}
		time.Sleep(time.Millisecond)
	}
	if runtime.created != 1 || !runtime.aliveByHandle["h1"] || st.sessions["session-1"].Mode != domain.SessionModeTUI {
		t.Fatalf("expected one committed live TUI target: creates=%d alive=%v mode=%s",
			runtime.created, runtime.aliveByHandle["h1"], st.sessions["session-1"].Mode)
	}
	if got := fmt.Sprint(*log); got != "[prepare:chat:interrupt stop:chat start:tui stop:tui:h1 stop:tui:h1]" {
		t.Fatalf("source restarted despite unconfirmed target shutdown: %s", got)
	}

	if _, err := m.recoverInterruptedInterfaceTransitions(ctx); err != nil {
		t.Fatalf("startup could not quarantine unconfirmed TUI target: %v", err)
	}
	current, found, err := st.GetActiveSessionInterfaceTransition(ctx, "session-1")
	if err != nil || !found || current.ErrorCode != "TARGET_STOP_UNCONFIRMED" {
		t.Fatalf("restart lost unconfirmed-shutdown fence: transition=%+v found=%v err=%v", current, found, err)
	}
	if st.sessions["session-1"].Mode != domain.SessionModeTUI || !runtime.aliveByHandle["h1"] {
		t.Fatal("restart changed ownership before target shutdown became conclusive")
	}

	runtime.destroyErr = nil
	if _, err := m.recoverInterruptedInterfaceTransitions(ctx); err != nil {
		t.Fatalf("restart after conclusive target shutdown: %v", err)
	}
	if st.sessions["session-1"].Mode != domain.SessionModeChat || runtime.aliveByHandle["h1"] {
		t.Fatal("restart failed to restore original Chat ownership after stopping the target")
	}
	if _, active, err := st.GetActiveSessionInterfaceTransition(ctx, "session-1"); err != nil || active {
		t.Fatalf("confirmed shutdown did not release recovery fence: active=%v err=%v", active, err)
	}
}
