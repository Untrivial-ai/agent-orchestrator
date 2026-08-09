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

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// SessionReader is the single store read the guard needs: the session's
// current liveness and activity state.
type SessionReader interface {
	GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error)
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
	// safely steer an active turn (NudgeCoordination only).
	SuppressedBusy
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
	default:
		return "suppressed_unknown"
	}
}

// Guard is the guarded pane-write primitive shared by the session manager and
// lifecycle. It takes no locks of its own, so callers may hold theirs across a
// call (lifecycle's sendOnce calls it under react.mu). It implements
// ports.AgentMessenger (via Send) so it can transparently replace a raw
// messenger wherever only the error matters.
type Guard struct {
	store     SessionReader
	messenger ports.AgentMessenger
	logger    *slog.Logger
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

// refuseWhenBlocked is the shared policy for writes that are safe to submit
// at an idle needs-input prompt but not while the session is blocked on a
// pending decision: pasting there risks answering the dialog on the user's
// behalf, the unrecoverable-write hazard this package exists to prevent.
func refuseWhenBlocked(rec domain.SessionRecord) (Outcome, bool) {
	return SuppressedAwaitingUser, rec.Activity.State == domain.ActivityBlocked
}

// Deliver writes a user-initiated message (or its Enter-only re-submit: an
// empty msg) into a live agent. Its activity-specific policy refuses when the
// session is blocked on a pending decision — waiting_input does NOT suppress, because an agent
// sitting at an idle prompt is exactly where a user message (or the Enter that
// submits its unsent draft) belongs.
func (g *Guard) Deliver(ctx context.Context, id domain.SessionID, msg string) (Outcome, error) {
	return g.send(ctx, id, msg, refuseWhenBlocked)
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

// NudgeUrgent writes an AO-initiated message that must reach the agent even
// while the session sits idle at a needs-input prompt (waiting_input) —
// reserved for alerts where a human parked at that prompt may be exactly who
// needs to act (e.g. a merge conflict needing a rebase or a redirected
// agent), unlike a routine reaction nudge that can simply wait for the agent
// to resume on its own. It still refuses while the session is blocked on a
// live permission decision, the same as Deliver: an unsolicited write there
// risks answering the dialog rather than merely being read late.
func (g *Guard) NudgeUrgent(ctx context.Context, id domain.SessionID, msg string) (Outcome, error) {
	return g.send(ctx, id, msg, refuseWhenBlocked)
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
	if err := g.messenger.Send(ctx, id, msg); err != nil {
		return Sent, fmt.Errorf("guard %s: send: %w", id, err)
	}
	return Sent, nil
}
