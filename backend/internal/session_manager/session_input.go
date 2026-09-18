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
	agentOperationExit              agentOperationKind = "exit"
	agentOperationResume            agentOperationKind = "resume"
	agentOperationKill              agentOperationKind = "kill"
	agentOperationRestore           agentOperationKind = "restore"
	agentOperationRetire            agentOperationKind = "retire"
	agentOperationReconcile         agentOperationKind = "reconcile"
	agentOperationInterfaceRecovery agentOperationKind = "interface_recovery"
)

var errAgentOperationInProgress = errors.New("session: another exclusive operation is in progress")

// errInterfaceRecoveryShutdown is returned by beginInterfaceRecoveryWorker once
// daemon shutdown has closed worker admission. Callers preserve the deferred
// input fence so the next boot still recovers the interrupted transition.
var errInterfaceRecoveryShutdown = errors.New("session: interface recovery unavailable during shutdown")

var _ sessionguard.InputLease = (*Manager)(nil)

// AcquireSessionInput atomically admits one pane write unless an exclusive
// operation already owns the AO session. The returned release is idempotent;
// callers hold it through the underlying pane write so a later mutation can
// close admission and wait for every already-admitted write to finish.
func (m *Manager) AcquireSessionInput(id domain.SessionID) (release func(), ok bool) {
	id = domain.SessionID(strings.TrimSpace(string(id)))
	m.agentOpMu.Lock()
	if m.agentOperationActiveLocked(id) {
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
	_, deferred := m.deferredInterfaceRecovery[id]
	return ok || deferred
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

// beginAgentOperations reserves every currently-unowned session before waiting
// for any admitted input to drain. Startup reconciliation uses this batch form
// so candidates queued behind its worker limit are fenced just as early as the
// candidates already being probed.
func (m *Manager) beginAgentOperations(ctx context.Context, ids []domain.SessionID, kind agentOperationKind) ([]domain.SessionID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type reservation struct {
		id      domain.SessionID
		drained <-chan struct{}
	}
	reservations := make([]reservation, 0, len(ids))
	m.agentOpMu.Lock()
	for _, id := range ids {
		id = domain.SessionID(strings.TrimSpace(string(id)))
		if id == "" || m.agentOperationActiveLocked(id) {
			continue
		}
		m.agentOperations[id] = kind
		reservations = append(reservations, reservation{id: id, drained: m.inputDrained[id]})
	}
	m.agentOpMu.Unlock()

	for _, reservation := range reservations {
		if reservation.drained == nil {
			continue
		}
		select {
		case <-reservation.drained:
		case <-ctx.Done():
			for _, reserved := range reservations {
				m.endAgentOperation(reserved.id, kind)
			}
			return nil, ctx.Err()
		}
	}
	acquired := make([]domain.SessionID, 0, len(reservations))
	for _, reservation := range reservations {
		acquired = append(acquired, reservation.id)
	}
	return acquired, nil
}

func (m *Manager) endAgentOperation(id domain.SessionID, kind agentOperationKind) {
	m.agentOpMu.Lock()
	defer m.agentOpMu.Unlock()
	if current, ok := m.agentOperations[id]; ok && current == kind {
		delete(m.agentOperations, id)
		m.resumeDeferredInterfaceRecoveryLocked(id)
	}
}

// The deferred fence survives the foreign operation's release. Recovery, not
// that operation, is responsible for reopening input after its durable commit.
func (m *Manager) resumeDeferredInterfaceRecoveryLocked(id domain.SessionID) {
	transitionID, deferred := m.deferredInterfaceRecovery[id]
	if !deferred {
		return
	}
	if err := m.beginInterfaceRecoveryWorker(); err != nil {
		return // Preserve the fence for the next boot after shutdown admission closes.
	}
	go func() {
		defer m.interfaceRecoveryWorkers.Done()
		recovered, err := m.recoverInterfaceTransitions(m.backgroundContext, transitionID)
		if err != nil {
			m.logger.Error("interface transition: deferred recovery failed", "sessionID", id, "error", err)
		} else if len(recovered) == 0 {
			// The exact obligation was cancelled/settled before the worker read it.
			m.agentOpMu.Lock()
			if m.deferredInterfaceRecovery[id] == transitionID {
				delete(m.deferredInterfaceRecovery, id)
			}
			m.agentOpMu.Unlock()
		}
	}()
}

// beginInterfaceRecoveryWorker admits one deferred interface-transition
// recovery goroutine. Admission closes during daemon shutdown so a refused
// recovery leaves its fence intact for the next boot.
func (m *Manager) beginInterfaceRecoveryWorker() error {
	m.interfaceRecoveryWorkerMu.Lock()
	defer m.interfaceRecoveryWorkerMu.Unlock()
	if m.interfaceRecoveryWorkersClosed {
		return errInterfaceRecoveryShutdown
	}
	m.interfaceRecoveryWorkers.Add(1)
	return nil
}

// WaitInterfaceRecoveryWorkers closes worker admission and waits for every
// in-flight deferred interface-recovery goroutine to finish.
func (m *Manager) WaitInterfaceRecoveryWorkers(ctx context.Context) error {
	m.interfaceRecoveryWorkerMu.Lock()
	m.interfaceRecoveryWorkersClosed = true
	m.interfaceRecoveryWorkerMu.Unlock()

	done := make(chan struct{})
	go func() {
		m.interfaceRecoveryWorkers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) beginAgentResume(ctx context.Context, id domain.SessionID) error {
	if err := m.beginAgentOperation(ctx, id, agentOperationResume); err != nil {
		if errors.Is(err, errAgentOperationInProgress) {
			return ErrResumeInProgress
		}
		return err
	}
	return nil
}

func (m *Manager) endAgentResume(id domain.SessionID) {
	m.endAgentOperation(id, agentOperationResume)
}

// conversationFactBytes bounds a single durable user-prompt fact. Larger
// prompts are truncated so the fact rows stay within the SQLite value limit
// (and out of per-row metadata bloat).
const conversationFactBytes = 16 << 10

func boundedConversationFact(value string) string {
	return boundedString(strings.TrimSpace(value), conversationFactBytes)
}

func boundedString(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	return strings.ToValidUTF8(value[:maxBytes], "�")
}