package sessionguard

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type fakeStore struct {
	rec domain.SessionRecord
	ok  bool
	err error
}

func (s *fakeStore) GetSession(_ context.Context, _ domain.SessionID) (domain.SessionRecord, bool, error) {
	return s.rec, s.ok, s.err
}

type fakeMessenger struct {
	sent []string
	err  error
}

func (m *fakeMessenger) Send(_ context.Context, _ domain.SessionID, msg string) error {
	m.sent = append(m.sent, msg)
	return m.err
}

func record(state domain.ActivityState, terminated bool) domain.SessionRecord {
	return domain.SessionRecord{ID: "s1", IsTerminated: terminated, Activity: domain.Activity{State: state}}
}

func TestGuard_OutcomeByState(t *testing.T) {
	cases := []struct {
		name        string
		rec         domain.SessionRecord
		ok          bool
		wantDeliver Outcome
		wantNudge   Outcome
	}{
		{"active", record(domain.ActivityActive, false), true, Sent, Sent},
		{"idle", record(domain.ActivityIdle, false), true, Sent, Sent},
		// waiting_input is the split that motivates two methods: a user message
		// (or its Enter re-submit) belongs at an idle prompt; an unsolicited
		// automated nudge does not.
		{"waiting_input", record(domain.ActivityWaitingInput, false), true, Sent, SuppressedAwaitingUser},
		{"blocked", record(domain.ActivityBlocked, false), true, SuppressedAwaitingUser, SuppressedAwaitingUser},
		// exited is refused even without IsTerminated: the pane holds an
		// interactive shell after agent exit, so a paste would execute there.
		{"exited", record(domain.ActivityExited, false), true, SuppressedExited, SuppressedExited},
		{"terminated", record(domain.ActivityIdle, true), true, SuppressedTerminated, SuppressedTerminated},
		{"missing", domain.SessionRecord{}, false, SuppressedNotFound, SuppressedNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for method, want := range map[string]Outcome{"Deliver": tc.wantDeliver, "Nudge": tc.wantNudge} {
				msgr := &fakeMessenger{}
				g := New(&fakeStore{rec: tc.rec, ok: tc.ok}, msgr, nil)
				var got Outcome
				var err error
				if method == "Deliver" {
					got, err = g.Deliver(context.Background(), "s1", "hello")
				} else {
					got, err = g.Nudge(context.Background(), "s1", "hello")
				}
				if err != nil {
					t.Fatalf("%s: unexpected error: %v", method, err)
				}
				if got != want {
					t.Errorf("%s: outcome = %v, want %v", method, got, want)
				}
				if wantSent := want == Sent; (len(msgr.sent) == 1) != wantSent {
					t.Errorf("%s: messenger sends = %d, want sent=%v", method, len(msgr.sent), wantSent)
				}
			}
		})
	}
}

// TestGuard_NudgeUrgentReachesNeedsInputButNotBlocked pins NudgeUrgent's
// policy: unlike Nudge, an idle needs-input prompt (waiting_input) does not
// suppress the write, because some automated nudges (a merge conflict, for
// instance) must reach a session parked there. A live permission dialog
// (blocked) still refuses, matching Deliver: pasting there risks answering
// the dialog on the user's behalf.
func TestGuard_NudgeUrgentReachesNeedsInputButNotBlocked(t *testing.T) {
	cases := []struct {
		name string
		rec  domain.SessionRecord
		ok   bool
		want Outcome
	}{
		{"active", record(domain.ActivityActive, false), true, Sent},
		{"idle", record(domain.ActivityIdle, false), true, Sent},
		{"waiting_input", record(domain.ActivityWaitingInput, false), true, Sent},
		{"blocked", record(domain.ActivityBlocked, false), true, SuppressedAwaitingUser},
		{"exited", record(domain.ActivityExited, false), true, SuppressedExited},
		{"terminated", record(domain.ActivityIdle, true), true, SuppressedTerminated},
		{"missing", domain.SessionRecord{}, false, SuppressedNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msgr := &fakeMessenger{}
			g := New(&fakeStore{rec: tc.rec, ok: tc.ok}, msgr, nil)
			got, err := g.NudgeUrgent(context.Background(), "s1", "hello")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("outcome = %v, want %v", got, tc.want)
			}
			if wantSent := tc.want == Sent; (len(msgr.sent) == 1) != wantSent {
				t.Errorf("messenger sends = %d, want sent=%v", len(msgr.sent), wantSent)
			}
		})
	}
}

func TestGuard_StoreErrorFailsClosed(t *testing.T) {
	msgr := &fakeMessenger{}
	g := New(&fakeStore{err: errors.New("db locked")}, msgr, nil)
	for name, call := range map[string]func() (Outcome, error){
		"Deliver": func() (Outcome, error) { return g.Deliver(context.Background(), "s1", "x") },
		"Nudge":   func() (Outcome, error) { return g.Nudge(context.Background(), "s1", "x") },
	} {
		got, err := call()
		if err == nil {
			t.Fatalf("%s: want error from store failure", name)
		}
		if got != SuppressedUnknown {
			t.Errorf("%s: outcome = %v, want SuppressedUnknown", name, got)
		}
	}
	if len(msgr.sent) != 0 {
		t.Errorf("messenger was called %d times on unknown state, want 0", len(msgr.sent))
	}
}

func TestGuard_MessengerErrorIsSentPlusError(t *testing.T) {
	sendErr := errors.New("pane gone")
	g := New(&fakeStore{rec: record(domain.ActivityActive, false), ok: true}, &fakeMessenger{err: sendErr}, nil)
	got, err := g.Deliver(context.Background(), "s1", "x")
	if !errors.Is(err, sendErr) {
		t.Fatalf("error = %v, want wrapped %v", err, sendErr)
	}
	if got != Sent {
		t.Errorf("outcome = %v, want Sent (the write was attempted)", got)
	}
}

// TestGuard_NudgeCoordinationEnforcesSteeringAtWriteBoundary pins the binding
// safety decision to the guard's own just-in-time read: an active session is
// refused unless its harness declares it can be steered mid-turn, no matter what
// the caller believed when it decided to dispatch.
func TestGuard_NudgeCoordinationEnforcesSteeringAtWriteBoundary(t *testing.T) {
	steersCodex := func(h domain.AgentHarness) bool { return h == domain.HarnessCodex }
	cases := []struct {
		name    string
		state   domain.ActivityState
		harness domain.AgentHarness
		steers  func(domain.AgentHarness) bool
		want    Outcome
	}{
		{"idle delivers", domain.ActivityIdle, domain.HarnessClaudeCode, steersCodex, Sent},
		{"active non-steering suppressed", domain.ActivityActive, domain.HarnessClaudeCode, steersCodex, SuppressedBusy},
		{"active steering delivers", domain.ActivityActive, domain.HarnessCodex, steersCodex, Sent},
		{"active nil predicate suppressed", domain.ActivityActive, domain.HarnessCodex, nil, SuppressedBusy},
		{"waiting_input suppressed", domain.ActivityWaitingInput, domain.HarnessCodex, steersCodex, SuppressedAwaitingUser},
		{"blocked suppressed", domain.ActivityBlocked, domain.HarnessCodex, steersCodex, SuppressedAwaitingUser},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := record(tc.state, false)
			rec.Harness = tc.harness
			msg := &fakeMessenger{}
			g := New(&fakeStore{rec: rec, ok: true}, msg, nil)

			got, err := g.NudgeCoordination(context.Background(), "s1", "x", tc.steers)
			if err != nil {
				t.Fatalf("NudgeCoordination: %v", err)
			}
			if got != tc.want {
				t.Fatalf("outcome = %v, want %v", got, tc.want)
			}
			wantSends := 0
			if tc.want == Sent {
				wantSends = 1
			}
			if len(msg.sent) != wantSends {
				t.Fatalf("sends = %d, want %d", len(msg.sent), wantSends)
			}
		})
	}
}
