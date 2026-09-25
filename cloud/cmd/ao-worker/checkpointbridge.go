package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"
)

// checkpointSafetyNetInterval is the coarse periodic backstop that captures
// in-progress work the Stop hook has not yet flushed. The Stop hook (turn
// completion) is the primary, event-driven trigger; this timer only exists so a
// long turn, or a delete/restore in the MIDDLE of the first turn (before any
// Stop has fired), still has a recent checkpoint to restore from instead of
// re-running from the task prompt. It is deliberately coarse — the checkpoint is
// change-detected, so a tick with no new work is a no-op — and is not the old
// 15s polling model. A future change (see the tool-use event trigger discussion)
// can drive capture off file-modifying tool events and drop or lengthen this.
const checkpointSafetyNetInterval = 25 * time.Second

// runCheckpointBridge serves a local unix socket that the harness Stop hook pokes
// on every turn completion (see runHook in ao-cloud-agent). Each poke runs one
// durable-restore checkpoint. The primary trigger is event-driven off turn
// completion; a coarse periodic safety net (checkpointSafetyNetInterval) also
// pokes so in-progress work is not lost when a turn is interrupted or restored
// before it completes. A single-slot trigger channel coalesces bursts (from any
// producer) so overlapping pokes collapse into one in-flight checkpoint, and the
// checkpoint itself is change-detected so an unchanged poke is a no-op. Adding a
// new trigger later is just another producer calling poke(). Blocks until ctx is
// done.
func runCheckpointBridge(
	ctx context.Context,
	socketPath string,
	run func(context.Context),
	logger *slog.Logger,
) error {
	return runCheckpointBridgeWithInterval(ctx, socketPath, run, logger, checkpointSafetyNetInterval)
}

func runCheckpointBridgeWithInterval(
	ctx context.Context,
	socketPath string,
	run func(context.Context),
	logger *slog.Logger,
	safetyNetInterval time.Duration,
) error {
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on checkpoint bridge socket: %w", err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = listener.Close()
		return fmt.Errorf("secure checkpoint bridge socket: %w", err)
	}

	// One consumer runs checkpoints serially, so overlapping pokes never race a
	// git preserve or a control-plane push.
	trigger := make(chan struct{}, 1)
	// poke is the single coalescing entry point every trigger source uses: a
	// non-blocking send that drops the poke if a checkpoint is already queued.
	poke := func() {
		select {
		case trigger <- struct{}{}:
		default:
		}
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-trigger:
				run(ctx)
			}
		}
	}()

	// Coarse periodic safety net (see checkpointSafetyNetInterval).
	go func() {
		ticker := time.NewTicker(safetyNetInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				poke()
			}
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /checkpoint", func(w http.ResponseWriter, _ *http.Request) {
		// Non-blocking, coalescing send: if a checkpoint is already queued this
		// poke is dropped. The handler returns immediately and the capture runs
		// asynchronously, so the Stop hook's short timeout is never spent waiting
		// on git or the network.
		poke()
		w.WriteHeader(http.StatusAccepted)
	})
	server := &http.Server{Handler: mux}
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		<-errCh
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
