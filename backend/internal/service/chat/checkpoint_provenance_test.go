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

func TestCheckpointCompletedCoordinationStillAnchorsNewProvider(t *testing.T) {
	turns, messages := poisonedRows(domain.TurnStateCompleted)
	turns[1].ProviderTurnID = "coordination"
	messages[2].Text = "AO transferred the previous agent's context in hidden system instructions. Continue the task."
	checkpoint := nativeHistoryCheckpoint{}
	checkpoint.captureAOHighWater(testCheckpointSession, turns, messages, nil)
	if checkpoint.aoHighWater.providerTurnID != "coordination" {
		t.Fatalf("coordination erased durable replay boundary: %+v", checkpoint.aoHighWater)
	}
	if len(checkpoint.mismatches(nil, turns, messages, nil)) == 0 {
		t.Fatal("empty replay admitted after a completed coordination turn")
	}
	events := []ports.ChatEvent{
		{Kind: ports.ChatEventUserMessageCompleted, ProviderTurnID: "coordination", Text: messages[2].Text},
		{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "coordination", TurnState: domain.TurnStateCompleted},
	}
	if got := checkpoint.mismatches(events, turns, messages, nil); len(got) != 0 {
		t.Fatalf("current provider replay incorrectly requires previous provider's history: %v", got)
	}
}

func TestCheckpointRepeatedPairDoesNotAdmitOlderPrefix(t *testing.T) {
	checkpoint := nativeHistoryCheckpoint{
		latestUserPrompt: "continue", latestAssistantUpdate: "Done", completedUserPrompt: true,
		userMismatch: ports.ChatHistoryMismatchTrustedUserText, assistantMismatch: ports.ChatHistoryMismatchTrustedAssistantText,
	}
	// The real latest turn C repeats A. The supplied prefix ends at B, before C.
	events := []ports.ChatEvent{
		{Kind: ports.ChatEventUserMessageCompleted, ProviderTurnID: "A", Text: "continue"},
		{Kind: ports.ChatEventMessageCompleted, ProviderTurnID: "A", Text: "Done"},
		{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "A", TurnState: domain.TurnStateCompleted},
		{Kind: ports.ChatEventUserMessageCompleted, ProviderTurnID: "B", Text: "other"},
		{Kind: ports.ChatEventMessageCompleted, ProviderTurnID: "B", Text: "answer"},
		{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "B", TurnState: domain.TurnStateCompleted},
	}
	if got := checkpoint.mismatches(events, nil, nil, nil); len(got) == 0 {
		t.Fatal("older occurrence A admitted replay A+B without checkpoint C")
	}
}

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
			checkpoint := nativeHistoryCheckpoint{
				latestUserPrompt: "Say hi to", userMismatch: ports.ChatHistoryMismatchUntrustedUserText,
			}
			checkpoint.captureAOHighWater(testCheckpointSession, turns, messages, nil)

			if len(checkpoint.mismatches(completedReplay(), turns, messages, nil)) != 0 {
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

	checkpoint := nativeHistoryCheckpoint{
		latestUserPrompt: "Run the final verification.", userMismatch: ports.ChatHistoryMismatchUntrustedUserText,
	}
	checkpoint.captureAOHighWater(testCheckpointSession, turns, messages, nil)

	if len(checkpoint.mismatches(completedReplay(), turns, messages, nil)) == 0 {
		t.Fatal("a settled prompt the replay has not reached must keep gating the import")
	}
}

func TestCheckpointKeepsTrustedTextMatchingAnUnsettledChatTurn(t *testing.T) {
	for _, role := range []domain.MessageRole{domain.MessageRoleUser, domain.MessageRoleAssistant} {
		for _, state := range []domain.TurnState{
			domain.TurnStateCancelled, domain.TurnStateInterrupted, domain.TurnStateFailed,
		} {
			t.Run(string(role)+"/"+string(state), func(t *testing.T) {
				turns, messages := poisonedRows(state)
				checkpoint := nativeHistoryCheckpoint{}
				var want ports.ChatHistoryMismatchDimension
				if role == domain.MessageRoleUser {
					checkpoint.latestUserPrompt = "Say hi to"
					checkpoint.userMismatch = ports.ChatHistoryMismatchTrustedUserText
					want = ports.ChatHistoryMismatchTrustedUserText
				} else {
					messages = append(messages, domain.ConversationMessage{
						TurnID: "unsettled-turn", Sequence: 4, Role: role, Text: "Unsettled answer",
					})
					checkpoint.latestAssistantUpdate = "Unsettled answer"
					checkpoint.assistantMismatch = ports.ChatHistoryMismatchTrustedAssistantText
					want = ports.ChatHistoryMismatchTrustedAssistantText
				}
				checkpoint.captureAOHighWater(testCheckpointSession, turns, messages, nil)
				mismatches := checkpoint.mismatches(completedReplay(), turns, messages, nil)
				if len(mismatches) != 1 || mismatches[0] != want {
					t.Fatalf("trusted %s checkpoint borrowed an old %s outcome: %v", role, state, mismatches)
				}
			})
		}
	}
}
