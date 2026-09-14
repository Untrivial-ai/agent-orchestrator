package chat

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// completedReplay is what ACP session/load reproduces for a settled provider
// thread: one completed turn, its user prompt, and its answer.
func completedReplay() []ports.ChatEvent {
	return []ports.ChatEvent{
		{
			Kind: ports.ChatEventUserMessageCompleted, ProviderEventID: "history-user",
			ProviderTurnID: "native-turn-1", ProviderItemID: "native-user-1", Text: "Say hi",
		},
		{
			Kind: ports.ChatEventMessageCompleted, ProviderEventID: "history-answer",
			ProviderTurnID: "native-turn-1", ProviderItemID: "native-answer-1", Text: "Hi!",
		},
		{
			Kind: ports.ChatEventTurnCompleted, ProviderEventID: "history-complete",
			ProviderTurnID: "native-turn-1", TurnState: domain.TurnStateCompleted,
		},
	}
}

// poisonedRows mirrors a session whose newest hook fact came from a turn the
// provider never settled: the prompt is durable in AO, but session/load replays
// only the completed turn before it.
func poisonedRows(state domain.TurnState) ([]domain.ConversationTurn, []domain.ConversationMessage) {
	base := time.Date(2026, 9, 7, 14, 33, 20, 0, time.UTC)
	turns := []domain.ConversationTurn{
		{
			ID: "completed-turn", HandledBySessionID: testCheckpointSession,
			ProviderTurnID: "native-turn-1", State: domain.TurnStateCompleted, RequestedAt: base,
		},
		{
			ID: "unsettled-turn", HandledBySessionID: testCheckpointSession,
			State: state, RequestedAt: base.Add(2 * time.Minute),
		},
	}
	messages := []domain.ConversationMessage{
		{
			TurnID: "completed-turn", Sequence: 1, Role: domain.MessageRoleUser,
			Text: "Say hi", ProviderItemID: "native-user-1",
		},
		{
			TurnID: "completed-turn", Sequence: 2, Role: domain.MessageRoleAssistant,
			Text: "Hi!", ProviderItemID: "native-answer-1",
		},
		{TurnID: "unsettled-turn", Sequence: 3, Role: domain.MessageRoleUser, Text: "Say hi to"},
	}
	return turns, messages
}

const testCheckpointSession = domain.SessionID("checkpoint-session")

// A prompt AO recorded on a cancelled or interrupted turn is not something the
// provider promises to replay, so gating the native-history import on it can
// never be satisfied: the settle loop burns its full budget and the interface
// transition rolls back to Terminal for good. See #4424.
func TestCheckpointIgnoresPromptFromUnsettledTurn(t *testing.T) {
	for _, state := range []domain.TurnState{
		domain.TurnStateCancelled,
		domain.TurnStateInterrupted,
		domain.TurnStateFailed,
	} {
		t.Run(string(state), func(t *testing.T) {
			turns, messages := poisonedRows(state)
			checkpoint := nativeHistoryCheckpoint{latestUserPrompt: "Say hi to"}
			checkpoint.captureAOHighWater(testCheckpointSession, turns, messages, nil)

			if !checkpoint.reached(completedReplay()) {
				t.Fatalf("replay checkpoint gates on a %s turn's prompt; TUI-to-Chat would "+
					"time out after nativeHistorySettleLimit and roll back", state)
			}
		})
	}
}

// The guard must stay narrow: a prompt from a completed turn is still a hard
// gate, so a replay that has not caught up with settled work is rejected.
func TestCheckpointStillGatesOnCompletedPrompt(t *testing.T) {
	base := time.Date(2026, 9, 7, 14, 33, 20, 0, time.UTC)
	turns := []domain.ConversationTurn{{
		ID: "completed-turn", HandledBySessionID: testCheckpointSession,
		ProviderTurnID: "native-turn-1", State: domain.TurnStateCompleted, RequestedAt: base,
	}}
	messages := []domain.ConversationMessage{{
		TurnID: "completed-turn", Sequence: 1, Role: domain.MessageRoleUser,
		Text: "Say hi", ProviderItemID: "native-user-1",
	}}

	checkpoint := nativeHistoryCheckpoint{latestUserPrompt: "Run the final verification."}
	checkpoint.captureAOHighWater(testCheckpointSession, turns, messages, nil)

	if checkpoint.reached(completedReplay()) {
		t.Fatal("a settled prompt the replay has not reached must keep gating the import")
	}
}
