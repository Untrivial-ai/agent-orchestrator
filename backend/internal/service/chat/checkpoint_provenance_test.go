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
			completedAt := turns[1].RequestedAt.Add(time.Second)
			turns[1].CompletedAt = &completedAt
			checkpoint := nativeHistoryCheckpoint{latestUserPrompt: "Say hi to", latestUserPromptAt: turns[1].RequestedAt}
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

func TestCheckpointRetiresOnlySupersededHookFacts(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*nativeHistoryCheckpoint, []domain.ConversationTurn, []domain.ConversationMessage)
		want bool
	}{
		{name: "known older prompt and answer", want: true},
		{name: "unknown terminal answer", edit: func(p *nativeHistoryCheckpoint, _ []domain.ConversationTurn, _ []domain.ConversationMessage) {
			p.latestAssistantUpdate = "New work AO has not imported"
		}},
		{name: "unknown terminal prompt", edit: func(p *nativeHistoryCheckpoint, _ []domain.ConversationTurn, _ []domain.ConversationMessage) {
			p.latestUserPrompt = "New request AO has not imported"
		}},
		{name: "newest repeated answer must still match", edit: func(_ *nativeHistoryCheckpoint, _ []domain.ConversationTurn, messages []domain.ConversationMessage) {
			messages[3].Text = "Old answer"
		}},
		{name: "newer interrupted turn cannot supersede", edit: func(_ *nativeHistoryCheckpoint, turns []domain.ConversationTurn, _ []domain.ConversationMessage) {
			turns[1].State = domain.TurnStateInterrupted
		}},
		{name: "newer rolled back turn cannot supersede", edit: func(_ *nativeHistoryCheckpoint, turns []domain.ConversationTurn, _ []domain.ConversationMessage) {
			at := turns[1].RequestedAt.Add(time.Second)
			turns[1].RolledBackAt = &at
		}},
		{name: "other session cannot establish provenance", edit: func(_ *nativeHistoryCheckpoint, turns []domain.ConversationTurn, _ []domain.ConversationMessage) {
			turns[0].HandledBySessionID = "other-session"
		}},
		{name: "streaming text cannot establish settled provenance", edit: func(_ *nativeHistoryCheckpoint, _ []domain.ConversationTurn, messages []domain.ConversationMessage) {
			messages[1].Streaming = true
		}},
		{name: "legacy provider boundary cannot supersede predecessor facts", edit: func(_ *nativeHistoryCheckpoint, _ []domain.ConversationTurn, messages []domain.ConversationMessage) {
			messages[2].Text = "<ao-handoff-request>new provider</ao-handoff-request>"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := time.Date(2026, 9, 12, 11, 8, 0, 0, time.UTC)
			turns := []domain.ConversationTurn{
				{ID: "old", HandledBySessionID: testCheckpointSession, ProviderTurnID: "native-old", State: domain.TurnStateCompleted, RequestedAt: base},
				{ID: "new", HandledBySessionID: testCheckpointSession, ProviderTurnID: "native-new", State: domain.TurnStateCompleted, RequestedAt: base.Add(time.Minute)},
			}
			messages := []domain.ConversationMessage{
				{TurnID: "old", Sequence: 1, Role: domain.MessageRoleUser, Text: "Old prompt", ProviderItemID: "old-user"},
				{TurnID: "old", Sequence: 2, Role: domain.MessageRoleAssistant, Text: "Old answer", ProviderItemID: "old-answer"},
				{TurnID: "new", Sequence: 3, Role: domain.MessageRoleUser, Text: "New prompt", ProviderItemID: "new-user"},
				{TurnID: "new", Sequence: 4, Role: domain.MessageRoleAssistant, Text: "New answer", ProviderItemID: "new-answer"},
			}
			oldReplay := []ports.ChatEvent{
				{Kind: ports.ChatEventUserMessageCompleted, ProviderTurnID: "native-old", ProviderItemID: "old-user", Text: "Old prompt"},
				{Kind: ports.ChatEventMessageCompleted, ProviderTurnID: "native-old", ProviderItemID: "old-answer", Text: "Old answer"},
				{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "native-old"},
			}
			fullReplay := append(append([]ports.ChatEvent(nil), oldReplay...),
				ports.ChatEvent{Kind: ports.ChatEventUserMessageCompleted, ProviderTurnID: "native-new", ProviderItemID: "new-user", Text: "New prompt"},
				ports.ChatEvent{Kind: ports.ChatEventMessageCompleted, ProviderTurnID: "native-new", ProviderItemID: "new-answer", Text: "New answer"},
				ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "native-new"},
			)
			checkpoint := nativeHistoryCheckpoint{latestUserPrompt: "Old prompt", latestAssistantUpdate: "Old answer", latestUserPromptAt: base, latestAssistantUpdateAt: base.Add(time.Second)}
			if tc.edit != nil {
				tc.edit(&checkpoint, turns, messages)
			}
			checkpoint.captureAOHighWater(testCheckpointSession, turns, messages, nil)
			if got := checkpoint.reached(fullReplay); got != tc.want {
				t.Fatalf("complete replay reached = %v, want %v", got, tc.want)
			}
			if tc.want && checkpoint.reached(oldReplay) {
				t.Fatal("retiring older hook facts must not admit a replay missing the newer completed Chat turn")
			}
		})
	}
}
