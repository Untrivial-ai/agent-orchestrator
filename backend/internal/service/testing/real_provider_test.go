package testing

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestSessionQueryToolValidatesClosedSelector(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{"resource":"reviews","sessionId":"worker-42"}`, true},
		{`{"resource":"conversation","sessionId":"worker_42"}`, true},
		{`{"resource":"reviews"}`, false},
		{`{"resource":"conversation","sessionId":"../other"}`, false},
		{`{"resource":"reviews","sessionId":"worker%2fother"}`, false},
		{`{"resource":"projects","sessionId":"worker"}`, false},
		{`{"resource":"reviews","sessionId":"worker","url":"http://host"}`, false},
	} {
		_, _, err := decodeInput(json.RawMessage(tc.body), "target_daemon_query")
		if (err == nil) != tc.valid {
			t.Errorf("%s: %v", tc.body, err)
		}
	}
}

func TestStartForwardsInvestigatorProfileToWorker(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Cancel(context.Background(), f.start.AttemptID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := f.svc.WaitCleanup(ctx, f.start.AttemptID); err != nil {
		t.Fatal(err)
	}
	result, err := f.svc.StartAttempt(context.Background(), f.run.ID, StartAttemptInput{Harness: domain.HarnessClaudeCode, Model: "claude-opus-5-5", Effort: "medium", WorkerPrompt: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	f.start = result
	if f.worker.request.Model != "claude-opus-5-5" || f.worker.request.Effort != "medium" || f.worker.request.Harness != domain.HarnessClaudeCode {
		t.Fatalf("lost profile: %+v", f.worker.request)
	}
}
