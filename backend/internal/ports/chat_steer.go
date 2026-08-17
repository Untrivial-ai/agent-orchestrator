package ports

import (
	"context"
	"errors"
)

// Steering: guidance delivered INTO a turn that is already running.
//
// This is not interrupt-then-resend. An interrupt throws away the turn — its
// reasoning, its half-finished tool calls, the command it has running — and the
// resend starts the work over from a cold start. A steer leaves all of it in place
// and adds the user's correction to the same turn, so the agent keeps its context
// and decides for itself what to abandon. For a user who realizes mid-turn that
// they asked for the wrong thing, that difference is the whole feature.
//
// Optional, like every other capability that not all providers have: a driver that
// cannot do it simply does not implement ChatSteerer, and AO hides the affordance
// rather than offering a control that fails.

// ChatSteerer is implemented by drivers whose provider accepts guidance into a
// running turn.
//
// providerTurnID is a precondition, not a hint: the caller names the turn it
// believes is in flight, and a driver must fail rather than redirect the guidance
// if the provider disagrees. Silently steering a different turn would put the
// user's correction on work they were not looking at.
//
// The returned ref is the turn that absorbed the guidance. Against Codex
// 0.146.0 that is the same turn id, but it is returned rather than assumed: a
// provider that answered with a new turn would otherwise leave AO attributing the
// steer to the wrong one.
type ChatSteerer interface {
	Steer(ctx context.Context, providerTurnID string, msg ChatUserMessage) (ChatTurnRef, error)
}

// Errors a ChatSteerer returns. Both are ordinary outcomes rather than failures,
// and they are distinct because the advice a client should give differs: one means
// "send this as a new message instead", the other means "wait, then try again".
var (
	// ErrChatNoSteerableTurn means there is no turn in flight for the guidance to
	// join, or the named turn is no longer the active one.
	//
	// Deliberately not ErrChatNoActiveTurn: that sentinel is documented as an
	// interrupt finding nothing to cancel, and reads back to the user as a message
	// about stopping the agent. It is also reachable at a moment interrupt never
	// sees — Codex refuses a steer for a turn it has accepted but not yet
	// announced, so this fires in the window between turn/start returning and the
	// turn actually starting.
	ErrChatNoSteerableTurn = errors.New("no active turn to steer")
	// ErrChatTurnNotSteerable means a turn IS running but its kind cannot absorb
	// guidance. Codex refuses a compaction or a review turn this way: those are
	// machine-driven turns with no room for a user's correction.
	ErrChatTurnNotSteerable = errors.New("the running turn cannot be steered")
	// ErrChatSteerContentUnsupported means the driver cannot deliver every prompt
	// block supplied for this steer. Drivers must reject before sending rather than
	// silently dropping attachments.
	ErrChatSteerContentUnsupported = errors.New("steer content is unsupported")
)
