// Package reconciler drives the terminal-resource reconciler: a state-driven,
// idempotent loop that converges every terminated session's runtime + workspace
// toward safe release. It is the mirror image of observe/reaper — the reaper
// skips terminated sessions, the reconciler processes ONLY them.
//
// The durable invariant it maintains: is_terminated=true means the session no
// longer wants runtime resources, so the daemon must release them (or record why
// it can't: a dirty worktree, a transient failure being retried, or an exhausted
// failure). Terminal state is the durable intent; it does not promise cleanup
// already succeeded.
//
// Three drivers feed one async worker:
//   - Live wake: a CDC session_updated subscription enqueues a session the moment
//     its is_terminated flips true (the immediate first attempt).
//   - Boot scan: a sessions-driven candidate scan at start drains the pre-existing
//     leaked backlog (terminated sessions with no facts row).
//   - Periodic sweep: the same candidate scan on a ticker retries capped-backoff
//     pending cleanups whose next_attempt_at is due.
package reconciler

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/cdc"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Default tunables.
const (
	// DefaultSweepInterval is how often the periodic candidate scan runs to pick
	// up capped-backoff retries and any session missed by a dropped live wake.
	DefaultSweepInterval = 30 * time.Second
	// DefaultQueueSize bounds the enqueue channel. A live wake that finds it full
	// is dropped (the scan is the durable backstop), so this only needs to absorb
	// bursts, not the whole backlog.
	DefaultQueueSize = 256
	// DefaultAttemptTimeout bounds one finalize attempt. It runs on a detached
	// context so a slow `git worktree remove` is not interrupted by daemon
	// shutdown, yet cannot hang forever.
	DefaultAttemptTimeout = 2 * time.Minute
	// DefaultShutdownGrace is how long Stop waits for an in-flight attempt to
	// finish before returning. Independent of AttemptTimeout so a generous
	// per-attempt bound can't stall daemon exit.
	DefaultShutdownGrace = 5 * time.Second
)

// finalizer releases one terminated session's resources. Implemented by
// *sessionmanager.Manager.FinalizeTerminalSession.
type finalizer interface {
	FinalizeTerminalSession(ctx context.Context, id domain.SessionID) error
}

// candidateSource is the sessions-driven candidate scan. Implemented by the
// sqlite store's ListTerminalCleanupCandidates.
type candidateSource interface {
	ListTerminalCleanupCandidates(ctx context.Context, now time.Time) ([]domain.SessionID, error)
}

// subscriber is the CDC broadcaster surface used for the live wake. Implemented
// by *cdc.Broadcaster.
type subscriber interface {
	Subscribe(fn func(cdc.Event)) (unsubscribe func())
}

// Config holds the reconciler's tunables; the zero value is filled with defaults.
type Config struct {
	SweepInterval  time.Duration
	QueueSize      int
	AttemptTimeout time.Duration
	ShutdownGrace  time.Duration
	Clock          func() time.Time
	Logger         *slog.Logger
}

// Reconciler runs the terminal-resource reconciliation loop.
type Reconciler struct {
	finalizer  finalizer
	candidates candidateSource
	events     subscriber

	queue chan domain.SessionID
	unsub func()
	// attemptBase is the detached base context for finalize attempts: it survives
	// parent (daemon) cancellation so a git op isn't interrupted the instant
	// shutdown begins, but is force-cancelled after the shutdown grace so an
	// in-flight attempt can't keep writing to a store the daemon is about to close.
	attemptBase   context.Context
	attemptCancel context.CancelFunc

	sweep          time.Duration
	attemptTimeout time.Duration
	grace          time.Duration
	clock          func() time.Time
	logger         *slog.Logger
}

// New builds a Reconciler, defaulting any unset Config field.
func New(f finalizer, candidates candidateSource, events subscriber, cfg Config) *Reconciler {
	r := &Reconciler{
		finalizer:      f,
		candidates:     candidates,
		events:         events,
		sweep:          cfg.SweepInterval,
		attemptTimeout: cfg.AttemptTimeout,
		grace:          cfg.ShutdownGrace,
		clock:          cfg.Clock,
		logger:         cfg.Logger,
	}
	if r.sweep <= 0 {
		r.sweep = DefaultSweepInterval
	}
	if r.attemptTimeout <= 0 {
		r.attemptTimeout = DefaultAttemptTimeout
	}
	if r.grace <= 0 {
		r.grace = DefaultShutdownGrace
	}
	if r.clock == nil {
		r.clock = time.Now
	}
	if r.logger == nil {
		r.logger = slog.Default()
	}
	qs := cfg.QueueSize
	if qs <= 0 {
		qs = DefaultQueueSize
	}
	r.queue = make(chan domain.SessionID, qs)
	return r
}

// Start registers the live-wake subscription, then launches the run loop and
// returns a done-channel closed once the loop has drained on ctx cancellation.
// Subscribing here — BEFORE run takes the boot-scan snapshot — closes the boot
// window: a restore-then-exit session can't slip between the snapshot and the
// live wake (an overlap is harmless because finalize is idempotent).
func (r *Reconciler) Start(ctx context.Context) <-chan struct{} {
	r.unsub = r.events.Subscribe(r.onEvent)
	// Detached from ctx on purpose (see field doc); cancelled by run after the
	// shutdown grace, and always released via defer in run.
	r.attemptBase, r.attemptCancel = context.WithCancel(context.Background()) //nolint:gosec // G118: detached attempt context is intentional
	done := make(chan struct{})
	go r.run(ctx, done)
	return done
}

func (r *Reconciler) run(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	// Always release the detached base so it can't leak past shutdown.
	defer r.attemptCancel()

	workerDone := make(chan struct{})
	go r.worker(workerDone)

	// Boot scan: drain the backlog onto the worker (idempotent with any live wake
	// that also fires for the same session).
	r.scan(ctx)

	t := time.NewTicker(r.sweep)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			// Stop new live wakes BEFORE closing the queue, else a late Publish
			// would send on a closed channel and panic. Unsubscribe blocks until any
			// in-flight Publish callback returns, so no onEvent can race the close.
			r.unsub()
			close(r.queue)
			// Wait a short bounded grace for the worker to finish cleanly. If it
			// doesn't (a slow git op on the detached context), force-cancel the
			// attempt base and JOIN the worker before returning: this both bounds
			// shutdown (we don't wait the full per-attempt timeout) AND guarantees
			// the worker has stopped touching the store before the daemon closes it,
			// closing the write-after-close window.
			select {
			case <-workerDone:
			case <-time.After(r.grace):
				r.logger.Warn("reconciler: shutdown grace elapsed, cancelling in-flight cleanup")
				r.attemptCancel()
				<-workerDone
			}
			return
		case <-t.C:
			r.scan(ctx)
		}
	}
}

// worker drains the queue, finalizing one session at a time. Serial processing
// keeps concurrent finalizes off the same session (the manager also holds a
// per-session lock) and bounds resource use during a large boot drain.
func (r *Reconciler) worker(done chan<- struct{}) {
	defer close(done)
	for id := range r.queue {
		// A fresh bounded context per attempt, derived from the detached
		// attemptBase: it survives daemon-shutdown cancellation (so a git op isn't
		// interrupted mid-write) yet cannot hang forever, and run force-cancels
		// attemptBase after the shutdown grace so this can't outlive store.Close.
		attemptCtx, cancel := context.WithTimeout(r.attemptBase, r.attemptTimeout)
		if err := r.finalizer.FinalizeTerminalSession(attemptCtx, id); err != nil {
			r.logger.Error("reconciler: finalize failed", "sessionID", id, "err", err)
		}
		cancel()
	}
}

// scan enqueues every current cleanup candidate. It blocks on a full queue
// (draining as the worker makes room) but unblocks promptly on ctx cancellation,
// so a large backlog neither drops work nor stalls shutdown.
func (r *Reconciler) scan(ctx context.Context) {
	ids, err := r.candidates.ListTerminalCleanupCandidates(ctx, r.clock())
	if err != nil {
		r.logger.Error("reconciler: candidate scan failed", "err", err)
		return
	}
	for _, id := range ids {
		select {
		case r.queue <- id:
		case <-ctx.Done():
			return
		}
	}
}

// onEvent is the live-wake callback. It runs inline on the CDC poller goroutine
// under the broadcaster's RLock, so it MUST NOT block: it does a non-blocking
// send and drops on a full queue (the scan is the backstop). It filters on the
// payload's isTerminated so it only wakes on a genuine terminal transition and
// never does a DB read on the hot path. Facts-trigger events carry {id} only (no
// isTerminated), so they are ignored here and serve purely as the frontend
// refetch nudge — which is also why a failed/pending retry write can't self-wake
// the reconciler and hot-loop.
func (r *Reconciler) onEvent(e cdc.Event) {
	if e.Type != cdc.EventSessionUpdated {
		return
	}
	var p struct {
		ID           string `json:"id"`
		IsTerminated bool   `json:"isTerminated"`
	}
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return
	}
	if p.ID == "" || !p.IsTerminated {
		return
	}
	select {
	case r.queue <- domain.SessionID(p.ID):
	default:
		// Never block the broadcaster (it stalls SSE + terminal fanout too).
		r.logger.Warn("reconciler: enqueue full, dropping live wake; scan will retry", "sessionID", p.ID)
	}
}
