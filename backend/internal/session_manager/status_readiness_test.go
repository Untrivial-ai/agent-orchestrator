package sessionmanager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestStatusReadinessWaitsForRecoveryAndAllowsRetry(t *testing.T) {
	m, st, rt, _ := newManager()
	rec := domain.SessionRecord{ID: "s1", ProjectID: "mer", Harness: domain.HarnessOpenCode,
		Activity: domain.Activity{State: domain.ActivityActive, LastActivityAt: time.Unix(100, 0)},
		Metadata: domain.SessionMetadata{Branch: "ao/s1", WorkspacePath: "/wt/s1", RuntimeHandleID: "s1"}}
	st.sessions[rec.ID] = rec
	if got := m.SessionStatusReadiness(rec); got != "checking" {
		t.Fatalf("before recovery = %s", got)
	}
	rt.aliveErr = errors.New("runtime probe unavailable")
	if err := m.ReconcileBackground(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := m.SessionStatusReadiness(rec); got != "unavailable" {
		t.Fatalf("failed probe = %s", got)
	}
	if st.sessions[rec.ID].Activity != rec.Activity {
		t.Fatal("failed probe changed activity")
	}
	rt.aliveErr = nil
	rt.aliveByHandle = map[string]bool{"s1": true}
	if _, err := m.ResumeAgentWithMode(context.Background(), rec.ID); err != nil {
		t.Fatal(err)
	}
	if got := m.SessionStatusReadiness(st.sessions[rec.ID]); got != "ready" {
		t.Fatalf("retry = %s", got)
	}
	if rt.created != 0 {
		t.Fatal("retry spawned a duplicate of a surviving runtime")
	}
}

type stubbornAliveRuntime struct {
	*fakeRuntime
	entered chan domain.SessionID
	release chan struct{}
}

func (r *stubbornAliveRuntime) IsAlive(_ context.Context, handle ports.RuntimeHandle) (bool, error) {
	r.entered <- domain.SessionID(handle.ID)
	<-r.release
	return true, nil
}

func TestStatusReadinessDoesNotOfferRetryWhileRecoveryOwnsSession(t *testing.T) {
	m, st, rt, _ := newManager()
	rec := domain.SessionRecord{ID: "s1", ProjectID: "mer", Harness: domain.HarnessOpenCode,
		Activity: domain.Activity{State: domain.ActivityActive},
		Metadata: domain.SessionMetadata{Branch: "ao/s1", WorkspacePath: "/wt/s1", RuntimeHandleID: "s1"}}
	st.sessions[rec.ID] = rec
	blocked := &stubbornAliveRuntime{fakeRuntime: rt, entered: make(chan domain.SessionID, 1), release: make(chan struct{})}
	m.runtime = blocked
	m.statusVerificationLimit = time.Nanosecond
	finished := make(chan error, 1)
	go func() { finished <- m.ReconcileBackground(context.Background()) }()
	<-blocked.entered
	time.Sleep(time.Millisecond)
	if got := m.SessionStatusReadiness(rec); got != "checking" {
		t.Fatalf("owned recovery = %s, want checking until retry can acquire the session", got)
	}
	close(blocked.release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if got := m.SessionStatusReadiness(rec); got != "ready" {
		t.Fatalf("late recovery = %s", got)
	}
	if st.sessions[rec.ID].Activity != rec.Activity {
		t.Fatal("recovery invented activity")
	}
}

type deadlineAwareRuntime struct {
	*fakeRuntime
	entered chan struct{}
}

func (r *deadlineAwareRuntime) IsAlive(ctx context.Context, _ ports.RuntimeHandle) (bool, error) {
	close(r.entered)
	<-ctx.Done()
	return false, ctx.Err()
}

func TestStatusReadinessDeadlineReleasesSessionForRetry(t *testing.T) {
	m, st, rt, _ := newManager()
	rec := domain.SessionRecord{ID: "s1", ProjectID: "mer", Harness: domain.HarnessOpenCode,
		Activity: domain.Activity{State: domain.ActivityActive},
		Metadata: domain.SessionMetadata{Branch: "ao/s1", WorkspacePath: "/wt/s1", RuntimeHandleID: "s1"}}
	st.sessions[rec.ID] = rec
	deadlineRuntime := &deadlineAwareRuntime{fakeRuntime: rt, entered: make(chan struct{})}
	m.runtime = deadlineRuntime
	m.statusVerificationLimit = 10 * time.Millisecond
	finished := make(chan error, 1)
	go func() { finished <- m.ReconcileBackground(context.Background()) }()
	<-deadlineRuntime.entered
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if got := m.SessionStatusReadiness(rec); got != "unavailable" {
		t.Fatalf("deadline = %s, want unavailable", got)
	}
	rt.aliveByHandle = map[string]bool{"s1": true}
	m.runtime = rt
	if _, err := m.ResumeAgentWithMode(context.Background(), rec.ID); err != nil {
		t.Fatalf("retry after deadline: %v", err)
	}
	if got := m.SessionStatusReadiness(st.sessions[rec.ID]); got != "ready" {
		t.Fatalf("retry = %s, want ready", got)
	}
}

func TestStatusReadinessDiscoveryFailureIsUnavailable(t *testing.T) {
	m, st, _, _ := newManager()
	st.listAllErr = errors.New("storage unavailable")
	if err := m.ReconcileBackground(context.Background()); err == nil {
		t.Fatal("expected discovery failure")
	}
	if got := m.SessionStatusReadiness(domain.SessionRecord{ID: "s1"}); got != "unavailable" {
		t.Fatalf("readiness = %s", got)
	}
}

func TestStatusReadinessFreshSpawnAfterDiscoveryFailureIsReady(t *testing.T) {
	m, st, _, _ := newManager()
	st.listAllErr = errors.New("storage unavailable")
	if err := m.ReconcileBackground(context.Background()); err == nil {
		t.Fatal("expected discovery failure")
	}
	st.listAllErr = nil
	rec, _, _, err := m.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID: "mer",
		Kind:      domain.KindWorker,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.SessionStatusReadiness(rec); got != "ready" {
		t.Fatalf("fresh spawn readiness = %s, want ready", got)
	}
}

func TestStatusReadinessFreshSpawnAfterSuccessfulRecoveryDoesNotChangeRevision(t *testing.T) {
	m, _, _, _ := newManager()
	if err := m.ReconcileBackground(context.Background()); err != nil {
		t.Fatal(err)
	}
	revision := m.StatusRecoveryRevision()
	rec, _, _, err := m.Spawn(context.Background(), ports.SpawnConfig{
		ProjectID: "mer",
		Kind:      domain.KindWorker,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.StatusRecoveryRevision(); got != revision {
		t.Fatalf("fresh spawn recovery revision = %d, want %d", got, revision)
	}
	if got := m.SessionStatusReadiness(rec); got != "ready" {
		t.Fatalf("fresh spawn readiness = %s, want ready", got)
	}
}
