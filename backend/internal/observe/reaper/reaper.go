// Package reaper implements the OBSERVE-layer polling timer that supplies the
// LCM with per-session runtime liveness probes.
//
// The reaper only reports facts — it never writes session rows directly. The LCM
// consumes these facts through ApplyRuntimeObservation. A probe error is
// reported as a probe-failure fact, never collapsed to "alive" or "dead".
package reaper

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// DefaultTickInterval is the cadence used when Config.Tick is zero. It mirrors
// the design doc's 5s sampling window for runtime liveness.
const DefaultTickInterval = 5 * time.Second

// Circuit breaker against mass termination (issue #3475): if one cycle
// concludes that most of the board is dead at once, that is not N independent
// agent exits — it is one infrastructure outage (or an adapter bug) being
// misread. A wrongly-skipped termination is cheap and self-corrects on the
// next probe; a wrongly-applied mass termination archives the user's entire
// board. The breaker only engages above massDeadMinSessions so small boards
// (where two agents finishing together is normal) keep exact behavior.
const (
	massDeadMinSessions = 5
	massDeadFraction    = 0.5
)

// Config holds the externally-tunable knobs for a Reaper. Every field is
// optional; zero values fall back to safe defaults so production wiring and
// tests can both stay terse.
type Config struct {
	// Tick is the interval between ticks. <=0 means DefaultTickInterval.
	Tick time.Duration
	// Clock supplies ObservedAt stamps. nil means time.Now. Injected in tests so
	// assertions don't race wallclock.
	Clock func() time.Time
	// Logger receives operational diagnostics (probe errors, skipped sessions,
	// LCM call failures). The reaper logs but does not propagate these errors
	// because a single failed probe must not kill the loop. nil means
	// slog.Default.
	Logger *slog.Logger
}

type sessionSource interface {
	ListAllSessions(ctx context.Context) ([]domain.SessionRecord, error)
}

type runtimeObservationSink interface {
	ApplyRuntimeObservation(ctx context.Context, id domain.SessionID, f ports.RuntimeFacts) error
}

type runtimeProber interface {
	IsAlive(context.Context, ports.RuntimeHandle) (bool, error)
}

// Reaper is the polling timer. Construct it with New; start the background
// goroutine with Start, or drive a single cycle synchronously with Tick.
type Reaper struct {
	sink     runtimeObservationSink
	sessions sessionSource
	runtime  runtimeProber
	workload ports.SupervisedProcessInspector
	tick     time.Duration
	clock    func() time.Time
	logger   *slog.Logger

	// The periodic loop and boot-time reconciliation may call Tick on the same
	// Reaper concurrently, so the per-run warning set needs synchronization.
	missingHandleMu     sync.Mutex
	warnedMissingHandle map[domain.SessionID]struct{}
}

// New constructs a Reaper. sink is the lifecycle fact destination; sessions
// supplies the rows to probe; runtime checks whether a stored handle is alive.
func New(sink runtimeObservationSink, sessions sessionSource, runtime runtimeProber, cfg Config) *Reaper {
	r := &Reaper{
		sink:                sink,
		sessions:            sessions,
		runtime:             runtime,
		tick:                cfg.Tick,
		clock:               cfg.Clock,
		logger:              cfg.Logger,
		warnedMissingHandle: make(map[domain.SessionID]struct{}),
	}
	if workload, ok := runtime.(ports.SupervisedProcessInspector); ok {
		r.workload = workload
	}
	if r.tick <= 0 {
		r.tick = DefaultTickInterval
	}
	if r.clock == nil {
		r.clock = time.Now
	}
	if r.logger == nil {
		r.logger = slog.Default()
	}
	return r
}

// Start launches the background goroutine and returns a channel that closes
// once the loop has exited. The loop exits on ctx cancellation; the channel
// gives the daemon a clean shutdown hook (wait on it after cancel to confirm
// the reaper has stopped before tearing down dependencies).
func (r *Reaper) Start(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go r.loop(ctx, done)
	return done
}

func (r *Reaper) loop(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(r.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.Tick(ctx); err != nil {
				r.logger.Error("reaper: tick failed", "err", err)
			}
		}
	}
}

// Tick runs one observation cycle: it enumerates non-terminated sessions,
// probes each one's runtime, and reports each result back as a fact.
//
// Probing and reporting are two phases so the cycle can be arbitrated as a
// whole: if the dead fraction of one pass trips the mass-death circuit
// breaker, every dead conclusion in that pass is downgraded to a failed probe
// before anything reaches the LCM, and the pass is logged at error level.
//
// Tick is exported so the daemon (and tests) can drive cycles synchronously,
// and so the Start goroutine has a single chokepoint to log against.
//
// Errors: only the session-listing failure is propagated, since it short-
// circuits the rest of the cycle. Per-session ApplyRuntimeObservation failures
// are logged but never propagated — one failed call must not bring down the loop.
func (r *Reaper) Tick(ctx context.Context) error {
	now := r.clock()

	sessions, err := r.sessions.ListAllSessions(ctx)
	if err != nil {
		return err
	}

	type observation struct {
		id    domain.SessionID
		facts ports.RuntimeFacts
	}
	var observations []observation
	dead := 0
	for _, sess := range sessions {
		if sess.IsTerminated {
			continue
		}
		facts, ok := r.probeOne(ctx, sess, now)
		if !ok {
			continue
		}
		if facts.Runtime == ports.ProbeDead {
			dead++
		}
		observations = append(observations, observation{id: sess.ID, facts: facts})
	}

	if dead >= massDeadMinSessions && float64(dead) > massDeadFraction*float64(len(observations)) {
		// 28 unrelated agents do not exit within one probe pass of each other.
		// Whatever produced this pass is an outage, not N deaths; leave the
		// board untouched and let the next probe decide.
		r.logger.Error("reaper: mass-death circuit breaker tripped, reporting the pass as inconclusive",
			"dead", dead, "probed", len(observations))
		for i := range observations {
			if observations[i].facts.Runtime == ports.ProbeDead {
				observations[i].facts.Runtime = ports.ProbeFailed
			}
		}
	}

	for _, obs := range observations {
		if err := r.sink.ApplyRuntimeObservation(ctx, obs.id, obs.facts); err != nil {
			r.logger.Error("reaper: ApplyRuntimeObservation failed",
				"session", obs.id, "err", err)
		}
	}
	return nil
}

// probeOne probes a single session and returns the resulting facts. Every
// probe result — alive, dead, or failed — is returned for reporting. The
// reaper does not optimize away the "alive" case; the reaper has no business
// deciding what counts as a no-op. The LCM diffs and only writes on actual
// change. ok is false for sessions that cannot or must not be runtime-probed.
func (r *Reaper) probeOne(ctx context.Context, sess domain.SessionRecord, now time.Time) (ports.RuntimeFacts, bool) {
	// A chat session has no terminal runtime by design, so it has no handle to
	// probe and its absence is not an anomaly. Terminal liveness and chat
	// controller liveness are separate facts: inferring one from the other would
	// let a healthy chat session look dead to the reaper. The chat controller
	// reports its own health through the conversation's controller state.
	if domain.NormalizeSessionMode(sess.Mode) == domain.SessionModeChat {
		return ports.RuntimeFacts{}, false
	}
	handle, ok := handleFromRecord(sess)
	if !ok {
		// A session in the running-set without a handle is an anomaly worth
		// surfacing (MarkSpawned should have set both keys). Warn rather
		// than Debug so it doesn't hide behind a noisy log level.
		r.warnMissingHandleOnce(sess.ID)
		return ports.RuntimeFacts{}, false
	}
	alive, probeErr := r.runtime.IsAlive(ctx, handle)
	facts := ports.RuntimeFacts{ObservedAt: now, LaunchID: sess.Metadata.RuntimeLaunchID, Workload: ports.ProbeFailed}
	switch {
	case probeErr != nil:
		// Failed probe must NOT be collapsed to alive — that would let a
		// transient tmux outage hide a really-dead session, and a
		// transient adapter bug terminate a really-alive one. Report failed
		// and let the LCM arbitrate.
		facts.Runtime = ports.ProbeFailed
		r.logger.Debug("reaper: probe error reported as failed fact",
			"session", sess.ID, "err", probeErr)
	case !alive:
		facts.Runtime = ports.ProbeDead
	default:
		facts.Runtime = ports.ProbeAlive
		if facts.LaunchID != "" && r.workload != nil {
			workloadAlive, workloadErr := r.workload.IsSupervisedProcessAlive(ctx, handle, ports.SupervisedProcessRef{
				SessionID: sess.ID,
				LaunchID:  facts.LaunchID,
			})
			switch {
			case workloadErr != nil:
				facts.Workload = ports.ProbeFailed
				r.logger.Debug("reaper: workload probe error reported as failed fact",
					"session", sess.ID, "launch", facts.LaunchID, "err", workloadErr)
			case workloadAlive:
				facts.Workload = ports.ProbeAlive
			default:
				facts.Workload = ports.ProbeDead
			}
		}
	}

	return facts, true
}

// warnMissingHandleOnce logs at most once per session for this Reaper's lifetime.
func (r *Reaper) warnMissingHandleOnce(id domain.SessionID) {
	r.missingHandleMu.Lock()
	if _, warned := r.warnedMissingHandle[id]; warned {
		r.missingHandleMu.Unlock()
		return
	}
	r.warnedMissingHandle[id] = struct{}{}
	r.missingHandleMu.Unlock()

	r.logger.Warn("reaper: session has no runtime handle metadata, skipping",
		"session", id)
}

// handleFromRecord reconstructs the RuntimeHandle stored on the session by
// MarkSpawned. An empty handle id means the session cannot be probed.
func handleFromRecord(rec domain.SessionRecord) (ports.RuntimeHandle, bool) {
	id := rec.Metadata.RuntimeHandleID
	if id == "" {
		return ports.RuntimeHandle{}, false
	}
	return ports.RuntimeHandle{ID: id}, true
}
