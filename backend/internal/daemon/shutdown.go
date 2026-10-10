package daemon

import (
	"context"
	"log/slog"
	"os/signal"
	"syscall"
	"time"
)

// withDaemonContext owns signals outside the scope containing resource defers.
// Workers are cancelled explicitly before joins; signals are unregistered last.
func withDaemonContext(run func(context.Context, context.CancelFunc) error) error {
	ctx, cancelWorkers, stopSignals := daemonContext()
	defer stopSignals()
	defer cancelWorkers()
	return run(ctx, cancelWorkers)
}

func daemonContext() (context.Context, context.CancelFunc, context.CancelFunc) {
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	// Cancelling workers must not restore default signal handling while resource
	// cleanup is still running. Run defers stopSignals until shutdown finishes.
	ctx, cancelWorkers := context.WithCancel(signalCtx)
	return ctx, cancelWorkers, stopSignals
}

// shutdownStep bounds joins even when a worker ignores context cancellation.
// A timed-out worker may still be running until the daemon process exits.
func shutdownStep(timeout time.Duration, log *slog.Logger, name string, step func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- step(ctx) }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err != nil {
		log.Error(name+" shutdown", "err", err, "timeout", timeout)
	}
}
