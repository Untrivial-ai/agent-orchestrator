package junie

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestJunieNativeSessionIDRejectsValuesThatCannotRoundTrip(t *testing.T) {
	for _, value := range []string{"", " ", " native ", "-native", "native\x00id", "native\n", strings.Repeat("界", 86)} {
		payload, err := json.Marshal(map[string]string{"session_id": value})
		if err != nil {
			t.Fatal(err)
		}
		if got := NativeSessionID(payload); got != "" {
			t.Fatalf("accepted unsafe identity %q", got)
		}
	}
	const valid = "native/session id:日本語"
	payload, err := json.Marshal(map[string]string{"session_id": valid})
	if err != nil {
		t.Fatal(err)
	}
	if got := NativeSessionID(payload); got != valid {
		t.Fatalf("identity changed: %q", got)
	}
}

func TestJunieActivity(t *testing.T) {
	for _, tc := range []struct {
		event, payload string
		want           domain.ActivityState
		ok             bool
	}{
		{"session-start", `{"session_id":"junie-1","source":"startup"}`, "", false},
		{"user-prompt-submit", `{"session_id":"junie-1","prompt":"work"}`, domain.ActivityActive, true},
		{"pre-tool-use", `{"tool_name":"Bash"}`, domain.ActivityActive, true},
		{"permission-request", `{"tool_name":"Bash"}`, domain.ActivityBlocked, true},
		{"stop", `{"last_assistant_message":"done"}`, domain.ActivityIdle, true},
		{"stop-failure", `{"error":"rate_limit"}`, domain.ActivityWaitingInput, true},
		{"session-end", `{"reason":"prompt_input_exit"}`, domain.ActivityExited, true},
		{"unknown", `{}`, "", false},
		{"stop", `{`, "", false},
		{"stop", `null`, "", false},
		{"stop", `[]`, "", false},
		{"stop", ``, "", false},
	} {
		t.Run(tc.event+"/"+tc.payload, func(t *testing.T) {
			got, ok := DeriveActivityState(tc.event, []byte(tc.payload))
			if got != tc.want || ok != tc.ok {
				t.Fatalf("got (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}
