package chat

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestNativeCheckpointRequiresExactUserBoundaryAndAnswer(t *testing.T) {
	checkpoint := nativeHistoryCheckpoint{nativeBoundary: &ports.NativeCheckpointBoundary{
		UserMessageID: "native-B", UserText: "continue", AssistantText: "Done",
	}}
	replay := func(nativeID, answer string) []ports.ChatEvent {
		return []ports.ChatEvent{
			{Kind: ports.ChatEventUserMessageCompleted, ProviderTurnID: "reconstructed-B", NativeUserMessageID: nativeID, Text: "continue"},
			{Kind: ports.ChatEventMessageCompleted, ProviderTurnID: "reconstructed-B", Text: answer},
			{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "reconstructed-B"},
		}
	}
	for _, test := range []struct {
		name, id, answer string
		wantPass         bool
	}{
		{"exact", "native-B", "Done", true},
		{"repeated earlier turn", "native-A", "Done", false},
		{"synthetic identity", "", "Done", false},
		{"missing answer", "native-B", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := checkpoint.mismatches(replay(test.id, test.answer), nil, nil, nil)
			if (len(got) == 0) != test.wantPass {
				t.Fatalf("mismatches=%v", got)
			}
		})
	}
}
