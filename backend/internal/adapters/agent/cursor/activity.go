package cursor

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

// DeriveActivityState maps a Cursor hook event onto an AO activity state.
//
// event is the AO hook sub-command name installed in cursorManagedHooks
// ("user-prompt-submit", "pre-tool-use", ...), NOT the native Cursor event
// name.
//
// cursor-agent is always launched with --trust, so beforeShellExecution/
// beforeMCPExecution (and their after* counterparts) never pause for a real
// permission decision — they fire on every auto-approved tool call. They are
// therefore treated like claude-code's PreToolUse/PostToolUse trio (active),
// not like a permission dialog. Unlike claude-code, Cursor exposes no
// separate hook that fires only for a genuine blocking dialog, so a cursor
// session never enters waiting_input/blocked: there is no signal AO could use
// to know one is happening.
func DeriveActivityState(event string, _ []byte) (domain.ActivityState, bool) {
	switch event {
	case "session-start":
		return domain.ActivityActive, true
	case "user-prompt-submit":
		return domain.ActivityActive, true
	case "pre-tool-use", "post-tool-use":
		return domain.ActivityActive, true
	case "stop":
		return domain.ActivityIdle, true
	default:
		return "", false
	}
}
