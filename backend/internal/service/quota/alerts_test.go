package quota

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestTransitionAlertsOnlyOnActionableTransitions(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	usedNormal, usedCritical := 70.0, 91.0
	previous := domain.QuotaSnapshot{Provider: "claude", AccountID: "default", ObservedAt: now.Add(-time.Minute), Limits: []domain.QuotaLimit{{
		ID: "five_hour", Name: "Five hour", UsedPercent: &usedNormal,
	}}}
	next := domain.QuotaSnapshot{Provider: "claude", AccountID: "default", ObservedAt: now, Limits: []domain.QuotaLimit{{
		ID: "five_hour", Name: "Five hour", UsedPercent: &usedCritical,
	}}}
	alerts := TransitionAlerts(previous, next)
	if len(alerts) != 1 || alerts[0].Kind != "threshold" || alerts[0].Severity != "critical" {
		t.Fatalf("alerts = %#v", alerts)
	}
	if duplicate := TransitionAlerts(next, next); len(duplicate) != 0 {
		t.Fatalf("same-state alerts = %#v", duplicate)
	}
}

func TestTransitionAlertsRecoveryCreditsAndSpend(t *testing.T) {
	now := time.Now().UTC()
	falseValue, trueValue := false, true
	usedExhausted, usedAvailable := 100.0, 20.0
	previous := domain.QuotaSnapshot{
		Provider: "codex", AccountID: "default", ObservedAt: now.Add(-time.Minute),
		Limits: []domain.QuotaLimit{
			{ID: "primary", UsedPercent: &usedExhausted},
			{ID: "spend", Category: domain.QuotaSpendLimit, Reached: &falseValue},
		},
		Balances: []domain.QuotaBalance{{ID: "credits", Name: "Credits", Value: "2"}},
	}
	next := domain.QuotaSnapshot{
		Provider: "codex", AccountID: "default", ObservedAt: now,
		Limits: []domain.QuotaLimit{
			{ID: "primary", UsedPercent: &usedAvailable},
			{ID: "spend", Category: domain.QuotaSpendLimit, Reached: &trueValue},
		},
		Balances: []domain.QuotaBalance{{ID: "credits", Name: "Credits", Value: "0"}},
	}
	alerts := TransitionAlerts(previous, next)
	want := map[string]bool{"available": false, "spend_reached": false, "credits_depleted": false}
	for _, alert := range alerts {
		want[alert.Kind] = true
	}
	for kind, found := range want {
		if !found {
			t.Fatalf("missing %s in %#v", kind, alerts)
		}
	}
}
