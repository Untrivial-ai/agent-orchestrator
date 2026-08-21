package chat

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestReconcileNativeHistoryUsesLegacyProviderItemAlias(t *testing.T) {
	events := []ports.ChatEvent{
		{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "new-scoped-turn"},
		{
			Kind: ports.ChatEventMessageCompleted, ProviderTurnID: "new-scoped-turn",
			ProviderItemID: "new-scoped-assistant", ProviderItemAliases: []string{"legacy-assistant"},
			Text: "answer without a user prompt",
		},
		{
			Kind: ports.ChatEventActivityCompleted, ProviderTurnID: "new-scoped-turn",
			ProviderItemID: "new-scoped-tool", ProviderItemAliases: []string{"legacy-tool"},
			ActivityKind: domain.ActivityKindCommand, ActivityStatus: domain.ActivityStatusCompleted,
			Summary: "go test ./...", Detail: []byte(`{"parentProviderItemId":"new-scoped-parent"}`),
		},
		{
			Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "new-scoped-turn",
			TurnState: domain.TurnStateCompleted,
		},
	}
	existingTurns := []domain.ConversationTurn{{
		ID: "legacy-turn", ProviderTurnID: "legacy-history-turn", State: domain.TurnStateCompleted,
	}}
	existingMessages := []domain.ConversationMessage{{
		TurnID: "legacy-turn", Role: domain.MessageRoleAssistant,
		ProviderItemID: "legacy-assistant", Text: "answer without a user prompt",
	}}
	existingActivities := []domain.ConversationActivity{{
		TurnID: "legacy-turn", Kind: domain.ActivityKindCommand,
		Status: domain.ActivityStatusCompleted, ProviderItemID: "legacy-tool", Summary: "go test ./...",
		Detail: []byte(`{"parentProviderItemId":"legacy-parent"}`),
	}}

	got := reconcileNativeHistory(events, existingTurns, existingMessages, existingActivities)
	if len(got) != 2 {
		t.Fatalf("reconciled events = %#v, want only turn boundaries", got)
	}
	for _, event := range got {
		if event.ProviderTurnID != "legacy-history-turn" {
			t.Fatalf("reconciled event turn = %q, want legacy-history-turn", event.ProviderTurnID)
		}
	}
}

func TestReconcileNativeHistoryUsesLegacyAliasForAttachmentOnlyUserTurn(t *testing.T) {
	events := []ports.ChatEvent{
		{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "new-scoped-turn"},
		{
			Kind: ports.ChatEventUserMessageCompleted, ProviderTurnID: "new-scoped-turn",
			ProviderItemID: "new-scoped-user", ProviderItemAliases: []string{"legacy-client"},
			ClientMessageID: "new-scoped-user", Text: "[Image]",
		},
		{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "new-scoped-turn", TurnState: domain.TurnStateInterrupted},
	}
	existingTurns := []domain.ConversationTurn{{
		ID: "legacy-turn", ProviderTurnID: "legacy-history-turn", State: domain.TurnStateInterrupted,
	}}
	existingMessages := []domain.ConversationMessage{{
		TurnID: "legacy-turn", Role: domain.MessageRoleUser,
		ClientMessageID: "legacy-client", Text: "",
	}}

	got := reconcileNativeHistory(events, existingTurns, existingMessages, nil)
	for _, event := range got {
		if event.ProviderTurnID != "legacy-history-turn" {
			t.Fatalf("reconciled event turn = %q, want legacy-history-turn; events=%#v",
				event.ProviderTurnID, got)
		}
	}
}
