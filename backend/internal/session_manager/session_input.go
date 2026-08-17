package sessionmanager

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/sessionguard"
)

type agentOperationKind string

const (
	agentOperationSwitch  agentOperationKind = "switch"
	agentOperationResume  agentOperationKind = "resume"
	agentOperationKill    agentOperationKind = "kill"
	agentOperationRestore agentOperationKind = "restore"
	agentOperationRetire  agentOperationKind = "retire"
)

var errAgentOperationInProgress = errors.New("session: another exclusive operation is in progress")

var _ sessionguard.InputLease = (*Manager)(nil)

// AcquireSessionInput atomically admits one pane write unless an exclusive
// operation already owns the AO session. The returned release is idempotent;
// callers hold it through the underlying pane write so a later mutation can
// close admission and wait for every already-admitted write to finish.
func (m *Manager) AcquireSessionInput(id domain.SessionID) (release func(), ok bool) {
	id = domain.SessionID(strings.TrimSpace(string(id)))
	m.agentOpMu.Lock()
	if m.agentOperationActiveLocked(id) && !m.agentSwitchDecisionInputAllowedLocked(id) {
		m.agentOpMu.Unlock()
		return nil, false
	}
	if m.inputLeases[id] == 0 {
		m.inputDrained[id] = make(chan struct{})
	}
	m.inputLeases[id]++
	m.agentOpMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() { m.releaseSessionInput(id) })
	}, true
}

func (m *Manager) releaseSessionInput(id domain.SessionID) {
	m.agentOpMu.Lock()
	defer m.agentOpMu.Unlock()
	count := m.inputLeases[id]
	if count <= 1 {
		delete(m.inputLeases, id)
		if drained := m.inputDrained[id]; drained != nil {
			close(drained)
			delete(m.inputDrained, id)
		}
		return
	}
	m.inputLeases[id] = count - 1
}

// SessionMutationInProgress is consumed by observation-driven lifecycle paths
// that must not independently terminate a session while AO is replacing or
// relaunching its provider process.
func (m *Manager) SessionMutationInProgress(id domain.SessionID) bool {
	id = domain.SessionID(strings.TrimSpace(string(id)))
	m.agentOpMu.Lock()
	defer m.agentOpMu.Unlock()
	return m.agentOperationActiveLocked(id)
}

func (m *Manager) agentOperationActiveLocked(id domain.SessionID) bool {
	_, ok := m.agentOperations[id]
	return ok
}

func (m *Manager) agentSwitchDecisionInputAllowedLocked(id domain.SessionID) bool {
	switching := m.agentOperations[id] == agentOperationSwitch
	_, allowed := m.switchDecisionInput[id]
	return switching && allowed
}

// beginAgentOperation closes input admission before waiting for already-issued
// leases. Because both actions share agentOpMu, a pane write is either fully
// admitted before the operation (and drained) or rejected after it; there is
// no interval in which it can pass a boolean check and write into the new
// provider generation.
func (m *Manager) beginAgentOperation(ctx context.Context, id domain.SessionID, kind agentOperationKind) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.agentOpMu.Lock()
	if m.agentOperationActiveLocked(id) {
		m.agentOpMu.Unlock()
		return errAgentOperationInProgress
	}
	m.agentOperations[id] = kind
	drained := m.inputDrained[id]
	m.agentOpMu.Unlock()

	if drained == nil {
		return nil
	}
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		m.endAgentOperation(id, kind)
		return ctx.Err()
	}
}

func (m *Manager) endAgentOperation(id domain.SessionID, kind agentOperationKind) {
	m.agentOpMu.Lock()
	defer m.agentOpMu.Unlock()
	if kind == agentOperationSwitch {
		delete(m.retainedSwitches, id)
		delete(m.switchDecisionInput, id)
	}
	if current, ok := m.agentOperations[id]; ok && current == kind {
		delete(m.agentOperations, id)
	}
}

func (m *Manager) allowAgentSwitchDecisionInput(id domain.SessionID, switchID domain.AgentSwitchID) {
	m.agentOpMu.Lock()
	defer m.agentOpMu.Unlock()
	if m.agentOperations[id] != agentOperationSwitch {
		return
	}
	if m.switchDecisionInput == nil {
		m.switchDecisionInput = make(map[domain.SessionID]domain.AgentSwitchID)
	}
	m.switchDecisionInput[id] = switchID
}

// closeAgentSwitchDecisionInput closes the temporary permission lane and waits
// for an already-admitted keystroke write to finish before source teardown.
func (m *Manager) closeAgentSwitchDecisionInput(ctx context.Context, id domain.SessionID, switchID domain.AgentSwitchID) error {
	m.agentOpMu.Lock()
	if current, ok := m.switchDecisionInput[id]; !ok || current != switchID {
		m.agentOpMu.Unlock()
		return nil
	}
	delete(m.switchDecisionInput, id)
	drained := m.inputDrained[id]
	m.agentOpMu.Unlock()
	if drained == nil {
		return nil
	}
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) beginAgentSwitch(ctx context.Context, id domain.SessionID) error {
	if err := m.beginAgentOperation(ctx, id, agentOperationSwitch); err != nil {
		if errors.Is(err, errAgentOperationInProgress) {
			return ErrSwitchInProgress
		}
		return err
	}
	return nil
}

func (m *Manager) endAgentSwitch(id domain.SessionID) {
	m.endAgentOperation(id, agentOperationSwitch)
}

// retainAgentSwitch keeps the already-owned switch/input gate closed after an
// ambiguous external side effect. It also marks the gate as reclaimable by a
// later recovery pass; ordinary user switches cannot reuse it.
func (m *Manager) retainAgentSwitch(id domain.SessionID) {
	id = domain.SessionID(strings.TrimSpace(string(id)))
	m.agentOpMu.Lock()
	defer m.agentOpMu.Unlock()
	if m.agentOperations[id] != agentOperationSwitch {
		return
	}
	if m.retainedSwitches == nil {
		m.retainedSwitches = make(map[domain.SessionID]struct{})
	}
	m.retainedSwitches[id] = struct{}{}
}

func (m *Manager) agentSwitchRetained(id domain.SessionID) bool {
	id = domain.SessionID(strings.TrimSpace(string(id)))
	m.agentOpMu.Lock()
	defer m.agentOpMu.Unlock()
	_, retained := m.retainedSwitches[id]
	return retained
}

// beginAgentSwitchRecovery acquires the switch gate on daemon boot, or
// reclaims a gate retained by an earlier inconclusive recovery attempt. It
// never re-enters a switch that is still executing normally.
func (m *Manager) beginAgentSwitchRecovery(ctx context.Context, id domain.SessionID) error {
	id = domain.SessionID(strings.TrimSpace(string(id)))
	if err := ctx.Err(); err != nil {
		return err
	}
	m.agentOpMu.Lock()
	if current, active := m.agentOperations[id]; active {
		if current == agentOperationSwitch {
			if _, retained := m.retainedSwitches[id]; retained {
				delete(m.retainedSwitches, id)
				m.agentOpMu.Unlock()
				return nil
			}
		}
		m.agentOpMu.Unlock()
		return ErrSwitchInProgress
	}
	if m.agentOperations == nil {
		m.agentOperations = make(map[domain.SessionID]agentOperationKind)
	}
	m.agentOperations[id] = agentOperationSwitch
	drained := m.inputDrained[id]
	m.agentOpMu.Unlock()

	if drained == nil {
		return nil
	}
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		m.endAgentSwitch(id)
		return ctx.Err()
	}
}

// releaseRetainedAgentSwitch closes a stale in-memory fence only when it was
// explicitly retained for recovery and no durable active saga remains.
func (m *Manager) releaseRetainedAgentSwitch(id domain.SessionID) {
	id = domain.SessionID(strings.TrimSpace(string(id)))
	m.agentOpMu.Lock()
	defer m.agentOpMu.Unlock()
	if _, retained := m.retainedSwitches[id]; !retained {
		return
	}
	delete(m.retainedSwitches, id)
	delete(m.agentOperations, id)
}

func (m *Manager) beginAgentResume(ctx context.Context, id domain.SessionID) error {
	if err := m.beginAgentOperation(ctx, id, agentOperationResume); err != nil {
		if errors.Is(err, errAgentOperationInProgress) {
			m.agentOpMu.Lock()
			activeOperation := m.agentOperations[id]
			m.agentOpMu.Unlock()
			if activeOperation == agentOperationSwitch {
				return ErrSwitchInProgress
			}
			return ErrResumeInProgress
		}
		return err
	}
	return nil
}

func (m *Manager) endAgentResume(id domain.SessionID) {
	m.endAgentOperation(id, agentOperationResume)
}
