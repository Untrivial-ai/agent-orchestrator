package aider

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

// DeriveActivityState maps Aider's completion notification onto the only
// native activity transition it exposes. Aider runs the notification command
// after an LLM attempt returns and immediately before presenting user input, so
// the session is waiting for the user's next instruction. It does not expose a
// corresponding prompt-start callback; callers must classify this as partial
// signal coverage rather than a complete lifecycle pipeline.
func DeriveActivityState(event string, _ []byte) (domain.ActivityState, bool) {
	if event == "notification" {
		return domain.ActivityWaitingInput, true
	}
	return "", false
}
