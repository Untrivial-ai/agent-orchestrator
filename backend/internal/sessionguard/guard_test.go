package sessionguard

import (
	"context"
	"errors"
	"testing"
	"time"

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

type fixedInputLease bool

func (l fixedInputLease) AcquireSessionInput(domain.SessionID) (func(), bool) {
	if !l {
		return nil, false
	}
	return func() {}, true
}

type observedInputLease struct {
	acquired chan struct{}
	released chan struct{}
}

func (l *observedInputLease) AcquireSessionInput(domain.SessionID) (func(), bool) {
	close(l.acquired)
	return func() { close(l.released) }, true
}

type blockingMessenger struct {
	started chan struct{}
	unblock chan struct{}
}

func (m *blockingMessenger) Send(context.Context, domain.SessionID, string) error {
	close(m.started)
	<-m.unblock
	return nil
}

func record(state domain.ActivityState, terminated bool) domain.SessionRecord {
	return domain.SessionRecord{
		ID:            "s1",
		IsTerminated:  terminated,
		Activity:      domain.Activity{State: state},
		FirstSignalAt: time.Now(),
	}
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
// outcome table directly: it is the only nudge method that may write into a
// session idle at a needs-input prompt, and it must still refuse blocked (where
// a paste+Enter would answer a live permission dialog on the user's behalf) and
// a waiting_input prompt on a harness that cannot prove the prompt is a genuine
// idle composer rather than a masked permission decision.
func TestGuard_NudgeUrgentReachesNeedsInputButNotBlocked(t *testing.T) {
	acceptsAll := func(domain.AgentHarness) bool { return true }
	refusesAll := func(domain.AgentHarness) bool { return false }
	cases := []struct {
		name    string
		rec     domain.SessionRecord
		ok      bool
		accepts func(domain.AgentHarness) bool
		want    Outcome
	}{
		{"active", record(domain.ActivityActive, false), true, acceptsAll, Sent},
		{"idle", record(domain.ActivityIdle, false), true, acceptsAll, Sent},
		// The whole point of the method: unlike Nudge, an urgent nudge lands at
		// an idle waiting_input prompt — but only when the harness proves that
		// prompt is a real composer, not an ambiguous permission state.
		{"waiting_input capability-safe", record(domain.ActivityWaitingInput, false), true, acceptsAll, Sent},
		{"waiting_input ambiguous suppressed", record(domain.ActivityWaitingInput, false), true, refusesAll, SuppressedAwaitingUser},
		{"waiting_input nil predicate suppressed", record(domain.ActivityWaitingInput, false), true, nil, SuppressedAwaitingUser},
		{"blocked", record(domain.ActivityBlocked, false), true, acceptsAll, SuppressedAwaitingUser},
		{"exited", record(domain.ActivityExited, false), true, acceptsAll, SuppressedExited},
		{"terminated", record(domain.ActivityIdle, true), true, acceptsAll, SuppressedTerminated},
		{"missing", domain.SessionRecord{}, false, acceptsAll, SuppressedNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msgr := &fakeMessenger{}
			g := New(&fakeStore{rec: tc.rec, ok: tc.ok}, msgr, nil)
			got, err := g.NudgeUrgent(context.Background(), "s1", "hello", tc.accepts)
			if err != nil {
				t.Fatalf("NudgeUrgent: %v", err)
			}
			if got != tc.want {
				t.Fatalf("outcome = %v, want %v", got, tc.want)
			}
			if wantSent := tc.want == Sent; (len(msgr.sent) == 1) != wantSent {
				t.Fatalf("messenger sends = %d, want sent=%v", len(msgr.sent), wantSent)
			}
		})
	}
}

// TestGuard_NudgeUrgentRespectsTUIStartupGate keeps the urgent carve-out from
// widening past its one intended state: a TUI session that has not yet sent
// its first hook may still be sitting on a pre-session dialog, so an urgent
// nudge is refused there exactly as Deliver is.
func TestGuard_NudgeUrgentRespectsTUIStartupGate(t *testing.T) {
	msgr := &fakeMessenger{}
	rec := domain.SessionRecord{
		ID:       "s1",
		Mode:     domain.SessionModeTUI,
		Activity: domain.Activity{State: domain.ActivityWaitingInput},
	}
	g := New(&fakeStore{rec: rec, ok: true}, msgr, nil)
	g.SetStartupSignalGate(func(domain.AgentHarness) bool { return true })

	got, err := g.NudgeUrgent(context.Background(), "s1", "rebase", func(domain.AgentHarness) bool { return true })
	if err != nil {
		t.Fatalf("NudgeUrgent: %v", err)
	}
	if got != SuppressedStartupPending {
		t.Fatalf("outcome = %v, want SuppressedStartupPending", got)
	}
	if len(msgr.sent) != 0 {
		t.Fatalf("messenger sends = %d, want 0 before first hook", len(msgr.sent))
	}
}

func TestGuard_TUIStartupPendingBlocksDeliver(t *testing.T) {
	msgr := &fakeMessenger{}
	rec := domain.SessionRecord{
		ID:       "s1",
		Mode:     domain.SessionModeTUI,
		Activity: domain.Activity{State: domain.ActivityIdle},
	}
	g := New(&fakeStore{rec: rec, ok: true}, msgr, nil)
	g.SetStartupSignalGate(func(domain.AgentHarness) bool { return true })

	got, err := g.Deliver(context.Background(), "s1", "follow-up")
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got != SuppressedStartupPending {
		t.Fatalf("outcome = %v, want SuppressedStartupPending", got)
	}
	if len(msgr.sent) != 0 {
		t.Fatalf("messenger sends = %d, want 0 before first hook", len(msgr.sent))
	}

	rec.FirstSignalAt = time.Now()
	g = New(&fakeStore{rec: rec, ok: true}, msgr, nil)
	got, err = g.Deliver(context.Background(), "s1", "follow-up")
	if err != nil {
		t.Fatalf("Deliver after signal: %v", err)
	}
	if got != Sent {
		t.Fatalf("outcome after signal = %v, want Sent", got)
	}
}

func TestGuard_TUIWithoutStartupCapabilityAllowsDelivery(t *testing.T) {
	msgr := &fakeMessenger{}
	rec := domain.SessionRecord{
		ID:       "s1",
		Harness:  domain.HarnessAider,
		Mode:     domain.SessionModeTUI,
		Activity: domain.Activity{State: domain.ActivityIdle},
	}
	g := New(&fakeStore{rec: rec, ok: true}, msgr, nil)
	g.SetStartupSignalGate(func(domain.AgentHarness) bool { return false })

	got, err := g.Deliver(context.Background(), "s1", "follow-up")
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got != Sent {
		t.Fatalf("outcome = %v, want Sent for adapter without startup capability", got)
	}
	if len(msgr.sent) != 1 {
		t.Fatalf("messenger sends = %d, want 1", len(msgr.sent))
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

func TestGuard_InputLeaseCoversActualPaneWrite(t *testing.T) {
	lease := &observedInputLease{acquired: make(chan struct{}), released: make(chan struct{})}
	messenger := &blockingMessenger{started: make(chan struct{}), unblock: make(chan struct{})}
	g := New(&fakeStore{rec: record(domain.ActivityActive, false), ok: true}, messenger, nil)
	g.SetInputLease(lease)

	done := make(chan struct{})
	var got Outcome
	var gotErr error
	go func() {
		got, gotErr = g.Deliver(context.Background(), "s1", "x")
		close(done)
	}()

	<-lease.acquired
	<-messenger.started
	select {
	case <-lease.released:
		t.Fatal("input lease released before pane write returned")
	default:
	}
	close(messenger.unblock)
	<-done
	if gotErr != nil || got != Sent {
		t.Fatalf("Deliver = (%v, %v), want (Sent, nil)", got, gotErr)
	}
	select {
	case <-lease.released:
	default:
		t.Fatal("input lease was not released after pane write")
	}
}

func TestGuard_DeliverPostWriteRunsBeforeInputLeaseRelease(t *testing.T) {
	lease := &observedInputLease{acquired: make(chan struct{}), released: make(chan struct{})}
	g := New(&fakeStore{rec: record(domain.ActivityActive, false), ok: true}, &fakeMessenger{}, nil)
	g.SetInputLease(lease)
	callbackRan := false
	outcome, err := g.DeliverWithPostWrite(context.Background(), "s1", "hello", func(context.Context) error {
		select {
		case <-lease.released:
			t.Fatal("input lease released before post-write callback")
		default:
		}
		callbackRan = true
		return nil
	})
	if err != nil || outcome != Sent || !callbackRan {
		t.Fatalf("DeliverWithPostWrite = (%v, %v), callback=%v", outcome, err, callbackRan)
	}
	select {
	case <-lease.released:
	default:
		t.Fatal("input lease remained held after callback")
	}
}

func TestGuard_ClosedInputGateSuppressesBeforePaneWrite(t *testing.T) {
	messenger := &fakeMessenger{}
	g := New(&fakeStore{rec: record(domain.ActivityActive, false), ok: true}, messenger, nil)
	g.SetInputLease(fixedInputLease(false))

	got, err := g.Deliver(context.Background(), "s1", "x")
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got != SuppressedInputGated {
		t.Fatalf("outcome = %v, want SuppressedInputGated", got)
	}
	if len(messenger.sent) != 0 {
		t.Fatalf("messenger sends = %d, want 0", len(messenger.sent))
	}
}

func TestGuard_DeliverUnderMutationBypassesInputGateButKeepsSafetyChecks(t *testing.T) {
	messenger := &fakeMessenger{}
	g := New(&fakeStore{rec: record(domain.ActivityActive, false), ok: true}, messenger, nil)
	g.SetInputLease(fixedInputLease(false))

	got, err := g.DeliverUnderMutation(context.Background(), "s1", "handoff")
	if err != nil || got != Sent {
		t.Fatalf("DeliverUnderMutation = (%v, %v), want (Sent, nil)", got, err)
	}
	if len(messenger.sent) != 1 {
		t.Fatalf("messenger sends = %d, want 1", len(messenger.sent))
	}

	g.store = &fakeStore{rec: record(domain.ActivityBlocked, false), ok: true}
	got, err = g.DeliverUnderMutation(context.Background(), "s1", "unsafe")
	if err != nil || got != SuppressedAwaitingUser {
		t.Fatalf("blocked DeliverUnderMutation = (%v, %v), want (SuppressedAwaitingUser, nil)", got, err)
	}
	if len(messenger.sent) != 1 {
		t.Fatalf("blocked write reached messenger; sends = %d", len(messenger.sent))
	}
}

func TestGuard_CoordinationUnderMutationRechecksActivityAndBypassesInputGate(t *testing.T) {
	steersCodex := func(h domain.AgentHarness) bool { return h == domain.HarnessCodex }
	acceptsClaudeWaiting := func(h domain.AgentHarness) bool { return h == domain.HarnessClaudeCode }
	cases := []struct {
		name           string
		state          domain.ActivityState
		harness        domain.AgentHarness
		acceptsWaiting func(domain.AgentHarness) bool
		steers         func(domain.AgentHarness) bool
		want           Outcome
	}{
		{"idle delivers", domain.ActivityIdle, domain.HarnessClaudeCode, nil, steersCodex, Sent},
		{"capability-safe waiting_input delivers", domain.ActivityWaitingInput, domain.HarnessClaudeCode, acceptsClaudeWaiting, steersCodex, Sent},
		{"ambiguous waiting_input suppressed", domain.ActivityWaitingInput, domain.HarnessCodex, acceptsClaudeWaiting, steersCodex, SuppressedAwaitingUser},
		{"waiting_input nil predicate suppressed", domain.ActivityWaitingInput, domain.HarnessClaudeCode, nil, steersCodex, SuppressedAwaitingUser},
		{"blocked suppressed", domain.ActivityBlocked, domain.HarnessCodex, acceptsClaudeWaiting, steersCodex, SuppressedAwaitingUser},
		{"active non-steering suppressed", domain.ActivityActive, domain.HarnessClaudeCode, acceptsClaudeWaiting, steersCodex, SuppressedBusy},
		{"active steering delivers", domain.ActivityActive, domain.HarnessCodex, acceptsClaudeWaiting, steersCodex, Sent},
		{"active nil predicate suppressed", domain.ActivityActive, domain.HarnessCodex, acceptsClaudeWaiting, nil, SuppressedBusy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := record(tc.state, false)
			rec.Harness = tc.harness
			msg := &fakeMessenger{}
			g := New(&fakeStore{rec: rec, ok: true}, msg, nil)
			g.SetInputLease(fixedInputLease(false))

			got, err := g.CoordinationUnderMutation(context.Background(), "s1", "handoff", tc.acceptsWaiting, tc.steers)
			if err != nil {
				t.Fatalf("CoordinationUnderMutation: %v", err)
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

func TestGuard_CoordinationUnderMutationCheckedRejectsBeforeRuntimeWrite(t *testing.T) {
	rec := record(domain.ActivityIdle, false)
	rec.Harness = domain.HarnessKimi
	messenger := &fakeMessenger{}
	g := New(&fakeStore{rec: rec, ok: true}, messenger, nil)
	g.SetInputLease(fixedInputLease(false))
	proofErr := errors.New("generation changed")
	proofCalls := 0

	got, err := g.CoordinationUnderMutationChecked(
		context.Background(),
		"s1",
		"continuation",
		func(domain.AgentHarness) bool { return false },
		func(domain.AgentHarness) bool { return false },
		func(_ context.Context, current domain.SessionRecord) error {
			proofCalls++
			if current.ID != rec.ID {
				t.Fatalf("pre-write record ID = %q, want %q", current.ID, rec.ID)
			}
			return proofErr
		},
	)
	if got != SuppressedUnknown {
		t.Fatalf("outcome = %v, want %v", got, SuppressedUnknown)
	}
	if !errors.Is(err, proofErr) {
		t.Fatalf("error = %v, want wrapped %v", err, proofErr)
	}
	if proofCalls != 1 {
		t.Fatalf("pre-write calls = %d, want 1", proofCalls)
	}
	if len(messenger.sent) != 0 {
		t.Fatalf("runtime writes = %d, want 0", len(messenger.sent))
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
