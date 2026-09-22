package controllers_test

import (
	"net/http"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestActivityLaunchFailureEvidence(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		status     int
		code       bool
		cause      domain.LaunchFailureCause
	}{
		{"zero", `{"state":"exited","launchId":"launch","exitCode":0}`, http.StatusOK, true, ""},
		{"negative", `{"state":"exited","exitCode":-1}`, http.StatusOK, false, ""},
		{"active code ignored", `{"state":"active","exitCode":1}`, http.StatusOK, false, ""},
		{"explicit resume", `{"state":"exited","launchId":"launch","agentSessionId":"native","launchFailureCause":"resume_invalid"}`, http.StatusOK, false, domain.LaunchFailureResumeInvalid},
		{"unknown cause", `{"state":"exited","launchFailureCause":"guessed"}`, http.StatusBadRequest, false, ""},
		{"wrong state", `{"state":"active","launchFailureCause":"resume_invalid"}`, http.StatusBadRequest, false, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := &fakeActivityRecorder{}
			srv := newActivityTestServer(t, rec)
			body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/activity", tt.body)
			if status != tt.status {
				t.Fatalf("status=%d body=%s", status, body)
			}
			if status != http.StatusOK {
				if rec.calls != 0 {
					t.Fatal("invalid request reached lifecycle")
				}
				return
			}
			if (rec.gotSignal.ExitCode != nil) != tt.code || rec.gotSignal.LaunchFailureCause != tt.cause {
				t.Fatalf("signal=%+v", rec.gotSignal)
			}
			if tt.code && *rec.gotSignal.ExitCode != 0 {
				t.Fatalf("code=%d", *rec.gotSignal.ExitCode)
			}
		})
	}
}
