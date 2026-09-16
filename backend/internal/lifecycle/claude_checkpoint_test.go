package lifecycle

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestClaudeQueuedPromptDoesNotCertifyCrossTurnAnswer(t *testing.T) {
	store := newFakeStore()
	store.sessions["qa-1"] = domain.SessionRecord{
		ID: "qa-1", Harness: domain.HarnessClaudeCode, Mode: domain.SessionModeTUI,
		Metadata: domain.SessionMetadata{RuntimeLaunchID: "launch", AgentSessionID: "native", AgentSessionIDLaunchID: "launch"},
	}
	manager := New(store, nil)
	// Observed with Claude 2.1.270: a queued submission carries the RUNNING
	// prompt's ID, not its eventual native user promptId.
	for _, signal := range []ports.ActivitySignal{
		{Event: "user-prompt-submit", ProviderTurnID: "A", LatestUserPrompt: "prompt A", SubmissionID: "submission-A"},
		{Event: "user-prompt-submit", ProviderTurnID: "A", LatestUserPrompt: "queued B", SubmissionID: "submission-B"},
		{Event: "stop", ProviderTurnID: "A", LatestAssistantUpdate: "answer A"},
	} {
		signal.LaunchID, signal.AgentSessionID = "launch", "native"
		if err := manager.ApplyActivitySignal(context.Background(), "qa-1", signal); err != nil {
			t.Fatal(err)
		}
	}
	got := store.sessions["qa-1"].Metadata
	if got.ConversationCheckpointState == domain.ConversationCheckpointComplete {
		t.Fatalf("certified cross-turn checkpoint: prompt=%q answer=%q", got.LatestUserPrompt, got.LatestAssistantUpdate)
	}
}

func TestClaudeLateStopCannotDischargePreUpgradePrompt(t *testing.T) {
	store := newFakeStore()
	store.sessions["qa-1"] = domain.SessionRecord{ID: "qa-1", Mode: domain.SessionModeTUI, Harness: domain.HarnessClaudeCode,
		Metadata: domain.SessionMetadata{RuntimeLaunchID: "launch", AgentSessionID: "native", AgentSessionIDLaunchID: "launch",
			ConversationCheckpointState: domain.ConversationCheckpointPrompt, ConversationCheckpointGeneration: "launch",
			ConversationCheckpointNativeID: "native", LatestUserPrompt: "pending B"}}
	if err := New(store, nil).ApplyActivitySignal(context.Background(), "qa-1", ports.ActivitySignal{
		Event: "stop", ProviderTurnID: "A", LatestAssistantUpdate: "answer A", LaunchID: "launch", AgentSessionID: "native",
	}); err != nil {
		t.Fatal(err)
	}
	var evidence domain.NativeCheckpointEvidence
	if err := json.Unmarshal([]byte(store.sessions["qa-1"].Metadata.NativeCheckpointEvidence), &evidence); err != nil {
		t.Fatal(err)
	}
	if !evidence.Invalid {
		t.Fatalf("late Stop dropped pre-upgrade B: %+v", evidence)
	}
}
