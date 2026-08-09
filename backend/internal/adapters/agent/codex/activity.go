package codex

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

// DeriveActivityState maps a Codex hook event onto an AO activity state. The
// bool is false when the event carries no activity signal.
//
// event is the AO hook sub-command name installed in codexManagedHooks
// ("user-prompt-submit", "pre-tool-use", "post-tool-use", ...), not the
// native Codex event name. Codex currently has no SessionEnd/Notification
// equivalent in the adapter, so runtime exit still falls back to the reaper.
func DeriveActivityState(event string, _ []byte) (domain.ActivityState, bool) {
	switch event {
	case "user-prompt-submit":
		return domain.ActivityActive, true
	case "pre-tool-use":
		return domain.ActivityBlocked, true
	case "post-tool-use":
		return domain.ActivityActive, true
	case "permission-request":
		// Permission prompts remain waiting_input. ActivityBlocked is reserved
		// for the paired native request_user_input lifecycle above.
		return domain.ActivityWaitingInput, true
	case "stop":
		return domain.ActivityIdle, true
	default:
		return "", false
	}
}
