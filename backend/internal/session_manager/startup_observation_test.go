package sessionmanager

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

type pausedRuntimeObservationStore struct {
	*sqlite.Store
	read    chan struct{}
	release chan struct{}
}

func (s *pausedRuntimeObservationStore) UpdateSession(ctx context.Context, rec domain.SessionRecord) error {
	close(s.read)
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.Store.UpdateSession(ctx, rec)
}

func TestRuntimeObservationPreservesFinishedStartup(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		name := "before_completion"
		if concurrent {
			name = "read_before_write_after_completion"
		}
		t.Run(name, func(t *testing.T) {
			m, s, _, _ := newDurableStartupManager(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			seed := seedRecord(ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, RequestedMode: domain.SessionModeTUI, Harness: domain.HarnessAider}, testRoleAgents(), time.Now())
			seed.Metadata.RuntimeHandleID = "owned-handle"
			seed.Metadata.RuntimeLaunchID = "owned-launch"
			seed.Metadata.Startup = &domain.SessionStartup{ID: "operation", Stage: "launch_commit", RuntimePossible: true, Committed: true}
			rec, err := s.CreateSession(ctx, seed)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.beginAgentOperation(ctx, rec.ID, agentOperationSpawn); err != nil {
				t.Fatal(err)
			}
			defer m.endAgentOperation(rec.ID, agentOperationSpawn)
			attempt := startupAttemptFromRecord(rec)
			paused := &pausedRuntimeObservationStore{Store: s, read: make(chan struct{}), release: make(chan struct{})}
			lc := lifecycle.New(paused, nil)
			lc.SetSessionOperationGate(m)
			done := make(chan error, 1)
			go func() {
				done <- lc.ApplyRuntimeObservation(ctx, rec.ID, ports.RuntimeFacts{Runtime: ports.ProbeAlive, Workload: ports.ProbeDead, LaunchID: "owned-launch", ObservedAt: time.Now()})
			}()
			select {
			case <-paused.read:
			case <-ctx.Done():
				t.Fatal("runtime observation did not reach session write")
			}
			if !concurrent {
				close(paused.release)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
			if err := m.finishStartup(ctx, attempt); err != nil {
				t.Fatal(err)
			}
			cleared, _, err := s.GetSession(ctx, rec.ID)
			if err != nil || cleared.Metadata.Startup != nil {
				t.Fatalf("finish did not clear journal: %+v %v", cleared.Metadata.Startup, err)
			}
			if concurrent {
				close(paused.release)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
			after, _, err := s.GetSession(ctx, rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Metadata.Startup != nil {
				t.Fatalf("runtime observation resurrected completed startup: %+v", after.Metadata.Startup)
			}
			if after.Activity.State != domain.ActivityExited || after.IsTerminated || after.Metadata.RuntimeLaunchID != rec.Metadata.RuntimeLaunchID {
				t.Fatalf("runtime observation lost workload exit or changed ownership: %+v", after)
			}
		})
	}
}
