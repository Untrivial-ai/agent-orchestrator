package chat

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type replayFallback string

func (e replayFallback) Error() string             { return string(e) }
func (e replayFallback) ChatFailureFallback() bool { return true }

func TestReconcileNativeHistoryFailurePrecedence(t *testing.T) {
	const stored = "controller ended before the turn completed"
	fallback := replayFallback("no provider details")
	for _, tt := range []struct {
		name        string
		storedState domain.TurnState
		storedError string
		replayState domain.TurnState
		replayError error
		wantState   domain.TurnState
		wantError   string
	}{
		{"failed without error", domain.TurnStateFailed, stored, domain.TurnStateFailed, nil, domain.TurnStateFailed, stored},
		{"blank error", domain.TurnStateFailed, stored, domain.TurnStateFailed, errors.New("   "), domain.TurnStateFailed, stored},
		{"fallback", domain.TurnStateFailed, stored, domain.TurnStateFailed, fallback, domain.TurnStateFailed, stored},
		{"wrapped fallback", domain.TurnStateFailed, stored, domain.TurnStateFailed, fmt.Errorf("replay: %w", fallback), domain.TurnStateFailed, stored},
		{"fresh error", domain.TurnStateFailed, stored, domain.TurnStateFailed, errors.New("fresh error"), domain.TurnStateFailed, "fresh error"},
		{"recovered fallback", domain.TurnStateFailed, stored, domain.TurnStateRecovered, fallback, domain.TurnStateFailed, stored},
		{"empty stored error", domain.TurnStateFailed, "", domain.TurnStateFailed, fallback, domain.TurnStateFailed, string(fallback)},
		{"blank stored error", domain.TurnStateFailed, " \t", domain.TurnStateFailed, fallback, domain.TurnStateFailed, string(fallback)},
		{"upgraded recovered", domain.TurnStateRecovered, "", domain.TurnStateFailed, fallback, domain.TurnStateFailed, string(fallback)},
		{"completed replay", domain.TurnStateFailed, stored, domain.TurnStateCompleted, nil, domain.TurnStateCompleted, ""},
		{"interrupted replay", domain.TurnStateFailed, stored, domain.TurnStateInterrupted, nil, domain.TurnStateInterrupted, ""},
		{"recovered completed", domain.TurnStateCompleted, "stale", domain.TurnStateRecovered, nil, domain.TurnStateCompleted, ""},
		{"recovered interrupted", domain.TurnStateInterrupted, "stale", domain.TurnStateRecovered, nil, domain.TurnStateInterrupted, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			events := []ports.ChatEvent{{
				Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn",
				TurnState: tt.replayState, Err: tt.replayError,
			}}
			turns := []domain.ConversationTurn{{
				ID: "ao-turn", ProviderTurnID: "provider-turn", State: tt.storedState, ErrorMessage: tt.storedError,
			}}
			got := reconcileNativeHistory(events, turns, nil, nil)
			if len(got) != 1 || got[0].TurnState != tt.wantState {
				t.Fatalf("events = %+v, want state %s", got, tt.wantState)
			}
			message := ""
			if got[0].Err != nil {
				message = got[0].Err.Error()
			}
			if message != tt.wantError {
				t.Fatalf("message = %q, want %q", message, tt.wantError)
			}
		})
	}
}

func TestReconcileNativeHistoryKeepsFallbackForNewTurn(t *testing.T) {
	fallback := replayFallback("no provider details")
	events := []ports.ChatEvent{{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "new-turn",
		TurnState: domain.TurnStateFailed, Err: fallback,
	}}
	for _, turns := range [][]domain.ConversationTurn{nil, {{ID: "other", ProviderTurnID: "other", State: domain.TurnStateFailed}}} {
		got := reconcileNativeHistory(events, turns, nil, nil)
		if len(got) != 1 || !errors.Is(got[0].Err, fallback) {
			t.Fatalf("new turn lost fallback: %+v", got)
		}
	}
}

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
	if len(got) != 1 || !errors.Is(got[0].Err, replayErr) {
		t.Fatalf("reconciled error = %#v, want the replay's own error", got[0].Err)
	}
}
