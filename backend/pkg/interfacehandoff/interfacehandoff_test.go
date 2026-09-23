package interfacehandoff

import "testing"

func TestPhaseTable(t *testing.T) {
	normal := []Phase{
		PhaseRequested, PhasePreflighting, PhaseDraining, PhaseSourceStopping,
		PhaseSourceStopped, PhaseTargetStarting, PhaseActivating,
	}
	for index, phase := range normal {
		if !phase.Valid() || !phase.Active() || phase.Terminal() {
			t.Fatalf("active phase %q has invalid terminal state", phase)
		}
		next, ok := phase.Next()
		if !ok || !CanAdvance(phase, next) {
			t.Fatalf("normal edge %q -> %q is not allowed", phase, next)
		}
		if index == len(normal)-1 && next != PhaseCompleted {
			t.Fatalf("activating next = %q, want completed", next)
		}
	}
	for _, phase := range []Phase{PhaseCompleted, PhaseFailed, PhaseCancelled, PhaseRecovery} {
		if !phase.Valid() || phase.Active() || !phase.Terminal() {
			t.Fatalf("terminal phase %q has invalid active state", phase)
		}
		if _, ok := phase.Next(); ok {
			t.Fatalf("terminal phase %q has a next edge", phase)
		}
	}
}

func TestTerminalEdgesAndHeldMessageRelease(t *testing.T) {
	for _, phase := range []Phase{
		PhaseRequested, PhasePreflighting, PhaseDraining, PhaseSourceStopping,
		PhaseSourceStopped, PhaseTargetStarting, PhaseActivating,
	} {
		if !CanAdvance(phase, PhaseFailed) || !CanAdvance(phase, PhaseRecovery) {
			t.Fatalf("terminal error outcomes must be allowed from %q", phase)
		}
	}
	for _, phase := range []Phase{PhaseRequested, PhasePreflighting, PhaseDraining} {
		if !CanAdvance(phase, PhaseCancelled) || !MayReleaseHeldMessages(phase, PhaseCancelled) {
			t.Fatalf("early cancellation must be allowed and release messages from %q", phase)
		}
	}
	for _, phase := range []Phase{PhaseSourceStopping, PhaseSourceStopped, PhaseTargetStarting, PhaseActivating} {
		if CanAdvance(phase, PhaseCancelled) || MayReleaseHeldMessages(phase, PhaseCancelled) {
			t.Fatalf("late cancellation must not be allowed or release messages from %q", phase)
		}
	}
	if !MayReleaseHeldMessages(PhaseActivating, PhaseCompleted) {
		t.Fatal("completed activation must release held messages")
	}
	for _, outcome := range []Phase{PhaseFailed, PhaseRecovery} {
		if MayReleaseHeldMessages(PhaseSourceStopped, outcome) || MayReleaseHeldMessages(PhaseTargetStarting, outcome) {
			t.Fatalf("%q must keep messages fenced without readiness proof", outcome)
		}
	}
}

func TestFailureOutcomeRequiresRecoveryAfterSourceStopping(t *testing.T) {
	for _, phase := range []Phase{PhaseRequested, PhasePreflighting, PhaseDraining} {
		if got := FailureOutcome(phase); got != PhaseFailed {
			t.Fatalf("failure outcome for %q = %q, want failed", phase, got)
		}
	}
	for _, phase := range []Phase{PhaseSourceStopping, PhaseSourceStopped, PhaseTargetStarting, PhaseActivating} {
		if got := FailureOutcome(phase); got != PhaseRecovery {
			t.Fatalf("failure outcome for %q = %q, want recovery_required", phase, got)
		}
	}
}
