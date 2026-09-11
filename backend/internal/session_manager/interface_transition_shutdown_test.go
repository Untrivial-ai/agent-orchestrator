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

			if _, err := m.recoverInterruptedInterfaceTransitions(context.Background()); err == nil {
				t.Fatal("startup restored TUI while target shutdown still failed")
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

	if _, err := m.recoverInterruptedInterfaceTransitions(ctx); err == nil {
		t.Fatal("startup released the Chat-to-TUI fence without confirming target shutdown")
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
