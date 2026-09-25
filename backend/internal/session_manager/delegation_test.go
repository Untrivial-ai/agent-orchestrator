package sessionmanager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type delegationSpawnStore struct {
	*fakeStore
	key         string
	fingerprint domain.TaskDelegationRequestFingerprint
	workerID    domain.SessionID
	startup     domain.TaskDelegationStartupState
}

func (s *delegationSpawnStore) CreateTaskDelegationSession(ctx context.Context, seed domain.SessionRecord, key string, fingerprint domain.TaskDelegationRequestFingerprint) (domain.SessionRecord, bool, error) {
	if s.workerID != "" {
		if s.key != key || s.fingerprint != fingerprint {
			return domain.SessionRecord{}, false, domain.ErrTaskDelegationIdempotencyConflict
		}
		return s.sessions[s.workerID], false, nil
	}
	record, err := s.CreateSession(ctx, seed)
	if err == nil {
		s.key, s.fingerprint, s.workerID = key, fingerprint, record.ID
		s.startup = domain.TaskDelegationStartupSeeded
	}
	return record, err == nil, err
}

func (s *delegationSpawnStore) ClaimTaskDelegationStartup(_ context.Context, key string, fingerprint domain.TaskDelegationRequestFingerprint, id domain.SessionID) (bool, error) {
	if key != s.key || fingerprint != s.fingerprint || id != s.workerID {
		return false, domain.ErrTaskDelegationIdempotencyConflict
	}
	if s.startup == domain.TaskDelegationStartupReady {
		return false, nil
	}
	if s.startup != domain.TaskDelegationStartupSeeded {
		return false, domain.ErrTaskDelegationRecoveryRequired
	}
	s.startup = domain.TaskDelegationStartupStarting
	return true, nil
}

func (s *delegationSpawnStore) CompleteTaskDelegation(_ context.Context, key string, fingerprint domain.TaskDelegationRequestFingerprint, id domain.SessionID, _ time.Time) (domain.TaskDelegation, error) {
	s.startup = domain.TaskDelegationStartupReady
	return domain.TaskDelegation{IdempotencyKey: key, RequestFingerprint: fingerprint, WorkerID: id,
		State: domain.TaskDelegationCompleted, StartupState: s.startup}, nil
}

func (s *delegationSpawnStore) TaskDelegationStartupForWorker(_ context.Context, id domain.SessionID) (domain.TaskDelegationStartupState, error) {
	if id == s.workerID {
		return s.startup, nil
	}
	return "", nil
}

func TestSpawnDelegationReplayDoesNotRecreateRuntime(t *testing.T) {
	m, store, runtime, _ := newManager()
	reserved := &delegationSpawnStore{fakeStore: store}
	m.store = reserved
	config := ports.SpawnConfig{
		ProjectID: "mer", Kind: domain.KindWorker,
		TaskDelegationKey:         "delegation-retry",
		TaskDelegationFingerprint: domain.NewTaskDelegationRequestFingerprint([]byte("task")),
	}
	first, _, _, err := m.Spawn(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	// A new manager has no in-memory knowledge of the completed spawn.
	restarted, _, _, _ := newManager()
	restarted.store, restarted.runtime = reserved, runtime
	replayed, _, _, err := restarted.Spawn(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != first.ID || runtime.created != 1 || len(store.sessions) != 1 {
		t.Fatalf("replay=%s first=%s runtimes=%d sessions=%d", replayed.ID, first.ID, runtime.created, len(store.sessions))
	}
}

func TestSpawnDelegationRecoversOnlyUnclaimedSeed(t *testing.T) {
	for _, starting := range []bool{false, true} {
		t.Run(map[bool]string{false: "before effects", true: "uncertain effects"}[starting], func(t *testing.T) {
			m, store, runtime, _ := newManager()
			reserved := &delegationSpawnStore{fakeStore: store}
			m.store = reserved
			cfg := ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker,
				TaskDelegationKey: "interrupted", TaskDelegationFingerprint: domain.NewTaskDelegationRequestFingerprint([]byte("task"))}
			seed, _, err := reserved.CreateTaskDelegationSession(context.Background(), seedRecord(cfg, store.projects["mer"].Config, m.clock()), cfg.TaskDelegationKey, cfg.TaskDelegationFingerprint)
			if err != nil {
				t.Fatal(err)
			}
			if starting {
				reserved.startup = domain.TaskDelegationStartupStarting
			}
			if err := m.reconcileLive(context.Background(), seed); !errors.Is(err, domain.ErrTaskDelegationRecoveryRequired) || len(store.sessions) != 1 {
				t.Fatalf("reconciliation lost startup fence: err=%v sessions=%d", err, len(store.sessions))
			}
			got, _, _, err := m.Spawn(context.Background(), cfg)
			if starting {
				if !errors.Is(err, domain.ErrTaskDelegationRecoveryRequired) || runtime.created != 0 {
					t.Fatalf("uncertain startup: err=%v runtimes=%d", err, runtime.created)
				}
			} else if err != nil || got.ID != seed.ID || runtime.created != 1 || reserved.startup != domain.TaskDelegationStartupReady {
				t.Fatalf("seed recovery: id=%s err=%v runtimes=%d phase=%s", got.ID, err, runtime.created, reserved.startup)
			}
			if !starting {
				if err := m.reconcileLive(context.Background(), seed); err != nil || runtime.created != 1 || store.sessions[seed.ID].IsTerminated {
					t.Fatalf("stale seed snapshot changed completed startup: err=%v runtimes=%d", err, runtime.created)
				}
			}
			if len(store.sessions) != 1 {
				t.Fatalf("sessions=%d", len(store.sessions))
			}
		})
	}
}

func TestSpawnDelegationChatMarksStartupReady(t *testing.T) {
	launcher := &recordingLauncher{}
	m, store, runtime := newChatManager(launcher)
	reserved := &delegationSpawnStore{fakeStore: store}
	m.store = reserved
	cfg := ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, RequestedMode: domain.SessionModeChat,
		TaskDelegationKey: "chat", TaskDelegationFingerprint: domain.NewTaskDelegationRequestFingerprint([]byte("task"))}
	if _, _, _, err := m.Spawn(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if reserved.startup != domain.TaskDelegationStartupReady || len(launcher.started) != 1 || runtime.created != 0 {
		t.Fatalf("phase=%s chat=%d runtimes=%d", reserved.startup, len(launcher.started), runtime.created)
	}
}
