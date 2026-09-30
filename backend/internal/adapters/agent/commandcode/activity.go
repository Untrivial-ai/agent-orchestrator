package commandcode

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

// DeriveActivityState maps Command Code's available lifecycle hooks onto AO
// activity. Command Code exposes no permission-request hook, so the adapter
// cannot distinguish a tool approval prompt from other in-turn work.
//
// session-start is deliberately absent. Command Code fires it on resume and
// clear as well as on initial startup, and a native restore delivers no prompt
// (see GetRestoreCommand), so mapping it to Active would flip a restored session
// sitting at an empty prompt to "working" with no Stop until the user's next
// turn. The hook still runs: activitydispatch captures the native session id
// from it and the adapter injects AO's standing instructions as SessionStart
// context. Only in-turn hooks drive activity.
func DeriveActivityState(event string, _ []byte) (domain.ActivityState, bool) {
	switch event {
	case "pre-tool-use", "post-tool-use":
		return domain.ActivityActive, true
	case "stop":
		return domain.ActivityIdle, true
	default:
		return "", false
	}
}
