package sessionmanager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func newInputLeaseTestManager() *Manager {
	return &Manager{
		agentOperations:     make(map[domain.SessionID]agentOperationKind),
		switchDecisionInput: make(map[domain.SessionID]domain.AgentSwitchID),
		retainedSwitches:    make(map[domain.SessionID]struct{}),
		inputLeases:         make(map[domain.SessionID]int),
		inputDrained:        make(map[domain.SessionID]chan struct{}),
	}
}

func TestAgentOperationClosesAdmissionThenDrainsExistingInput(t *testing.T) {
	m := newInputLeaseTestManager()
	release, ok := m.AcquireSessionInput("worker-1")
	if !ok {
		t.Fatal("initial input lease was refused")
	}

	beginDone := make(chan error, 1)
	go func() {
		beginDone <- m.beginAgentOperation(context.Background(), "worker-1", agentOperationSwitch)
	}()

	eventuallySessionInput(t, time.Second, func() bool { return m.SessionMutationInProgress("worker-1") })
	if _, admitted := m.AcquireSessionInput("worker-1"); admitted {
		t.Fatal("new input was admitted after mutation intent registered")
	}
	select {
	case err := <-beginDone:
		t.Fatalf("operation began before existing pane write drained: %v", err)
	default:
	}

	release()
	if err := <-beginDone; err != nil {
		t.Fatalf("begin operation after drain: %v", err)
	}
	if _, admitted := m.AcquireSessionInput("worker-1"); admitted {
		t.Fatal("input admitted while operation owns the session")
	}

	m.endAgentOperation("worker-1", agentOperationSwitch)
	releaseAfter, admitted := m.AcquireSessionInput("worker-1")
	if !admitted {
		t.Fatal("input gate did not reopen after operation")
	}
	releaseAfter()
}

func TestAgentSwitchDecisionInputIsNarrowAndDrained(t *testing.T) {
	m := newInputLeaseTestManager()
	id := domain.SessionID("worker-1")
	switchID := domain.AgentSwitchID("switch-1")
	if err := m.beginAgentOperation(context.Background(), id, agentOperationSwitch); err != nil {
		t.Fatal(err)
	}
	m.allowAgentSwitchDecisionInput(id, switchID)

	release, admitted := m.AcquireSessionInput(id)
	if !admitted {
		t.Fatal("human permission input was not admitted")
	}
	closed := make(chan error, 1)
	go func() {
		closed <- m.closeAgentSwitchDecisionInput(context.Background(), id, switchID)
	}()
	select {
	case err := <-closed:
		t.Fatalf("permission lane closed before admitted input drained: %v", err)
	default:
	}
	release()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if _, admitted := m.AcquireSessionInput(id); admitted {
		t.Fatal("ordinary input remained open after permission lane closed")
	}
}

func TestAgentOperationDrainHonorsContextAndReopensAdmission(t *testing.T) {
	m := newInputLeaseTestManager()
	release, ok := m.AcquireSessionInput("worker-1")
	if !ok {
		t.Fatal("initial input lease was refused")
	}

	ctx, cancel := context.WithCancel(context.Background())
	beginDone := make(chan error, 1)
	go func() {
		beginDone <- m.beginAgentOperation(ctx, "worker-1", agentOperationRestore)
	}()
	eventuallySessionInput(t, time.Second, func() bool { return m.SessionMutationInProgress("worker-1") })
	cancel()
	if err := <-beginDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("begin operation error = %v, want context.Canceled", err)
	}
	if m.SessionMutationInProgress("worker-1") {
		t.Fatal("cancelled operation left input admission closed")
	}
	release()
}

func TestAgentOperationAndInputLeaseAreScopedPerSession(t *testing.T) {
	m := newInputLeaseTestManager()
	if err := m.beginAgentOperation(context.Background(), "worker-1", agentOperationKill); err != nil {
		t.Fatalf("begin worker-1 operation: %v", err)
	}
	defer m.endAgentOperation("worker-1", agentOperationKill)

	if _, ok := m.AcquireSessionInput("worker-1"); ok {
		t.Fatal("worker-1 input admitted during its mutation")
	}
	release, ok := m.AcquireSessionInput("worker-2")
	if !ok {
		t.Fatal("worker-2 input was incorrectly gated by worker-1 mutation")
	}
	release()
	if err := m.beginAgentOperation(context.Background(), "worker-2", agentOperationRestore); err != nil {
		t.Fatalf("worker-2 operation was incorrectly blocked: %v", err)
	}
	m.endAgentOperation("worker-2", agentOperationRestore)
}

func eventuallySessionInput(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}

func TestTerminalInputReservationIsExactAndReleasesPartialFailure(t *testing.T) {
	m := newInputLeaseTestManager()
	ctx := context.Background()
	release, err := m.ReserveTerminalInput(ctx, []string{"ptyhost-v1:review-worker", "ptyhost-v1:review-worker", "review-other"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []domain.SessionID{"ptyhost-v1:review-worker", "review-other"} {
		if r, ok := m.AcquireSessionInput(id); ok {
			r()
			t.Fatalf("reserved terminal %s admitted input", id)
		}
	}
	for _, id := range []domain.SessionID{"worker", "shell-worker", "review-unrelated"} {
		r, ok := m.AcquireSessionInput(id)
		if !ok {
			t.Fatalf("unrelated terminal %s blocked", id)
		}
		r()
	}
	if _, err := m.ReserveTerminalInput(ctx, []string{"review-new", "review-other"}); err == nil {
		t.Fatal("conflicting reservation succeeded")
	}
	if m.SessionMutationInProgress("review-new") {
		t.Fatal("failed reservation leaked partial input fence")
	}
	release()
	release()
	if m.SessionMutationInProgress("review-other") {
		t.Fatal("release retained input fence")
	}
}

func TestTerminalInputReservationCancellationDrainsWithoutLeaking(t *testing.T) {
	m := newInputLeaseTestManager()
	write, ok := m.AcquireSessionInput("review-worker")
	if !ok {
		t.Fatal("initial write refused")
	}
	defer write()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		release, err := m.ReserveTerminalInput(ctx, []string{"review-first", "review-worker"})
		if release != nil {
			release()
		}
		done <- err
	}()
	eventuallySessionInput(t, time.Second, func() bool { return m.SessionMutationInProgress("review-worker") })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("reservation error=%v", err)
	}
	for _, id := range []domain.SessionID{"review-first", "review-worker"} {
		if m.SessionMutationInProgress(id) {
			t.Fatalf("canceled reservation retained %s", id)
		}
	}
}
