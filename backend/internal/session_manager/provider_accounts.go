package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func (m *Manager) applyAccountEnv(ctx context.Context, id domain.SessionID, env map[string]string) error {
	if m.accounts == nil {
		return nil
	}
	managed, err := m.accounts.LaunchAccountEnv(ctx, id)
	maps.Copy(env, managed)
	return err
}

// sessionAccount is a session's route; false is a session whose sign-in Account Manager does not supply.
func (m *Manager) sessionAccount(ctx context.Context, id domain.SessionID) (domain.ProviderSessionRoute, bool, error) {
	if m.accounts == nil {
		return domain.ProviderSessionRoute{}, false, nil
	}
	return m.accounts.SessionAccount(ctx, id)
}

func (m *Manager) forgetAccount(ctx context.Context, id domain.SessionID) error {
	if m.accounts == nil {
		return nil
	}
	return m.accounts.ForgetAccount(ctx, id)
}

// RelatedAccountEnv adds its owning worker's ticket to a same-provider reviewer's environment.
func (m *Manager) RelatedAccountEnv(ctx context.Context, id domain.SessionID, harness domain.AgentHarness, env map[string]string) error {
	route, managed, err := m.sessionAccount(ctx, id)
	if err != nil || !managed || route.Provider != domain.AccountProvider(harness) {
		return err
	}
	if route.AccountID == "" {
		return ports.ErrProviderLoginRequired
	}
	return m.applyAccountEnv(ctx, id, env)
}

// agentSwitching reports a session changing agent: it has no settled provider.
func (m *Manager) agentSwitching(ctx context.Context, id domain.SessionID) (bool, error) {
	store, ok := m.store.(ports.AgentSwitchStore)
	if !ok {
		return false, nil
	}
	_, active, err := store.GetActiveAgentSwitch(ctx, id)
	return active, err
}

// adoptLegacyChat gives a chat that predates Account Manager its provider's
// default account. It runs only as the chat's provider process is launched.
func (m *Manager) adoptLegacyChat(ctx context.Context, rec domain.SessionRecord) {
	if m.accounts == nil || domain.AccountProvider(rec.Harness) == "" {
		return
	}
	if switching, err := m.agentSwitching(ctx, rec.ID); err != nil || switching {
		return
	}
	if _, err := m.accounts.AdoptSession(ctx, rec.ID, rec.Harness); err != nil {
		m.logger.Warn("chat left on this computer's own sign-in", "session", rec.ID, "error", err)
	}
}

// legacyCandidate reports a session whose process may still need an account.
func legacyCandidate(rec domain.SessionRecord) bool {
	return !rec.IsTerminated && rec.HibernatedAt == nil && domain.AccountProvider(rec.Harness) != ""
}

// MigrateLegacySessions restarts, four at a time, the idle sessions that still run
// without an account, and reports how many keep running without one. A chat
// that is asleep or stopped is adopted when it next starts and is not counted.
func (m *Manager) MigrateLegacySessions(ctx context.Context) (int, error) {
	if m.accounts == nil {
		return 0, nil
	}
	sessions, err := m.store.ListAllSessions(ctx)
	if err != nil {
		return 0, err
	}
	var remaining atomic.Int64
	failures := make([]error, len(sessions))
	var wg sync.WaitGroup
	for i, rec := range sessions {
		if !legacyCandidate(rec) {
			continue
		}
		wg.Go(func() {
			left, err := m.moveLegacySession(ctx, rec.ID)
			if err != nil {
				failures[i] = fmt.Errorf("move session %s onto its account: %w", rec.ID, err)
			}
			if left {
				remaining.Add(1)
			}
		})
	}
	wg.Wait()
	return int(remaining.Load()), errors.Join(failures...)
}

// SessionTurnEnded moves such a session as its turn ends, ahead of the next check.
func (m *Manager) SessionTurnEnded(rec domain.SessionRecord) {
	if m.accounts == nil || !legacyCandidate(rec) || m.beginAgentSwitchAttempt() != nil {
		return
	}
	go func() {
		defer m.agentSwitchWorkers.Done()
		ctx, cancel := context.WithTimeout(m.backgroundContext, 2*time.Minute)
		defer cancel()
		if _, managed, err := m.sessionAccount(ctx, rec.ID); err != nil || managed {
			return
		}
		if _, err := m.moveLegacySession(ctx, rec.ID); err != nil {
			m.logger.Warn("moving a session onto Account Manager", "session", rec.ID, "error", err)
		}
	}()
}

// moveLegacySession restarts one session onto an account if it runs without one and
// is idle now, a terminal also off screen. It reports whether it is still to be moved.
func (m *Manager) moveLegacySession(ctx context.Context, id domain.SessionID) (bool, error) {
	if _, moving := m.legacyMoving.LoadOrStore(id, struct{}{}); moving {
		return true, nil
	}
	defer m.legacyMoving.Delete(id)
	if err := m.legacySlots.Acquire(ctx, 1); err != nil {
		return true, err
	}
	defer m.legacySlots.Release(1)
	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil || !ok || !legacyCandidate(rec) {
		return err != nil, err
	}
	if rec.Activity.State == domain.ActivityExited {
		// Only a terminal this move exited and could not resume is started again.
		if launch, _ := m.legacyExited.Load(id); launch != any(rec.Metadata.RuntimeLaunchID) {
			return false, nil
		}
		_, err = m.ResumeAgentWithMode(ctx, id)
		return err != nil, err
	}
	chatMode := domain.NormalizeSessionMode(rec.Mode) == domain.SessionModeChat
	if _, down := m.legacyExited.Load(id); down && chatMode {
		// A chat this move stopped and could not start is started again at each check.
		if !m.chat.HasLiveChatController(id) {
			if _, err = m.ResumeAgentWithMode(ctx, id); err != nil {
				return true, err
			}
		}
		m.legacyExited.Delete(id)
		return false, nil
	}
	if _, managed, err := m.sessionAccount(ctx, id); err != nil || managed {
		return err != nil, err
	}
	if chatMode {
		chat, ok := m.chat.(interface {
			HibernateChatForRestart(context.Context, domain.SessionID) (bool, error)
			WakeChat(context.Context, domain.SessionID) error
		})
		if !ok || !m.chat.HasLiveChatController(id) {
			return true, nil
		}
		if stopped, err := m.stopLegacySession(ctx, id, chat.HibernateChatForRestart); err != nil || !stopped {
			return true, err
		}
		if err := chat.WakeChat(ctx, id); err != nil {
			m.legacyExited.Store(id, true) // its start is tried again at each check
			return true, err
		}
		_, managed, err := m.sessionAccount(ctx, id)
		return !managed, err
	}
	if rec.Activity.State != domain.ActivityIdle || rec.Metadata.RuntimeLaunchID == "" ||
		rec.Metadata.AgentSessionID == "" || m.terminalOnScreen(rec) {
		return true, nil
	}
	if _, release := m.beginTerminalInputDrain(rec); release != nil {
		defer release() // keystrokes stay closed until the agent has resumed
	}
	exit := func(ctx context.Context, _ domain.SessionID) (bool, error) { return m.exitLegacyTerminal(ctx, rec) }
	if exited, err := m.stopLegacySession(ctx, id, exit); err != nil || !exited {
		return true, err
	}
	_, err = m.ResumeAgentWithMode(ctx, id)
	return err != nil, err
}

// exitLegacyTerminal gives a terminal its account and exits its agent, unless a
// second reading, taken once input is closed, finds that a turn has started.
func (m *Manager) exitLegacyTerminal(ctx context.Context, rec domain.SessionRecord) (bool, error) {
	if err := sleepContext(ctx, m.interfaceTransition.idleSettle); err != nil {
		return false, err
	}
	current, err := m.getRecord(ctx, rec.ID)
	if err != nil || current.IsTerminated || current.Activity.State != domain.ActivityIdle ||
		current.Metadata.RuntimeLaunchID != rec.Metadata.RuntimeLaunchID {
		return false, err
	}
	if _, err = m.accounts.AdoptSession(ctx, rec.ID, rec.Harness); err != nil {
		return false, err
	}
	if err = m.stopAgentController(ctx, current); err == nil {
		err = m.recordAgentExited(ctx, current)
	}
	if err != nil {
		_ = m.forgetAccount(context.WithoutCancel(ctx), rec.ID) // its agent keeps its own sign-in
		return false, err
	}
	m.legacyExited.Store(rec.ID, current.Metadata.RuntimeLaunchID)
	return true, nil
}

func (m *Manager) terminalOnScreen(rec domain.SessionRecord) bool {
	m.terminalInputGateMu.Lock()
	defer m.terminalInputGateMu.Unlock()
	return m.terminalInputGate != nil && m.terminalInputGate.TerminalOnScreen(rec.Metadata.RuntimeHandleID)
}

// stopLegacySession runs stop while nothing else may operate on the session.
func (m *Manager) stopLegacySession(ctx context.Context, id domain.SessionID, stop func(context.Context, domain.SessionID) (bool, error)) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := m.beginAgentOperation(ctx, id, agentOperationHibernate); err != nil {
		if errors.Is(err, errAgentOperationInProgress) {
			return false, nil
		}
		return false, err
	}
	defer m.endAgentOperation(id, agentOperationHibernate)
	if active, err := m.hasActiveInterfaceTransition(ctx, id); err != nil || active {
		return false, err
	}
	if switching, err := m.agentSwitching(ctx, id); err != nil || switching {
		return false, err
	}
	return stop(ctx, id)
}
