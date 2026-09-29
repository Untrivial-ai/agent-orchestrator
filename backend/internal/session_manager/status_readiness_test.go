package sessionmanager

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func configureFastRecoveryRetries(m *Manager) {
	m.statusRecoveryRetryInitial = time.Millisecond
	m.statusRecoveryRetryMax = 2 * time.Millisecond
	m.statusRecoveryRetryAttempts = 3
}

type retryableAliveRuntime struct {
	*fakeRuntime
	failing atomic.Bool
	calls   atomic.Int32
}

func (r *retryableAliveRuntime) IsAlive(_ context.Context, _ ports.RuntimeHandle) (bool, error) {
	r.calls.Add(1)
	if r.failing.Load() {
		return false, errors.New("runtime probe unavailable")
	}
	return true, nil
}

func TestStatusReadinessPersistentRecoveryStopsAfterBoundedRetries(t *testing.T) {
	m, st, rt, _ := newManager()
	configureFastRecoveryRetries(m)
	rec := domain.SessionRecord{ID: "s1", ProjectID: "mer", Harness: domain.HarnessClaudeCode,
		Activity: domain.Activity{State: domain.ActivityActive},
		Metadata: domain.SessionMetadata{Branch: "ao/s1", WorkspacePath: "/wt/s1", RuntimeHandleID: "s1"}}
	st.sessions[rec.ID] = rec
	retryRuntime := &retryableAliveRuntime{fakeRuntime: rt}
	retryRuntime.failing.Store(true)
	m.runtime = retryRuntime

	if err := m.ReconcileBackground(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !waitForStatusReadiness(t, m, rec, "unavailable") {
		t.Fatal("persistent recovery failure never became actionable")
	}
	if got, want := retryRuntime.calls.Load(), int32(1+m.statusRecoveryRetryAttempts); got != want {
		t.Fatalf("runtime probe calls = %d, want bounded initial plus retries = %d", got, want)
	}
}

func TestPermanentRecoveryDependenciesAreImmediatelyActionable(t *testing.T) {
	for _, err := range []error{
		ports.ErrAgentAuthRequired,
		ports.ErrAgentBinaryNotFound,
		ports.ErrChatDriverUnavailable,
		ports.ErrChatDriverIncompatible,
		ports.ErrChatAuthRequired,
	} {
		if !isUnrecoverableStartupRecoveryError(err) {
			t.Errorf("isUnrecoverableStartupRecoveryError(%v) = false, want true", err)
		}
	}
	if isUnrecoverableStartupRecoveryError(errors.New("transient pre-launch failure")) {
		t.Fatal("untyped pre-launch error must retain a bounded transient retry window")
	}
}

func TestStatusReadinessWaitsForRecoveryAndRetriesAutomatically(t *testing.T) {
	m, st, rt, _ := newManager()
	configureFastRecoveryRetries(m)
	rec := domain.SessionRecord{ID: "s1", ProjectID: "mer", Harness: domain.HarnessClaudeCode,
		Activity: domain.Activity{State: domain.ActivityActive, LastActivityAt: time.Unix(100, 0)},
		Metadata: domain.SessionMetadata{Branch: "ao/s1", WorkspacePath: "/wt/s1", RuntimeHandleID: "s1"}}
	st.sessions[rec.ID] = rec
	if got := m.SessionStatusReadiness(rec); got != "checking" {
		t.Fatalf("before recovery = %s", got)
	}
	retryRuntime := &retryableAliveRuntime{fakeRuntime: rt}
	retryRuntime.failing.Store(true)
	m.runtime = retryRuntime
	if err := m.ReconcileBackground(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := m.SessionStatusReadiness(rec); got != "checking" {
		t.Fatalf("failed probe = %s, want neutral checking while recovery retries", got)
	}
	if st.sessions[rec.ID].Activity != rec.Activity {
		t.Fatal("failed probe changed activity")
	}
	retryRuntime.failing.Store(false)
	if !waitForStatusReadiness(t, m, st.sessions[rec.ID], "ready") {
		t.Fatal("automatic liveness retry did not recover the session")
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
	rec := domain.SessionRecord{ID: "s1", ProjectID: "mer", Harness: domain.HarnessClaudeCode,
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
	calls   atomic.Int32
}

func (r *deadlineAwareRuntime) IsAlive(ctx context.Context, _ ports.RuntimeHandle) (bool, error) {
	if r.calls.Add(1) == 1 {
		close(r.entered)
		<-ctx.Done()
		return false, ctx.Err()
	}
	return true, nil
}

func TestStatusReadinessDeadlineReleasesSessionForRetry(t *testing.T) {
	m, st, rt, _ := newManager()
	configureFastRecoveryRetries(m)
	rec := domain.SessionRecord{ID: "s1", ProjectID: "mer", Harness: domain.HarnessClaudeCode,
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
	if got := m.SessionStatusReadiness(rec); got != "checking" {
		t.Fatalf("deadline = %s, want checking while recovery retries", got)
	}
	if !waitForStatusReadiness(t, m, st.sessions[rec.ID], "ready") {
		t.Fatal("automatic retry after deadline did not recover the session")
	}
}

func TestStatusReadinessDiscoveryFailureDoesNotMarkSessionsUnavailable(t *testing.T) {
	m, st, _, _ := newManager()
	configureFastRecoveryRetries(m)
	st.listAllErr = errors.New("storage unavailable")
	if err := m.ReconcileBackground(context.Background()); err == nil {
		t.Fatal("expected discovery failure")
	}
	if got := m.SessionStatusReadiness(domain.SessionRecord{ID: "s1"}); got != "checking" {
		t.Fatalf("readiness = %s, want checking because a global scan failure proves no individual session is dead", got)
	}
	if !waitForStatusReadiness(t, m, domain.SessionRecord{ID: "s1"}, "unavailable") {
		t.Fatal("bounded discovery retries did not expose an actionable unavailable state")
	}
}

func waitForStatusReadiness(t *testing.T, m *Manager, rec domain.SessionRecord, want string) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := m.SessionStatusReadiness(rec); got == want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return m.SessionStatusReadiness(rec) == want
}

func TestStatusReadinessFreshSpawnAfterDiscoveryFailureIsReady(t *testing.T) {
	m, st, _, _ := newManager()
	configureFastRecoveryRetries(m)
	st.listAllErr = errors.New("storage unavailable")
	if err := m.ReconcileBackground(context.Background()); err == nil {
		t.Fatal("expected discovery failure")
	}
	if !waitForStatusReadiness(t, m, domain.SessionRecord{ID: "old"}, "unavailable") {
		t.Fatal("discovery retries did not finish")
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
