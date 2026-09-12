package chat

import (
	"errors"
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

// SettleTurn writes error_message unconditionally, so a replay that upgrades the
// state without carrying the stored message back would blank it. That is how a
// failed turn loses its explanation on the next reconnect and falls back to a
// bare "The agent ran into a problem".
func TestReconcileNativeHistoryKeepsStoredFailureMessage(t *testing.T) {
	events := []ports.ChatEvent{{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn",
		TurnState: domain.TurnStateRecovered,
	}}
	turns := []domain.ConversationTurn{{
		ID: "ao-turn", ProviderTurnID: "provider-turn", State: domain.TurnStateFailed,
		ErrorMessage: `the agent stopped early (stop reason "refusal")`,
	}}

	got := reconcileNativeHistory(events, turns, nil, nil)
	if len(got) != 1 || got[0].TurnState != domain.TurnStateFailed {
		t.Fatalf("reconciled events = %#v, want durable failed outcome", got)
	}
	if got[0].Err == nil {
		t.Fatal("replay dropped the stored failure message; re-settling would blank error_message")
	}
	if got[0].Err.Error() != turns[0].ErrorMessage {
		t.Fatalf("restored message = %q, want %q", got[0].Err, turns[0].ErrorMessage)
	}
}

// A replay that carries its own error must win: it describes this replay, while
// the stored message may predate it.
func TestReconcileNativeHistoryPrefersReplayErrorOverStoredMessage(t *testing.T) {
	replayErr := errors.New("fresh provider failure")
	events := []ports.ChatEvent{{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn",
		TurnState: domain.TurnStateRecovered, Err: replayErr,
	}}
	turns := []domain.ConversationTurn{{
		ID: "ao-turn", ProviderTurnID: "provider-turn", State: domain.TurnStateFailed,
		ErrorMessage: "stale stored message",
	}}

	got := reconcileNativeHistory(events, turns, nil, nil)
	if len(got) != 1 || got[0].Err != replayErr {
		t.Fatalf("reconciled error = %#v, want the replay's own error", got[0].Err)
	}
}
