package main

import (
	"encoding/json"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

func TestNotificationFromActivityMapsNeedsInputWithStableIdentity(t *testing.T) {
	t.Parallel()
	activity := worker.ActivityEvent{
		Harness: "claude-code", Event: "permission-request", State: contract.ActivityBlocked,
		ToolName: "Bash", ToolUseID: "tool-7", AgentSessionID: "agent-1",
	}
	first, ok := notificationFromActivity("session-1", 4, activity)
	if !ok {
		t.Fatal("blocked activity did not produce a notification")
	}
	second, ok := notificationFromActivity("session-1", 4, activity)
	if !ok || second.EventID != first.EventID {
		t.Fatalf("event identity is not stable: %q and %q", first.EventID, second.EventID)
	}
	if first.EventType != "needs_input" || first.WorkerEpoch != 4 {
		t.Fatalf("event = %+v", first)
	}
	var payload struct{ ActivityID, Message string }
	if err := json.Unmarshal(first.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ActivityID != "tool-7" || payload.Message == "" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestNotificationFromActivityOnlyReportsTerminalFailure(t *testing.T) {
	t.Parallel()
	failed, ok := notificationFromActivity("session-1", 4, worker.ActivityEvent{
		Harness: "claude-code", Event: "session-end", State: contract.ActivityExited, AgentSessionID: "agent-1",
	})
	if !ok || failed.EventType != "agent_failed" {
		t.Fatalf("session end = %+v, %v", failed, ok)
	}
	for _, activity := range []worker.ActivityEvent{
		{Harness: "claude-code", Event: "stop", State: contract.ActivityIdle},
		{Harness: "codex", Event: "user-prompt-submit", State: contract.ActivityActive},
	} {
		if event, ok := notificationFromActivity("session-1", 4, activity); ok {
			t.Fatalf("ordinary activity produced notification %+v", event)
		}
	}
}
