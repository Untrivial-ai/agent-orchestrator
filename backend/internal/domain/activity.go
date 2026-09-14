package domain

import "time"

// ActivityState is how busy the agent is, reported via the agent's CLI hook
// callbacks, not inferred from transcript/JSONL
type ActivityState string

// Activity states. WaitingInput and Blocked are sticky (see IsSticky).
//
// WaitingInput and Blocked both mean "paused on the user" but demand opposite
// automation: waiting_input is an agent at an empty prompt awaiting its next
// INSTRUCTION (safe to message or nudge), while blocked is an agent stopped on
// a pending DECISION — a tool-permission or approval dialog — where a stray
// keystroke could answer the dialog on the user's behalf. Automated senders
// must never inject input into a blocked session. (Not to be confused with the
// PR-stack Blocked flag in the status read model; blocked here predates it —
// the state existed in the original activity model and returns with the
// permission-prompt producers.)
const (
	ActivityActive       ActivityState = "active"
	ActivityIdle         ActivityState = "idle"
	ActivityWaitingInput ActivityState = "waiting_input"
	ActivityBlocked      ActivityState = "blocked"
	ActivityExited       ActivityState = "exited"
)

// IsSticky reports whether an activity state must NOT be aged/demoted by the
// passage of time (a paused agent is still paused until a new signal says so).
func (a ActivityState) IsSticky() bool {
	return a == ActivityWaitingInput || a == ActivityBlocked
}

// NeedsInput reports whether the agent is paused on the user — waiting for the
// next instruction (waiting_input) or blocked on a decision (blocked). Both
// render as the needs_input session status. Distinct from IsSticky: stickiness
// is about time-demotion, NeedsInput about the user being the unblocker.
func (a ActivityState) NeedsInput() bool {
	return a == ActivityWaitingInput || a == ActivityBlocked
}

// Activity captures the persisted activity reading: the state and when it was
// last observed.
type Activity struct {
	State          ActivityState `json:"state"`
	LastActivityAt time.Time     `json:"lastActivityAt"`
}
