package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestQuotaSnapshotPersistsUnknownsAndDeduplicatesHistory(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	used := 71.0
	duration := 7 * 24 * time.Hour
	snapshot := domain.QuotaSnapshot{
		Provider: "codex", AccountID: "default", PlanType: "pro", Completeness: domain.QuotaComplete,
		ObservedAt: now, Capabilities: domain.QuotaCapabilities{SupportsRead: true, SupportsCredits: true},
		Limits:   []domain.QuotaLimit{{ID: "codex", Category: domain.QuotaRateLimit, Scope: domain.QuotaAccountScope, WindowType: "primary", UsedPercent: &used, WindowDuration: &duration}},
		Balances: []domain.QuotaBalance{{ID: "credits", Name: "Credits", Value: "0"}},
	}
	if err := store.UpsertQuotaSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.ObservedAt = now.Add(time.Minute)
	if err := store.UpsertQuotaSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.GetQuotaSnapshot(ctx, "codex", "default")
	if err != nil || !ok {
		t.Fatalf("get = ok:%v err:%v", ok, err)
	}
	if got.PlanType != "pro" || len(got.Limits) != 1 || got.Limits[0].UsedPercent == nil || *got.Limits[0].UsedPercent != 71 || len(got.Balances) != 1 {
		t.Fatalf("snapshot = %+v", got)
	}
	history, err := store.ListQuotaHistory(ctx, "codex", "default", now.Add(-time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("history = %d, want deduplicated 1", len(history))
	}
}

func TestPartialQuotaSnapshotPreservesOtherBuckets(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	first, second := 30.0, 40.0
	complete := domain.QuotaSnapshot{Provider: "claude", AccountID: "default", Completeness: domain.QuotaComplete, ObservedAt: now, Limits: []domain.QuotaLimit{
		{ID: "five_hour", Category: domain.QuotaRateLimit, Scope: domain.QuotaAccountScope, UsedPercent: &first},
		{ID: "seven_day", Category: domain.QuotaRateLimit, Scope: domain.QuotaAccountScope, UsedPercent: &second},
	}}
	if err := store.UpsertQuotaSnapshot(ctx, complete); err != nil {
		t.Fatal(err)
	}
	updated := 80.0
	partial := domain.QuotaSnapshot{Provider: "claude", AccountID: "default", Completeness: domain.QuotaPartial, ObservedAt: now.Add(time.Minute), Limits: []domain.QuotaLimit{{ID: "five_hour", Category: domain.QuotaRateLimit, Scope: domain.QuotaAccountScope, UsedPercent: &updated}}}
	if err := store.UpsertQuotaSnapshot(ctx, partial); err != nil {
		t.Fatal(err)
	}
	got, _, err := store.GetQuotaSnapshot(ctx, "claude", "default")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Limits) != 2 {
		t.Fatalf("limits = %+v", got.Limits)
	}
}

func TestCompactQuotaHistoryAppliesTieredRetention(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 21, 12, 30, 0, 0, time.UTC)
	observations := []time.Time{
		now.Add(-91 * 24 * time.Hour),
		now.Add(-60 * 24 * time.Hour),
		now.Add(-60*24*time.Hour + 15*time.Minute),
		now.Add(-10 * 24 * time.Hour),
		now.Add(-10*24*time.Hour + 15*time.Minute),
		now.Add(-20 * time.Second),
		now.Add(-10 * time.Second),
	}
	for i, observedAt := range observations {
		used := float64((i % 2) * 100)
		if err := store.UpsertQuotaSnapshot(ctx, domain.QuotaSnapshot{
			Provider:     "codex",
			AccountID:    "default",
			Completeness: domain.QuotaComplete,
			ObservedAt:   observedAt,
			Limits: []domain.QuotaLimit{{
				ID: "primary", Category: domain.QuotaRateLimit, Scope: domain.QuotaAccountScope,
				UsedPercent: &used, ObservedAt: observedAt,
			}},
		}); err != nil {
			t.Fatalf("record observation %d: %v", i, err)
		}
	}

	deleted, err := store.CompactQuotaHistory(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 4 {
		t.Fatalf("deleted = %d, want 4", deleted)
	}
	history, err := store.ListQuotaHistory(ctx, "codex", "default", now.Add(-100*24*time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("history = %d points, want 3 after daily/hourly/minute compaction", len(history))
	}
}

func TestPersistQuotaObservationStoresAlertsAtomically(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	snapshot := domain.QuotaSnapshot{Provider: "claude", AccountID: "default", Completeness: domain.QuotaPartial, ObservedAt: now}
	alert := domain.QuotaAlert{ID: "quota_alert_1", Provider: "claude", AccountID: "default", LimitID: "five_hour", Kind: "threshold", Severity: "critical", Title: "Claude usage is critical", CreatedAt: now}
	if err := store.PersistQuotaObservation(ctx, snapshot, []domain.QuotaAlert{alert}); err != nil {
		t.Fatal(err)
	}
	alerts, err := store.ListQuotaAlerts(ctx, now.Add(-time.Minute), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 || alerts[0].ID != alert.ID || alerts[0].Severity != "critical" {
		t.Fatalf("alerts = %#v", alerts)
	}
}
