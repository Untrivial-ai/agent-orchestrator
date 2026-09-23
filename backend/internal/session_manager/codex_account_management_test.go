package sessionmanager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type waitingControllerGate struct {
	entered chan struct{}
	release chan struct{}
}

func (*waitingControllerGate) AcquireShared(context.Context) (func(), error) {
	return nil, errors.New("fail-fast shared admission must not be used")
}

func (g *waitingControllerGate) AcquireSharedWait(ctx context.Context) (func(), error) {
	close(g.entered)
	select {
	case <-g.release:
		return func() {}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (*waitingControllerGate) AcquireExclusive(context.Context) (ports.CodexOperationLease, error) {
	return nil, errors.New("unexpected exclusive admission")
}

func (*waitingControllerGate) ExclusivePendingOrHeld() bool { return true }

func TestCodexControllerAdmissionWaitsOnlyForActiveDeviceGate(t *testing.T) {
	gate := &waitingControllerGate{entered: make(chan struct{}), release: make(chan struct{})}
	manager := New(Deps{CodexOperationGate: gate})

	type admissionResult struct {
		release func()
		err     error
	}
	done := make(chan admissionResult, 1)
	go func() {
		release, acquireErr := manager.acquireCodexControllerAdmission(context.Background(), domain.HarnessCodex)
		done <- admissionResult{release: release, err: acquireErr}
	}()
	<-gate.entered

	select {
	case result := <-done:
		if result.release != nil {
			result.release()
		}
		t.Fatalf("Codex controller admission completed during bootstrap: %v", result.err)
	default:
	}

	close(gate.release)
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("Codex controller admission after bootstrap: %v", result.err)
		}
		result.release()
	case <-time.After(time.Second):
		t.Fatal("Codex controller admission did not resume after bootstrap")
	}
}
