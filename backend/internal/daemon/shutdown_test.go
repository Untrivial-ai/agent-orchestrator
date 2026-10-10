package daemon

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestShutdownStepCompletionAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "completed"},
		{name: "failed", err: errors.New("worker failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			log := slog.New(slog.NewTextHandler(&logs, nil))
			shutdownStep(time.Second, log, "startup reconciliation", func(ctx context.Context) error {
				if _, ok := ctx.Deadline(); !ok {
					return errors.New("missing shutdown deadline")
				}
				return tc.err
			})
			if tc.err == nil && logs.Len() != 0 {
				t.Fatalf("successful shutdown logged an error: %s", logs.String())
			}
			if tc.err != nil && (!strings.Contains(logs.String(), "startup reconciliation shutdown") || !strings.Contains(logs.String(), tc.err.Error())) {
				t.Fatalf("shutdown error omitted phase or cause: %s", logs.String())
			}
		})
	}
}

func TestShutdownStepBoundsUnresponsiveWorker(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	release, exited := make(chan struct{}), make(chan struct{})
	defer func() {
		close(release)
		<-exited
	}()
	shutdownStep(20*time.Millisecond, log, "startup reconciliation", func(ctx context.Context) error {
		defer close(exited)
		<-ctx.Done()
		// A blocked worker must not prevent the remaining resource defers.
		<-release
		return nil
	})
	if !strings.Contains(logs.String(), "startup reconciliation shutdown") || !strings.Contains(logs.String(), context.DeadlineExceeded.Error()) {
		t.Fatalf("shutdown timeout omitted phase or cause: %s", logs.String())
	}
}
