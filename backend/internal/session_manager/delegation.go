package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type delegationStartupStore interface {
	CreateTaskDelegationSession(context.Context, domain.SessionRecord, string, domain.TaskDelegationRequestFingerprint) (domain.SessionRecord, bool, error)
	ClaimTaskDelegationStartup(context.Context, string, domain.TaskDelegationRequestFingerprint, domain.SessionID) (bool, error)
	CompleteTaskDelegation(context.Context, string, domain.TaskDelegationRequestFingerprint, domain.SessionID, time.Time) (domain.TaskDelegation, error)
}

func (m *Manager) createDelegationSeed(ctx context.Context, cfg ports.SpawnConfig, seed domain.SessionRecord) (domain.SessionRecord, bool, error) {
	store, ok := m.store.(delegationStartupStore)
	if !ok {
		return domain.SessionRecord{}, false, errors.New("spawn: task reservation storage is unavailable")
	}
	rec, _, err := store.CreateTaskDelegationSession(ctx, seed, cfg.TaskDelegationKey, cfg.TaskDelegationFingerprint)
	if err != nil {
		return rec, false, err
	}
	claimed, err := store.ClaimTaskDelegationStartup(ctx, cfg.TaskDelegationKey, cfg.TaskDelegationFingerprint, rec.ID)
	return rec, claimed, err
}

func (m *Manager) completeDelegationStartup(ctx context.Context, cfg ports.SpawnConfig, rec domain.SessionRecord) error {
	if cfg.TaskDelegationKey == "" {
		return nil
	}
	store, ok := m.store.(delegationStartupStore)
	if !ok {
		return errors.New("spawn: task reservation storage is unavailable")
	}
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	completed, err := store.CompleteTaskDelegation(commitCtx, cfg.TaskDelegationKey, cfg.TaskDelegationFingerprint, rec.ID, m.clock())
	if err != nil {
		return fmt.Errorf("commit task startup: %w", err)
	}
	if !completed.Ready() || completed.WorkerID != rec.ID {
		return errors.New("commit task startup: unexpected worker outcome")
	}
	return nil
}
