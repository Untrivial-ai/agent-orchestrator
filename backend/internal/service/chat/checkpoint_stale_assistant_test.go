package chat

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestCheckpointAcceptsCurrentPromptWithSupersededAssistant(t *testing.T) {
	base := time.Date(2026, 9, 12, 11, 8, 0, 0, time.UTC)
	turns := []domain.ConversationTurn{
		{ID: "terminal-turn", HandledBySessionID: testCheckpointSession, ProviderTurnID: "terminal-native-turn", State: domain.TurnStateCompleted, RequestedAt: base},
		{ID: "chat-turn", HandledBySessionID: testCheckpointSession, ProviderTurnID: "chat-native-turn", State: domain.TurnStateCompleted, RequestedAt: base.Add(time.Minute)},
	}
	oldPrompt := "Terminal prompt"
	newPrompt := "Chat prompt"
	oldAnswer := "Terminal answer"
	newAnswer := "Chat answer"
	messages := []domain.ConversationMessage{
		{TurnID: "terminal-turn", Sequence: 8, Role: domain.MessageRoleUser, Text: oldPrompt, ProviderItemID: "old-user"},
		{TurnID: "terminal-turn", Sequence: 9, Role: domain.MessageRoleAssistant, Text: oldAnswer, ProviderItemID: "old-answer"},
		{TurnID: "chat-turn", Sequence: 10, Role: domain.MessageRoleUser, Text: newPrompt, ProviderItemID: "new-user"},
		{TurnID: "chat-turn", Sequence: 11, Role: domain.MessageRoleAssistant, Text: newAnswer, ProviderItemID: "new-answer"},
	}
	events := []ports.ChatEvent{
		{Kind: ports.ChatEventUserMessageCompleted, ProviderTurnID: "terminal-native-turn", ProviderItemID: "old-user", Text: oldPrompt},
		{Kind: ports.ChatEventMessageCompleted, ProviderTurnID: "terminal-native-turn", ProviderItemID: "old-answer", Text: oldAnswer},
		{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "terminal-native-turn"},
		{Kind: ports.ChatEventUserMessageCompleted, ProviderTurnID: "chat-native-turn", ProviderItemID: "new-user", Text: newPrompt},
		{Kind: ports.ChatEventMessageCompleted, ProviderTurnID: "chat-native-turn", ProviderItemID: "new-answer", Text: newAnswer},
		{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "chat-native-turn"},
	}
	checkpoint := nativeHistoryCheckpoint{latestUserPrompt: newPrompt, latestAssistantUpdate: oldAnswer}
	checkpoint.captureAOHighWater(testCheckpointSession, turns, messages, nil)
	if !checkpoint.reached(events) {
		t.Fatal("complete replay rejected a current prompt with a superseded assistant checkpoint")
	}
}
