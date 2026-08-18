// Package activitystate holds the standard mapping from an AO hook sub-command
// name onto an activity state. Most adapters install the same
// session-start/user-prompt-submit/stop/permission-request callbacks and derive
// activity identically from the event name alone; they share this deriver rather
// than each carrying a copy. Adapters that inspect the hook payload for finer
// grained state (claude-code, codex, droid) keep their own deriver.
package activitystate

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

// StandardDeriveActivityState maps a hook sub-command name onto an AO activity
// state. The bool is false when the event carries no activity signal. The
// payload is ignored: this is the name-only mapping shared by adapters whose
// hooks report activity purely through which callback fired.
//
//   - session-start / user-prompt-submit → active
//   - tool-use                           → active
//   - stop                               → idle
//   - permission-request                 → waiting_input
//
// tool-use is for a "before/around a tool invocation" hook: an agent about to
// run a tool is actively working, not blocked on the user. It exists so an
// adapter whose only tool-adjacent hook fires before every tool (not just ones
// that prompt for approval) does not have to misroute that event through
// permission-request, which would pin an actively-working session to the sticky
// waiting_input state. It mirrors how the agy adapter treats tool activity
// (after-tool → active). permission-request stays reserved for events that
// genuinely gate on a user decision (e.g. Cursor's beforeShellExecution).
//
// permission-request maps to waiting_input, not blocked: none of the sharing
// adapters install the pre/post-tool-use trio, so a blocked state could never
// be cleared before the turn ends. waiting_input still suppresses automated
// nudges (NeedsInput) while leaving user-initiated sends deliverable.
func StandardDeriveActivityState(event string, _ []byte) (domain.ActivityState, bool) {
	switch event {
	case "session-start":
		return domain.ActivityActive, true
	case "user-prompt-submit":
		return domain.ActivityActive, true
	case "tool-use":
		return domain.ActivityActive, true
	case "stop":
		return domain.ActivityIdle, true
	case "permission-request":
		return domain.ActivityWaitingInput, true
	default:
		return "", false
	}
}
