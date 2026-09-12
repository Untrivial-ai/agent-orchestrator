package reaper

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var ctx = context.Background()

type fakeLCM struct {
	observed map[domain.SessionID]ports.RuntimeFacts
}

func (l *fakeLCM) ApplyRuntimeObservation(_ context.Context, id domain.SessionID, f ports.RuntimeFacts) error {
	if l.observed == nil {
		l.observed = map[domain.SessionID]ports.RuntimeFacts{}
	}
	l.observed[id] = f
	return nil
}

type fakeSessions struct{ rows []domain.SessionRecord }

func (s fakeSessions) ListAllSessions(context.Context) ([]domain.SessionRecord, error) {
	return s.rows, nil
}

type fakeRuntime struct {
	alive         bool
	err           error
	workloadAlive bool
	workloadErr   error
}

func (r fakeRuntime) IsAlive(context.Context, ports.RuntimeHandle) (bool, error) {
	return r.alive, r.err
}

func (r fakeRuntime) IsSupervisedProcessAlive(context.Context, ports.RuntimeHandle, ports.SupervisedProcessRef) (bool, error) {
	return r.workloadAlive, r.workloadErr
}

func probableSession(id domain.SessionID) domain.SessionRecord {
	return domain.SessionRecord{
		ID:       id,
		Activity: domain.Activity{State: domain.ActivityActive},
		Metadata: domain.SessionMetadata{RuntimeHandleID: "h1"},
	}
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newReaper(lcm *fakeLCM, sessions fakeSessions, rt fakeRuntime) *Reaper {
	return New(lcm, sessions, rt, Config{Logger: quietLogger()})
}

func TestTick_ReportsAliveProbe(t *testing.T) {
	lcm := &fakeLCM{}
	sessions := fakeSessions{rows: []domain.SessionRecord{probableSession("mer-1")}}
	if err := newReaper(lcm, sessions, fakeRuntime{alive: true}).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := lcm.observed["mer-1"]; got.Runtime != ports.ProbeAlive || got.Workload != ports.ProbeFailed {
		t.Fatalf("want alive runtime with unsupported workload, got %+v", got)
	}
}

func TestTick_ReportsSupervisedWorkloadExit(t *testing.T) {
	lcm := &fakeLCM{}
	session := probableSession("mer-1")
	session.Metadata.RuntimeLaunchID = "launch-1"
	sessions := fakeSessions{rows: []domain.SessionRecord{session}}
	if err := newReaper(lcm, sessions, fakeRuntime{alive: true, workloadAlive: false}).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got := lcm.observed["mer-1"]
	if got.Runtime != ports.ProbeAlive || got.Workload != ports.ProbeDead || got.LaunchID != "launch-1" {
		t.Fatalf("unexpected supervised workload facts: %+v", got)
	}
}

func TestTick_ReportsSupervisedWorkloadAlive(t *testing.T) {
	lcm := &fakeLCM{}
	session := probableSession("mer-1")
	session.Metadata.RuntimeLaunchID = "launch-1"
	sessions := fakeSessions{rows: []domain.SessionRecord{session}}
	if err := newReaper(lcm, sessions, fakeRuntime{alive: true, workloadAlive: true}).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got := lcm.observed["mer-1"]
	if got.Runtime != ports.ProbeAlive || got.Workload != ports.ProbeAlive {
		t.Fatalf("unexpected supervised workload facts: %+v", got)
	}
}

func TestTick_ReportsWorkloadProbeErrorAsFailed(t *testing.T) {
	lcm := &fakeLCM{}
	session := probableSession("mer-1")
	session.Metadata.RuntimeLaunchID = "launch-1"
	sessions := fakeSessions{rows: []domain.SessionRecord{session}}
	if err := newReaper(lcm, sessions, fakeRuntime{alive: true, workloadErr: errors.New("ps unavailable")}).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got := lcm.observed["mer-1"]
	if got.Runtime != ports.ProbeAlive || got.Workload != ports.ProbeFailed {
		t.Fatalf("workload probe error must remain inconclusive, got %+v", got)
	}
}

func TestTick_ReportsProbeErrorAsFailed(t *testing.T) {
	lcm := &fakeLCM{}
	sessions := fakeSessions{rows: []domain.SessionRecord{probableSession("mer-1")}}
	if err := newReaper(lcm, sessions, fakeRuntime{err: errors.New("tmux gone")}).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := lcm.observed["mer-1"]; got.Runtime != ports.ProbeFailed || got.Workload != ports.ProbeFailed {
		t.Fatalf("probe error must report failed facts, got %+v", got)
	}
}

func TestTick_SkipsTerminatedSession(t *testing.T) {
	lcm := &fakeLCM{}
	dead := probableSession("mer-1")
	dead.IsTerminated = true
	sessions := fakeSessions{rows: []domain.SessionRecord{dead}}
	if err := newReaper(lcm, sessions, fakeRuntime{alive: true}).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if _, probed := lcm.observed["mer-1"]; probed {
		t.Fatal("terminated sessions must not be probed")
	}
}

// perHandleRuntime probes per-handle so one pass can mix alive and dead
// sessions; handles absent from the map read as dead.
type perHandleRuntime struct{ alive map[string]bool }

func (r perHandleRuntime) IsAlive(_ context.Context, h ports.RuntimeHandle) (bool, error) {
	return r.alive[h.ID], nil
}

func handledSession(id domain.SessionID) domain.SessionRecord {
	rec := probableSession(id)
	rec.Metadata.RuntimeHandleID = "h-" + string(id)
	return rec
}

// A pass where (nearly) every session probes dead is one infrastructure
// outage, not N independent exits (issue #3475: a killed tmux server read as
// 28 session deaths archived the whole board). The breaker must downgrade
// every dead conclusion of that pass to a failed probe.
func TestTick_MassDeathPassIsReportedAsInconclusive(t *testing.T) {
	lcm := &fakeLCM{}
	var rows []domain.SessionRecord
	for _, id := range []domain.SessionID{"mer-1", "mer-2", "mer-3", "mer-4", "mer-5", "mer-6"} {
		rows = append(rows, handledSession(id))
	}
	r := New(lcm, fakeSessions{rows: rows}, perHandleRuntime{}, Config{Logger: quietLogger()})
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(lcm.observed) != len(rows) {
		t.Fatalf("observed %d sessions, want %d", len(lcm.observed), len(rows))
	}
	for id, got := range lcm.observed {
		if got.Runtime != ports.ProbeFailed {
			t.Fatalf("session %s runtime = %q, want %q (mass death must not conclude)",
				id, got.Runtime, ports.ProbeFailed)
		}
	}
}

// Below the breaker threshold the reaper keeps reporting genuine deaths: a
// minority of dead sessions in a large pass passes through as ProbeDead.
func TestTick_MinorityDeadPassesThroughBreaker(t *testing.T) {
	lcm := &fakeLCM{}
	alive := map[string]bool{}
	var rows []domain.SessionRecord
	for i, id := range []domain.SessionID{"mer-1", "mer-2", "mer-3", "mer-4", "mer-5", "mer-6"} {
		rows = append(rows, handledSession(id))
		alive["h-"+string(id)] = i >= 2 // mer-1, mer-2 dead; rest alive
	}
	r := New(lcm, fakeSessions{rows: rows}, perHandleRuntime{alive: alive}, Config{Logger: quietLogger()})
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []domain.SessionID{"mer-1", "mer-2"} {
		if got := lcm.observed[id]; got.Runtime != ports.ProbeDead {
			t.Fatalf("session %s runtime = %q, want %q", id, got.Runtime, ports.ProbeDead)
		}
	}
	for _, id := range []domain.SessionID{"mer-3", "mer-4", "mer-5", "mer-6"} {
		if got := lcm.observed[id]; got.Runtime != ports.ProbeAlive {
			t.Fatalf("session %s runtime = %q, want %q", id, got.Runtime, ports.ProbeAlive)
		}
	}
}

// Small boards never trip the breaker: two agents finishing together is
// normal, and both are genuinely dead.
func TestTick_SmallBoardMassDeathStillConcludes(t *testing.T) {
	lcm := &fakeLCM{}
	rows := []domain.SessionRecord{handledSession("mer-1"), handledSession("mer-2")}
	r := New(lcm, fakeSessions{rows: rows}, perHandleRuntime{}, Config{Logger: quietLogger()})
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []domain.SessionID{"mer-1", "mer-2"} {
		if got := lcm.observed[id]; got.Runtime != ports.ProbeDead {
			t.Fatalf("session %s runtime = %q, want %q", id, got.Runtime, ports.ProbeDead)
		}
	}
}

func TestTick_SkipsSessionWithoutHandle(t *testing.T) {
	lcm := &fakeLCM{}
	noHandle := domain.SessionRecord{ID: "mer-1"} // no runtime metadata
	sessions := fakeSessions{rows: []domain.SessionRecord{noHandle}}
	if err := newReaper(lcm, sessions, fakeRuntime{alive: true}).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if _, probed := lcm.observed["mer-1"]; probed {
		t.Fatal("a session without a runtime handle must be skipped")
	}
}

func TestTick_WarnsOnlyOnceForSessionWithoutHandle(t *testing.T) {
	lcm := &fakeLCM{}
	sessions := fakeSessions{
		rows: []domain.SessionRecord{
			{ID: "mer-1"},
			{ID: "mer-2"},
		},
	}

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	r := New(lcm, sessions, fakeRuntime{alive: true}, Config{
		Logger: logger,
	})

	for range 3 {
		if err := r.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}

	const warning = "reaper: session has no runtime handle metadata, skipping"
	if got := strings.Count(logs.String(), warning); got != 2 {
		t.Fatalf("warning count = %d, want 2; logs:\n%s", got, logs.String())
	}
	for _, id := range []domain.SessionID{"mer-1", "mer-2"} {
		if got := strings.Count(logs.String(), "session="+string(id)); got != 1 {
			t.Fatalf("warning count for %s = %d, want 1; logs:\n%s", id, got, logs.String())
		}
	}
}
