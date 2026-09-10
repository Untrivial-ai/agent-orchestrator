package codexops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestExclusiveAdmissionClosesSharedLaneBeforeDrain(t *testing.T) {
	gate := NewGate()
	releaseShared, err := gate.AcquireShared(context.Background())
	if err != nil {
		t.Fatalf("AcquireShared: %v", err)
	}

	type result struct {
		lease ports.CodexOperationLease
		err   error
	}
	acquired := make(chan result, 1)
	go func() {
		lease, acquireErr := gate.AcquireExclusive(context.Background())
		acquired <- result{lease: lease, err: acquireErr}
	}()

	deadline := time.Now().Add(time.Second)
	for !gate.ExclusivePendingOrHeld() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !gate.ExclusivePendingOrHeld() {
		t.Fatal("exclusive intent was not published")
	}
	if _, err := gate.AcquireShared(context.Background()); !errors.Is(err, ports.ErrCodexAccountSwitchInProgress) {
		t.Fatalf("late AcquireShared error = %v, want switch in progress", err)
	}
	select {
	case got := <-acquired:
		got.lease.Release()
		t.Fatal("exclusive acquired before admitted shared lease drained")
	default:
	}

	releaseShared()
	select {
	case got := <-acquired:
		if got.err != nil {
			t.Fatalf("AcquireExclusive: %v", got.err)
		}
		got.lease.Release()
	case <-time.After(time.Second):
		t.Fatal("exclusive did not acquire after shared lease release")
	}
}

func TestRetainedExclusiveLeaseBlocksUntilExplicitRelease(t *testing.T) {
	gate := NewGate()
	lease, err := gate.AcquireExclusive(context.Background())
	if err != nil {
		t.Fatalf("AcquireExclusive: %v", err)
	}

	if _, err := gate.AcquireShared(context.Background()); !errors.Is(err, ports.ErrCodexAccountSwitchInProgress) {
		t.Fatalf("AcquireShared error = %v, want switch in progress", err)
	}
	if _, err := gate.AcquireExclusive(context.Background()); !errors.Is(err, ports.ErrCodexAccountSwitchInProgress) {
		t.Fatalf("second AcquireExclusive error = %v, want switch in progress", err)
	}

	lease.Release()
	lease.Release()
	releaseShared, err := gate.AcquireShared(context.Background())
	if err != nil {
		t.Fatalf("AcquireShared after release: %v", err)
	}
	releaseShared()
}
