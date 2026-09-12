package chat

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestReconcileNativeHistoryUpgradesRecoveredWithKnownProviderOutcome(t *testing.T) {
	events := []ports.ChatEvent{{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn",
		TurnState: domain.TurnStateCompleted,
	}}
	turns := []domain.ConversationTurn{{
		ID: "ao-turn", ProviderTurnID: "provider-turn", State: domain.TurnStateRecovered,
	}}

	got := reconcileNativeHistory(events, turns, nil, nil)
	if len(got) != 1 || got[0].TurnState != domain.TurnStateCompleted {
		t.Fatalf("reconciled events = %#v, want provider completed to upgrade recovered", got)
	}
}

func TestReconcileNativeHistoryPreservesKnownOutcomeOverRecoveredReplay(t *testing.T) {
	events := []ports.ChatEvent{{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn",
		TurnState: domain.TurnStateRecovered,
	}}
	turns := []domain.ConversationTurn{{
		ID: "ao-turn", ProviderTurnID: "provider-turn", State: domain.TurnStateInterrupted,
	}}

	got := reconcileNativeHistory(events, turns, nil, nil)
	if len(got) != 1 || got[0].TurnState != domain.TurnStateInterrupted {
		t.Fatalf("reconciled events = %#v, want durable interrupted outcome", got)
	}
}
