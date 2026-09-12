package chat

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Regression captured from the live scratch-2 Codex E2E: these strings and the
// sequence reproduced a repeated TUI-to-Chat handoff timing out on settled history.
func TestLiveCodexCheckpointDoesNotRejectNewerSettledChatAnswer(t *testing.T) {
	base := time.Date(2026, 9, 12, 11, 8, 0, 0, time.UTC)
	turns := []domain.ConversationTurn{
		{ID: "terminal-turn", HandledBySessionID: "scratch-2", ProviderTurnID: "terminal-native-turn", State: domain.TurnStateCompleted, RequestedAt: base},
		{ID: "chat-turn", HandledBySessionID: "scratch-2", ProviderTurnID: "chat-native-turn", State: domain.TurnStateCompleted, RequestedAt: base.Add(time.Minute)},
	}
	oldPrompt := "Without using tools, reply CODEX-FORK-RECALL followed by the marker from this conversation."
	newPrompt := "Without tools, reply CODEX-FORK-CHAT followed by the marker you remember."
	oldAnswer := "CODEX-FORK-RECALL COMET-7516"
	newAnswer := "CODEX-FORK-CHAT COMET-7516"
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
	checkpoint.captureAOHighWater("scratch-2", turns, messages, nil)
	if checkpoint.reached(events) {
		return
	}
	probe := checkpoint
	probe.latestAssistantUpdate = ""
	if !probe.reached(events) {
		t.Fatal("diagnostic assumption is wrong: stale assistant checkpoint is not the sole rejection")
	}
	t.Fatal("settled native history is rejected solely because an older Terminal assistant checkpoint survives a newer completed Chat answer")
}
