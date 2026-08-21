package quota

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func float64ptr(v float64) *float64 { return &v }

func TestMergePartialPreservesUnreportedBuckets(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	current := domain.QuotaSnapshot{Provider: "claude", AccountID: "default", ObservedAt: now.Add(-time.Minute), Limits: []domain.QuotaLimit{
		{ID: "five_hour", WindowType: "rolling", UsedPercent: float64ptr(40)},
		{ID: "seven_day", WindowType: "rolling", UsedPercent: float64ptr(60)},
	}}
	update := domain.QuotaSnapshot{Provider: "claude", AccountID: "default", Completeness: domain.QuotaPartial, ObservedAt: now, Limits: []domain.QuotaLimit{
		{ID: "five_hour", WindowType: "rolling", UsedPercent: float64ptr(80)},
	}}
	got := Merge(current, update)
	if len(got.Limits) != 2 {
		t.Fatalf("limits = %d, want 2", len(got.Limits))
	}
	if got.Limits[0].UsedPercent == nil || *got.Limits[0].UsedPercent != 80 {
		t.Fatalf("five-hour = %#v", got.Limits[0])
	}
	if got.Limits[1].UsedPercent == nil || *got.Limits[1].UsedPercent != 60 {
		t.Fatalf("seven-day = %#v", got.Limits[1])
	}
}

func TestMergeCompleteRemovesDisappearedBuckets(t *testing.T) {
	now := time.Now().UTC()
	current := domain.QuotaSnapshot{Provider: "codex", AccountID: "default", Limits: []domain.QuotaLimit{{ID: "old", WindowType: "primary"}}}
	update := domain.QuotaSnapshot{Provider: "codex", AccountID: "default", Completeness: domain.QuotaComplete, ObservedAt: now, Limits: []domain.QuotaLimit{{ID: "codex", WindowType: "primary"}}}
	got := Merge(current, update)
	if len(got.Limits) != 1 || got.Limits[0].ID != "codex" {
		t.Fatalf("limits = %#v", got.Limits)
	}
}

func TestRemainingSeverityAndFreshness(t *testing.T) {
	used := 91.0
	limit := domain.QuotaLimit{UsedPercent: &used}
	if got := *limit.RemainingPercent(); got != 9 {
		t.Fatalf("remaining = %v", got)
	}
	if got := LimitSeverity(limit); got != SeverityCritical {
		t.Fatalf("severity = %q", got)
	}
	now := time.Now().UTC()
	if got := Freshness(now.Add(-6*time.Minute), now); got != "stale" {
		t.Fatalf("freshness = %q", got)
	}
}
