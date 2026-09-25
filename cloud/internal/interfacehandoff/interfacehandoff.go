// Package interfacehandoff defines Cloud's storage- and transport-independent
// state table for moving a session between its Chat and terminal controllers.
//
// It deliberately contains no session, process, lease, or database concepts.
// Adapters own the effects performed at each checkpoint and whether they can
// restore a stopped source controller after a failed effect. Cloud adapters use
// this package for durable phase validity and terminal-state semantics.
package interfacehandoff

// Policy determines how work in flight is handled before the source controller
// is stopped.
type Policy string

const (
	// PolicyDrain waits for in-flight source work to finish.
	PolicyDrain Policy = "drain"
	// PolicyInterrupt stops in-flight source work immediately.
	PolicyInterrupt Policy = "interrupt"
)

// Valid reports whether p is a policy understood by the handoff state table.
func (p Policy) Valid() bool {
	return p == PolicyDrain || p == PolicyInterrupt
}

// Phase is the durable checkpoint of a controller handoff.
type Phase string

const (
	// PhaseRequested is the durable checkpoint before work starts.
	PhaseRequested Phase = "requested"
	// PhasePreflighting validates the source and target controllers.
	PhasePreflighting Phase = "preflighting"
	// PhaseDraining waits for source-side work to quiesce.
	PhaseDraining Phase = "draining"
	// PhaseSourceStopping records that the source is being stopped.
	PhaseSourceStopping Phase = "source_stopping"
	// PhaseSourceStopped records that the source has stopped.
	PhaseSourceStopped Phase = "source_stopped"
	// PhaseTargetStarting records that the target is being started.
	PhaseTargetStarting Phase = "target_starting"
	// PhaseActivating records that the target is ready to become active.
	PhaseActivating Phase = "activating"
	// PhaseCompleted is the successful terminal checkpoint.
	PhaseCompleted Phase = "completed"
	// PhaseFailed is the terminal checkpoint for a safely restored failure.
	PhaseFailed Phase = "failed"
	// PhaseCancelled is the terminal checkpoint for an early cancellation.
	PhaseCancelled Phase = "cancelled"
	// PhaseRecovery records that adapter recovery is required.
	PhaseRecovery Phase = "recovery_required"
)

// Valid reports whether p is a known handoff checkpoint.
func (p Phase) Valid() bool {
	switch p {
	case PhaseRequested,
		PhasePreflighting,
		PhaseDraining,
		PhaseSourceStopping,
		PhaseSourceStopped,
		PhaseTargetStarting,
		PhaseActivating,
		PhaseCompleted,
		PhaseFailed,
		PhaseCancelled,
		PhaseRecovery:
		return true
	default:
		return false
	}
}

// Terminal reports whether no coordinator may continue this handoff.
func (p Phase) Terminal() bool {
	switch p {
	case PhaseCompleted, PhaseFailed, PhaseCancelled, PhaseRecovery:
		return true
	default:
		return false
	}
}

// Active reports whether this handoff owns its session's admission fence.
func (p Phase) Active() bool { return p.Valid() && !p.Terminal() }

// Next returns the next normal checkpoint. Terminal outcomes are intentionally
// not returned because they depend on the adapter's effect result.
func (p Phase) Next() (Phase, bool) {
	switch p {
	case PhaseRequested:
		return PhasePreflighting, true
	case PhasePreflighting:
		return PhaseDraining, true
	case PhaseDraining:
		return PhaseSourceStopping, true
	case PhaseSourceStopping:
		return PhaseSourceStopped, true
	case PhaseSourceStopped:
		return PhaseTargetStarting, true
	case PhaseTargetStarting:
		return PhaseActivating, true
	case PhaseActivating:
		return PhaseCompleted, true
	default:
		return "", false
	}
}

// CanAdvance validates a durable compare-and-swap. Failed and recovery outcomes
// may close any active checkpoint because an adapter can observe an effect
// failure at any time. Cancellation is safe only before source_stopping.
func CanAdvance(from, to Phase) bool {
	if !from.Active() || !to.Valid() {
		return false
	}
	// A target whose shutdown is unconfirmed remains fenced in
	// target_starting while the adapter records the recovery detail. This is an
	// idempotent checkpoint update, not a second controller start.
	if from == PhaseTargetStarting && to == PhaseTargetStarting {
		return true
	}
	if next, ok := from.Next(); ok && to == next {
		return true
	}
	switch to {
	case PhaseFailed, PhaseRecovery:
		return true
	case PhaseCancelled:
		return from == PhaseRequested || from == PhasePreflighting || from == PhaseDraining
	default:
		return false
	}
}

// MayReleaseHeldMessages reports the terminal outcomes that, from phase data
// alone, prove that a controller is ready to accept messages. A distributed
// adapter that has extra readiness proof may make a stronger decision, but it
// must not release merely because any terminal phase was written.
func MayReleaseHeldMessages(from, to Phase) bool {
	return (from == PhaseActivating && to == PhaseCompleted) ||
		((from == PhaseRequested || from == PhasePreflighting || from == PhaseDraining) && to == PhaseCancelled)
}

// FailureOutcome returns the safe terminal outcome for an adapter that cannot
// prove the source controller was restored after a failed effect. Once source
// stopping has begun, the session may have no usable controller and must be
// recovered before held work is released. A local adapter may restore the
// source synchronously and choose Failed after that proof; a leased Cloud
// adapter must use this conservative outcome until a recovery worker acts.
func FailureOutcome(phase Phase) Phase {
	switch phase {
	case PhaseSourceStopping, PhaseSourceStopped, PhaseTargetStarting, PhaseActivating:
		return PhaseRecovery
	default:
		return PhaseFailed
	}
}
