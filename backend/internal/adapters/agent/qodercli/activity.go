package qodercli

import (
	"encoding/json"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/claudecode"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// DeriveActivityState maps a Qoder CLI hook callback onto an AO activity state.
//
// Qoder CLI's hook event names, payload fields, notification types and
// session-end reasons are Claude Code's, so everything delegates to
// claudecode.DeriveActivityState. The exception is the notification family
// Claude has no equivalent for: Qoder CLI raises elicitation notifications when
// its AskUserQuestion tool puts a form in front of the user. Claude's deriver
// reports no signal for those, which would leave the session reading active
// while it is really parked on a human answer — and an automated Enter aimed at
// a stuck composer would answer the form instead.
func DeriveActivityState(event string, payload []byte) (domain.ActivityState, bool) {
	if event == "notification" {
		switch notificationType(payload) {
		case "elicitation_dialog":
			// A form is up and only a person can clear it.
			return domain.ActivityWaitingInput, true
		case "elicitation_response", "elicitation_complete":
			// Answered: the turn continues under the agent's own power.
			return domain.ActivityActive, true
		}
	}
	return claudecode.DeriveActivityState(event, payload)
}

func notificationType(payload []byte) string {
	var parsed struct {
		NotificationType string `json:"notification_type"`
	}
	_ = json.Unmarshal(payload, &parsed)
	return parsed.NotificationType
}
