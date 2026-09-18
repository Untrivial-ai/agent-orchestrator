package sessionmanager

import (
	"context"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// A slow or unavailable provider must not leave the board loading forever.
// This is a verification deadline, not an artificial delay before publication.
const statusVerificationLimit = 30 * time.Second

type statusRecovery struct {
	pending bool
	failed  *domain.SessionRecord
}

// StatusRecoveryRevision fences API snapshots read concurrently with recovery.
func (m *Manager) StatusRecoveryRevision() uint64 {
	m.statusRecoveryMu.RLock()
	defer m.statusRecoveryMu.RUnlock()
	return m.statusRecoveryRevision
}

// SessionStatusReadiness describes this daemon's recovery observation. It is
// deliberately not durable: a new daemon must verify the sessions again.
func (m *Manager) SessionStatusReadiness(rec domain.SessionRecord) string {
	m.statusRecoveryMu.RLock()
	defer m.statusRecoveryMu.RUnlock()
	result, found := m.statusRecoveries[rec.ID]
	if found {
		// A deadline cancels the recovery context, but the dependency must return
		// before another attempt can safely own this session. Keep the UI checking
		// until the operation gate has actually been released.
		if result.pending {
			return "checking"
		}
		if result.failed != nil && !rec.IsTerminated && rec.Activity.State != domain.ActivityExited &&
			rec.ControllerOwner() == result.failed.ControllerOwner() && rec.Activity == result.failed.Activity {
			return "unavailable"
		}
		return "ready"
	}
	select {
	case <-m.startupBackgroundReconcileDone:
		if m.statusRecoveryFailed {
			return "unavailable"
		}
		return "ready"
	default:
	}
	return "checking"
}

func (m *Manager) beginStatusRecovery(id domain.SessionID) {
	m.statusRecoveryMu.Lock()
	defer m.statusRecoveryMu.Unlock()
	m.statusRecoveries[id] = statusRecovery{pending: true}
	m.statusRecoveryRevision++
}

// markFreshSessionStatusReady keeps a session created by this daemon outside
// the startup snapshot's failure state. A concurrent startup recovery remains
// authoritative once it has already claimed the same session.
func (m *Manager) markFreshSessionStatusReady(id domain.SessionID) {
	m.statusRecoveryMu.Lock()
	defer m.statusRecoveryMu.Unlock()
	select {
	case <-m.startupBackgroundReconcileDone:
		// After a successful startup, an untracked session already reads as ready;
		// avoid turning ordinary spawns into recovery-revision changes.
		if !m.statusRecoveryFailed {
			return
		}
	default:
	}
	if result, found := m.statusRecoveries[id]; found && result.pending {
		return
	}
	m.statusRecoveries[id] = statusRecovery{}
	m.statusRecoveryRevision++
}

func (m *Manager) finishStatusRecovery(ctx context.Context, before domain.SessionRecord, recoveryErr error) {
	result := statusRecovery{}
	if recoveryErr != nil {
		current, found, err := m.store.GetSession(ctx, before.ID)
		if err == nil && found {
			before = current
		}
		result.failed = &before
	}
	m.statusRecoveryMu.Lock()
	m.statusRecoveries[before.ID] = result
	m.statusRecoveryRevision++
	m.statusRecoveryMu.Unlock()
}
