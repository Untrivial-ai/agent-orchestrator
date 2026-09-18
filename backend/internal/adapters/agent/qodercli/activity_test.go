package qodercli

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestDeriveActivityStateHandlesElicitationNotifications(t *testing.T) {
	// Qoder CLI's AskUserQuestion form parks the turn on a human answer. Claude
	// Code has no such notification, so its deriver reports nothing and the
	// session would keep reading active while a form waits.
	state, ok := DeriveActivityState("notification", []byte(`{"notification_type":"elicitation_dialog"}`))
	if !ok || state != domain.ActivityWaitingInput {
		t.Fatalf("elicitation_dialog = %q,%v want waiting_input", state, ok)
	}

	for _, event := range []string{"elicitation_response", "elicitation_complete"} {
		state, ok := DeriveActivityState("notification", []byte(`{"notification_type":"`+event+`"}`))
		if !ok || state != domain.ActivityActive {
			t.Fatalf("%s = %q,%v want active", event, state, ok)
		}
	}
}

func TestDeriveActivityStateDelegatesTheClaudeShapedVocabulary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		event   string
		payload string
		want    domain.ActivityState
		ok      bool
	}{
		{name: "prompt submitted", event: "user-prompt-submit", want: domain.ActivityActive, ok: true},
		{name: "tool running", event: "pre-tool-use", want: domain.ActivityActive, ok: true},
		{name: "permission dialog", event: "permission-request", want: domain.ActivityBlocked, ok: true},
		{name: "turn ended", event: "stop", want: domain.ActivityIdle, ok: true},
		{name: "idle notification", event: "notification", payload: `{"notification_type":"idle_prompt"}`, want: domain.ActivityIdle, ok: true},
		{name: "permission notification", event: "notification", payload: `{"notification_type":"permission_prompt"}`, want: domain.ActivityBlocked, ok: true},
		{name: "session end", event: "session-end", payload: `{"reason":"prompt_input_exit"}`, want: domain.ActivityExited, ok: true},
		{name: "clear keeps the session", event: "session-end", payload: `{"reason":"clear"}`, ok: false},
		{name: "session start is metadata only", event: "session-start", ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, ok := DeriveActivityState(tc.event, []byte(tc.payload))
			if ok != tc.ok || state != tc.want {
				t.Fatalf("%s = %q,%v want %q,%v", tc.event, state, ok, tc.want, tc.ok)
			}
		})
	}
}
