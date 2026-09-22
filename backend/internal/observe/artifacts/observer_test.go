package artifacts

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type fakeSessions struct {
	rows []domain.SessionRecord
	err  error
}

func (f fakeSessions) ListAllSessions(context.Context) ([]domain.SessionRecord, error) {
	return f.rows, f.err
}

type fakeSink struct {
	reconciled []domain.SessionID
	err        error
}

func (f *fakeSink) ReconcileSessionOutputType(_ context.Context, id domain.SessionID) error {
	f.reconciled = append(f.reconciled, id)
	return f.err
}

func TestPoll_SkipsTerminatedAndAlreadyPRSessions(t *testing.T) {
	sessions := fakeSessions{rows: []domain.SessionRecord{
		{ID: "live"},
		{ID: "terminated", IsTerminated: true},
		{ID: "already-pr", OutputType: domain.SessionOutputPR},
	}}
	sink := &fakeSink{}
	o := New(sessions, sink, Config{})

	if err := o.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.reconciled) != 1 || sink.reconciled[0] != "live" {
		t.Fatalf("reconciled = %v, want only [live]", sink.reconciled)
	}
}

func TestPoll_ReconcileFailurePerSessionDoesNotStopTheRest(t *testing.T) {
	sessions := fakeSessions{rows: []domain.SessionRecord{{ID: "a"}, {ID: "b"}}}
	sink := &fakeSink{err: errors.New("boom")}
	o := New(sessions, sink, Config{})

	if err := o.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.reconciled) != 2 {
		t.Fatalf("reconciled = %v, want both sessions attempted", sink.reconciled)
	}
}

func TestPoll_PropagatesListSessionsError(t *testing.T) {
	sessions := fakeSessions{err: errors.New("db down")}
	o := New(sessions, &fakeSink{}, Config{})

	if err := o.Poll(context.Background()); err == nil {
		t.Fatal("want error from ListAllSessions to propagate")
	}
}
