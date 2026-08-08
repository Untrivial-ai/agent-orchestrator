// Package sessionguard owns the one invariant every write into a live
// session's pane must satisfy: re-read the session immediately before writing
// and refuse when the paste could land somewhere only the user may act. The
// runtime appends Enter after every paste, so a write into a session paused on
// a permission/approval dialog would answer the decision on the user's behalf
// — an unrecoverable action, unlike a skipped message which callers re-attempt
// or surface. Every pane-writing path (user sends, post-send Enter nudges,
// lifecycle reaction nudges) funnels through this guard so the stale-state
// check lives in one tested place instead of being re-derived per call-site.
package sessionguard

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// SessionReader is the single store read the guard needs: the session's
// current liveness and activity state.
type SessionReader interface {
	GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error)
}

// InputLease is the late-bound admission authority shared by every path that
// can write bytes into an agent pane. A successful acquisition must be held
// until the underlying pane write returns. Session mutations close admission
// first and then wait for already-issued leases to drain, eliminating the
// check-then-write race that a boolean "input allowed" query cannot avoid.
type InputLease interface {
	AcquireSessionInput(id domain.SessionID) (release func(), ok bool)
}

// Outcome reports what a guarded write did. Anything other than Sent means the
// message did NOT reach the pane; callers that record delivery must not stamp
// a suppressed write as delivered.
type Outcome int

const (
	// SuppressedUnknown is returned when the pre-write session read failed, so
	// the state is unknown and the guard failed closed. Deliberately the zero
	// value — a forgotten assignment must never read as a successful send.
	SuppressedUnknown Outcome = iota
	// Sent means the message was written to the session's pane (a messenger
	// failure surfaces as Sent plus a non-nil error: the write was attempted).
	Sent
	// SuppressedNotFound means no session row exists for the id.
	SuppressedNotFound
	// SuppressedTerminated means the session is terminated; its pane is gone
	// or about to be reaped.
	SuppressedTerminated
	// SuppressedExited means the managed pane remains available but its agent
	// process exited. The pane may now contain an interactive shell, so writing
	// an agent prompt would execute it as shell input.
	SuppressedExited
	// SuppressedAwaitingUser means the session awaits the human — blocked on a
	// permission decision (Deliver and Nudge), or waiting at the prompt for
	// the next instruction (Nudge only).
	SuppressedAwaitingUser
	// SuppressedBusy means the session is mid-turn on a harness that cannot
	// safely steer an active turn (coordination writes only).
	SuppressedBusy
	// SuppressedInputGated means an exclusive session mutation (for example an
	// agent switch, resume, restore, or kill) closed pane-input admission before
	// this write acquired a lease.
	SuppressedInputGated
)

// String names the outcome for logs.
func (o Outcome) String() string {
	switch o {
	case Sent:
		return "sent"
	case SuppressedNotFound:
		return "suppressed_not_found"
	case SuppressedTerminated:
		return "suppressed_terminated"
	case SuppressedExited:
		return "suppressed_exited"
	case SuppressedAwaitingUser:
		return "suppressed_awaiting_user"
	case SuppressedBusy:
		return "suppressed_busy"
	case SuppressedInputGated:
		return "suppressed_input_gated"
	default:
		return "suppressed_unknown"
	}
}

// Guard is the guarded pane-write primitive shared by the session manager and
// lifecycle. Its small lease lock only protects late binding; it is never held
// across a store read or pane write. It implements
// ports.AgentMessenger (via Send) so it can transparently replace a raw
// messenger wherever only the error matters.
type Guard struct {
	store     SessionReader
	messenger ports.AgentMessenger
	logger    *slog.Logger

	leaseMu sync.RWMutex
	lease   InputLease
}

var _ ports.AgentMessenger = (*Guard)(nil)

// New builds a Guard over the store it re-reads and the messenger it writes
// through. A nil logger falls back to slog.Default().
func New(store SessionReader, messenger ports.AgentMessenger, logger *slog.Logger) *Guard {
	if logger == nil {
		logger = slog.Default()
	}
	return &Guard{store: store, messenger: messenger, logger: logger}
}

// SetInputLease late-binds the process-wide pane-input admission authority.
// This is needed by lifecycle, which is constructed before Session Manager at
// daemon boot. Nil restores the legacy always-admitted behavior.
func (g *Guard) SetInputLease(lease InputLease) {
	g.leaseMu.Lock()
	g.lease = lease
	g.leaseMu.Unlock()
}

// Send satisfies ports.AgentMessenger so a Guard can sit in for the raw
// messenger. It applies the Deliver policy but FOLDS a suppressed outcome into
// nil: a caller that learns only "did Send error?" cannot tell that the write
// was actually refused. That is fine for callers that only need a best-effort
// delivery, but paths whose success CONTRACT depends on the write landing
// (after-start prompt delivery in Spawn/Restore) must call Deliver directly and
// map non-Sent outcomes to an error, or a session that terminates or blocks
// before injection is reported as a successful spawn with a prompt that was
// never delivered.
func (g *Guard) Send(ctx context.Context, id domain.SessionID, msg string) error {
	_, err := g.Deliver(ctx, id, msg)
	return err
}

// Deliver writes a user-initiated message (or its Enter-only re-submit: an
// empty msg) into a live agent. Its activity-specific policy refuses when the
// session is blocked on a pending decision — waiting_input does NOT suppress, because an agent
// sitting at an idle prompt is exactly where a user message (or the Enter that
// submits its unsent draft) belongs.
func (g *Guard) Deliver(ctx context.Context, id domain.SessionID, msg string) (Outcome, error) {
	return g.send(ctx, id, msg, func(rec domain.SessionRecord) (Outcome, bool) {
		return SuppressedAwaitingUser, rec.Activity.State == domain.ActivityBlocked
	})
}

// DeliverWithPostWrite is Deliver plus a callback that runs only after a
// successful pane write and while the same input lease is still held. Session
// Manager uses it to persist narrow message facts before a provider switch can
// close admission and snapshot handoff context.
func (g *Guard) DeliverWithPostWrite(ctx context.Context, id domain.SessionID, msg string, after func(context.Context) error) (Outcome, error) {
	return g.sendThen(ctx, id, msg, func(rec domain.SessionRecord) (Outcome, bool) {
		return SuppressedAwaitingUser, rec.Activity.State == domain.ActivityBlocked
	}, after)
}

// DeliverUnderMutation applies the same just-in-time session safety checks as
// Deliver but intentionally bypasses input admission. It is only for an AO
// mutation that already owns the session's exclusive operation fence and must
// write its own handoff or startup prompt while ordinary input stays gated.
func (g *Guard) DeliverUnderMutation(ctx context.Context, id domain.SessionID, msg string) (Outcome, error) {
	return g.sendAdmitted(ctx, id, msg, func(rec domain.SessionRecord) (Outcome, bool) {
		return SuppressedAwaitingUser, rec.Activity.State == domain.ActivityBlocked
	})
}

// CoordinationUnderMutation writes an AO coordination message while the caller
// owns the session's exclusive mutation fence. It intentionally bypasses the
// ordinary input lease (which the mutation has already closed), but re-reads
// activity at the pane-write boundary. Idle is always safe. waiting_input is
// safe only when the harness can distinguish an idle composer from a blocked
// decision; blocked is never safe, and active is safe only when the current
// harness explicitly supports mid-turn steering.
func (g *Guard) CoordinationUnderMutation(
	ctx context.Context,
	id domain.SessionID,
	msg string,
	acceptsWaitingInput func(domain.AgentHarness) bool,
	steersActiveTurn func(domain.AgentHarness) bool,
) (Outcome, error) {
	return g.coordinationUnderMutation(ctx, id, msg, acceptsWaitingInput, steersActiveTurn, nil)
}

// CoordinationUnderMutationChecked adds one caller-owned proof immediately
// after the durable session/activity read and immediately before the runtime
// write. Agent switching uses it to revalidate the exact runtime handle and
// generation at the pane-write boundary. The runtime's safe exit sink remains
// the final defense if the child exits during a chunked paste.
func (g *Guard) CoordinationUnderMutationChecked(
	ctx context.Context,
	id domain.SessionID,
	msg string,
	acceptsWaitingInput func(domain.AgentHarness) bool,
	steersActiveTurn func(domain.AgentHarness) bool,
	preWrite func(context.Context, domain.SessionRecord) error,
) (Outcome, error) {
	return g.coordinationUnderMutation(ctx, id, msg, acceptsWaitingInput, steersActiveTurn, preWrite)
}

func (g *Guard) coordinationUnderMutation(
	ctx context.Context,
	id domain.SessionID,
	msg string,
	acceptsWaitingInput func(domain.AgentHarness) bool,
	steersActiveTurn func(domain.AgentHarness) bool,
	preWrite func(context.Context, domain.SessionRecord) error,
) (Outcome, error) {
	return g.sendAdmittedChecked(ctx, id, msg, func(rec domain.SessionRecord) (Outcome, bool) {
		switch rec.Activity.State {
		case domain.ActivityIdle:
			return SuppressedUnknown, false
		case domain.ActivityWaitingInput:
			return SuppressedAwaitingUser, acceptsWaitingInput == nil || !acceptsWaitingInput(rec.Harness)
		case domain.ActivityBlocked:
			return SuppressedAwaitingUser, true
		case domain.ActivityActive:
			return SuppressedBusy, steersActiveTurn == nil || !steersActiveTurn(rec.Harness)
		default:
			return SuppressedUnknown, true
		}
	}, preWrite)
}

// Nudge writes an AO-initiated (unsolicited) message into a live agent. Its
// activity-specific policy refuses whenever the session awaits the human — blocked on a
// decision or waiting at the prompt — because an automated paste+Enter there
// either answers a dialog or submits text the user never saw.
func (g *Guard) Nudge(ctx context.Context, id domain.SessionID, msg string) (Outcome, error) {
	return g.send(ctx, id, msg, func(rec domain.SessionRecord) (Outcome, bool) {
		return SuppressedAwaitingUser, rec.Activity.State.NeedsInput()
	})
}

// NudgeCoordination writes an AO-initiated coordination message under the full
// delivery policy, re-evaluated here — at the write boundary — rather than from
// a caller's earlier snapshot. It refuses whenever the session awaits the human,
// and additionally while it is mid-turn on a harness that cannot safely steer an
// active turn. steersActiveTurn is the adapter-provided capability; a nil
// predicate is treated as "cannot steer", so an unknown harness never takes an
// unsolicited write during a live turn.
func (g *Guard) NudgeCoordination(ctx context.Context, id domain.SessionID, msg string, steersActiveTurn func(domain.AgentHarness) bool) (Outcome, error) {
	return g.send(ctx, id, msg, func(rec domain.SessionRecord) (Outcome, bool) {
		if rec.Activity.State.NeedsInput() {
			return SuppressedAwaitingUser, true
		}
		if rec.Activity.State == domain.ActivityActive {
			return SuppressedBusy, steersActiveTurn == nil || !steersActiveTurn(rec.Harness)
		}
		return SuppressedUnknown, false
	})
}

// send re-reads the session immediately before pasting so the window between
// "state looked safe" and "bytes hit the pane" is as small as this process can
// make it. It is not atomic against the agent itself — a dialog can still
// appear mid-paste — but the just-in-time read is the strongest guarantee
// available without scraping the terminal. Fail closed: a store error
// suppresses the write rather than pressing Enter on an unknown state.
func (g *Guard) send(ctx context.Context, id domain.SessionID, msg string, refuse func(domain.SessionRecord) (Outcome, bool)) (Outcome, error) {
	return g.sendThen(ctx, id, msg, refuse, nil)
}

func (g *Guard) sendThen(ctx context.Context, id domain.SessionID, msg string, refuse func(domain.SessionRecord) (Outcome, bool), after func(context.Context) error) (Outcome, error) {
	g.leaseMu.RLock()
	lease := g.lease
	g.leaseMu.RUnlock()
	if lease != nil {
		release, ok := lease.AcquireSessionInput(id)
		if !ok {
			g.logger.Info("sessionguard: write suppressed", "sessionID", id, "reason", SuppressedInputGated.String())
			return SuppressedInputGated, nil
		}
		defer release()
	}
	outcome, err := g.sendAdmitted(ctx, id, msg, refuse)
	if err == nil && outcome == Sent && after != nil {
		if afterErr := after(ctx); afterErr != nil {
			return outcome, afterErr
		}
	}
	return outcome, err
}

// sendAdmitted performs the durable safety read and the actual pane write. A
// caller reaching it through send holds its input lease for this entire span.
func (g *Guard) sendAdmitted(ctx context.Context, id domain.SessionID, msg string, refuse func(domain.SessionRecord) (Outcome, bool)) (Outcome, error) {
	return g.sendAdmittedChecked(ctx, id, msg, refuse, nil)
}

func (g *Guard) sendAdmittedChecked(ctx context.Context, id domain.SessionID, msg string, refuse func(domain.SessionRecord) (Outcome, bool), preWrite func(context.Context, domain.SessionRecord) error) (Outcome, error) {
	rec, ok, err := g.store.GetSession(ctx, id)
	if err != nil {
		return SuppressedUnknown, fmt.Errorf("guard %s: read session: %w", id, err)
	}
	if !ok {
		g.logger.Info("sessionguard: write suppressed", "sessionID", id, "reason", "not_found")
		return SuppressedNotFound, nil
	}
	if rec.IsTerminated {
		g.logger.Info("sessionguard: write suppressed", "sessionID", id, "reason", "terminated")
		return SuppressedTerminated, nil
	}
	if rec.Activity.State == domain.ActivityExited {
		g.logger.Info("sessionguard: write suppressed", "sessionID", id, "reason", "agent_exited")
		return SuppressedExited, nil
	}
	if outcome, deny := refuse(rec); deny {
		g.logger.Info("sessionguard: write suppressed", "sessionID", id, "reason", outcome.String(), "state", string(rec.Activity.State))
		return outcome, nil
	}
	if preWrite != nil {
		if err := preWrite(ctx, rec); err != nil {
			return SuppressedUnknown, fmt.Errorf("guard %s: pre-write check: %w", id, err)
		}
	}
	if err := g.messenger.Send(ctx, id, msg); err != nil {
		return Sent, fmt.Errorf("guard %s: send: %w", id, err)
	}
	return Sent, nil
}
