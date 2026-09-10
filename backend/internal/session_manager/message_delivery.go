package sessionmanager

import (
	"context"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	messageDeliveryReadyPoll     = 150 * time.Millisecond
	messageDeliveryReadySettle   = 750 * time.Millisecond
	messageDeliveryReadyFallback = 5 * time.Second
	messageDeliveryReadyLines    = 80
)

// WaitForMessageDeliveryReady blocks until a session can safely accept an
// injected message. Runtime startup only proves that the process exists; a TUI
// can still be initializing and silently discard an otherwise-successful paste.
func (m *Manager) WaitForMessageDeliveryReady(ctx context.Context, id domain.SessionID) error {
	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	if rec.IsTerminated {
		return ErrTerminated
	}
	if rec.Activity.State == domain.ActivityExited {
		return ErrAgentExited
	}

	mode := domain.NormalizeSessionMode(rec.Mode)
	var detector ports.TerminalActivityDetector
	var emptyComposerDetector ports.EmptyComposerDetector
	emptyComposerReadyWhileWaiting := false
	var handle ports.RuntimeHandle
	requireFirstSignal := false
	if mode == domain.SessionModeTUI {
		if rec.Metadata.RuntimeHandleID == "" {
			return ErrIncompleteHandle
		}
		agent, found := m.agents.Agent(rec.Harness)
		if !found {
			return fmt.Errorf("%w: %s", ErrUnknownHarness, rec.Harness)
		}
		detector, _ = agent.(ports.TerminalActivityDetector)
		emptyComposerDetector, _ = agent.(ports.EmptyComposerDetector)
		if readiness, ok := agent.(ports.WaitingInputComposerReadiness); ok {
			emptyComposerReadyWhileWaiting = readiness.EmptyComposerProvesWaitingInputReady()
		}
		signaler, signalsReadiness := agent.(ports.StartupInputReadinessSignaler)
		requireFirstSignal = signalsReadiness && signaler.FirstSignalProvesInputReady()
		handle = runtimeHandle(rec.Metadata)
	}

	startedAt := time.Now()
	idleSince := time.Time{}
	ticker := time.NewTicker(messageDeliveryReadyPoll)
	defer ticker.Stop()

	for {
		rec, ok, err = m.store.GetSession(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		if rec.IsTerminated {
			return ErrTerminated
		}
		if rec.Activity.State == domain.ActivityExited {
			return ErrAgentExited
		}

		ready := rec.Activity.State == domain.ActivityIdle
		if mode == domain.SessionModeTUI {
			if detector != nil {
				ready = false
				if output, outputErr := m.runtime.GetOutput(ctx, handle, messageDeliveryReadyLines); outputErr == nil {
					state, authoritative := detector.DetectTerminalActivity(output)
					ready = authoritative && state == domain.ActivityIdle
					if !authoritative &&
						(rec.Activity.State == domain.ActivityIdle ||
							rec.Activity.State == domain.ActivityWaitingInput && emptyComposerReadyWhileWaiting) &&
						emptyComposerDetector != nil {
						ready = emptyComposerDetector.ComposerIsEmpty(output)
					}
				}
			} else {
				// MarkSpawned records idle before the TUI is necessarily ready.
				// Capability-declaring adapters require their first startup-ready
				// hook. Hookless adapters retain the bounded degraded fallback that
				// existed before the startup input gate.
				if requireFirstSignal {
					ready = ready && !rec.FirstSignalAt.IsZero()
				} else {
					ready = false
				}
				if !requireFirstSignal && rec.Activity.State == domain.ActivityIdle && time.Since(startedAt) >= messageDeliveryReadyFallback {
					m.logger.Warn("message delivery readiness timed out; falling back while session is idle",
						"sessionID", id,
						"timeout", messageDeliveryReadyFallback.String(),
					)
					return nil
				}
			}
		}

		if ready {
			if idleSince.IsZero() {
				idleSince = time.Now()
			} else if time.Since(idleSince) >= messageDeliveryReadySettle {
				return nil
			}
		} else {
			idleSince = time.Time{}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
