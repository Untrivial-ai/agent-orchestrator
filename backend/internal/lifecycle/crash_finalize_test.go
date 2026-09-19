package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeCrashFinalizer struct {
	calls         []domain.SessionID
	err           error
	sawTerminated bool
	store         *fakeStore
}

func (f *fakeCrashFinalizer) FinalizeCrashedSession(_ context.Context, id domain.SessionID) error {
	f.calls = append(f.calls, id)
	if f.store != nil {
		f.sawTerminated = f.store.sessions[id].IsTerminated
	}
	return f.err
}

func TestRuntimeObservationConfirmedDeathRunsCrashFinalizerAfterTermination(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.Activity.LastActivityAt = time.Now().Add(-2 * time.Minute)
	st.sessions[rec.ID] = rec
	finalizer := &fakeCrashFinalizer{store: st}
	m.SetCrashFinalizer(finalizer)

	if err := m.ApplyRuntimeObservation(ctx, rec.ID, ports.RuntimeFacts{Runtime: ports.ProbeDead, Workload: ports.ProbeFailed}); err != nil {
		t.Fatal(err)
	}
	if len(finalizer.calls) != 1 || finalizer.calls[0] != rec.ID {
		t.Fatalf("crash finalizer calls = %v, want [%s]", finalizer.calls, rec.ID)
	}
	if !finalizer.sawTerminated {
		t.Fatal("crash finalizer ran before the terminal fact was durable")
	}

	if err := m.ApplyRuntimeObservation(ctx, rec.ID, ports.RuntimeFacts{Runtime: ports.ProbeDead}); err != nil {
		t.Fatal(err)
	}
	if len(finalizer.calls) != 1 {
		t.Fatalf("crash finalizer reran for terminal session: %v", finalizer.calls)
	}
}

func TestRuntimeObservationFinalizerErrorDoesNotRollBackTermination(t *testing.T) {
	m, st, _ := newManager()
	rec := working("mer-1")
	rec.Activity.LastActivityAt = time.Now().Add(-2 * time.Minute)
	st.sessions[rec.ID] = rec
	m.SetCrashFinalizer(&fakeCrashFinalizer{store: st, err: errors.New("disk unavailable")})

	if err := m.ApplyRuntimeObservation(ctx, rec.ID, ports.RuntimeFacts{Runtime: ports.ProbeDead}); err != nil {
		t.Fatal(err)
	}
	if !st.sessions[rec.ID].IsTerminated {
		t.Fatal("finalizer error rolled back the terminal fact")
	}
}
