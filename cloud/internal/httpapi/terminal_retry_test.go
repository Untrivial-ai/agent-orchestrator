package httpapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

// ErrWorkerSuperseded means a newer worker epoch already connected: this
// terminal's worker is gone for good, so retrying is pointless and must not
// stall the caller for terminalReadyTimeout (20s) like a transient
// ErrWorkerUnavailable does.
func TestRetryTerminalRequest_FailsFastOnSupersededWorker(t *testing.T) {
	calls := 0
	start := time.Now()
	err := retryTerminalRequest(context.Background(), func() error {
		calls++
		return postgres.ErrWorkerSuperseded
	})
	elapsed := time.Since(start)

	if !errors.Is(err, postgres.ErrWorkerSuperseded) {
		t.Fatalf("expected ErrWorkerSuperseded, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly one attempt, got %d", calls)
	}
	if elapsed >= 50*time.Millisecond {
		t.Fatalf("expected an immediate return, took %s (one retry tick is 50ms)", elapsed)
	}
}

// A transient ErrWorkerUnavailable (worker still provisioning) must keep
// retrying instead of giving up on the first failure.
func TestRetryTerminalRequest_RetriesTransientUnavailable(t *testing.T) {
	calls := 0
	err := retryTerminalRequest(context.Background(), func() error {
		calls++
		if calls < 3 {
			return postgres.ErrWorkerUnavailable
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected eventual success, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 attempts before success, got %d", calls)
	}
}

// Any other error is not retried at all, same as before this change.
func TestRetryTerminalRequest_FailsFastOnUnknownError(t *testing.T) {
	sentinel := errors.New("boom")
	calls := 0
	err := retryTerminalRequest(context.Background(), func() error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly one attempt, got %d", calls)
	}
}
