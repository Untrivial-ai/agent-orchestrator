package codexappserver

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/codexappserver/codexproto"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestCapacityNormalizationIncludesBucketsAndRejectsMalformedWindows(t *testing.T) {
	var envelope capacityReadEnvelope
	err := json.Unmarshal([]byte(`{
		"rateLimits":{"limitId":"codex","planType":"pro","primary":{"usedPercent":81,"windowDurationMins":300,"resetsAt":4102444800},"secondary":{"usedPercent":101}},
		"rateLimitsByLimitId":{"spark":{"limitId":"spark","limitName":"Spark","primary":{"usedPercent":25}},"alpha":{"limitId":"alpha","primary":{"usedPercent":10}}},
		"rateLimitResetCredits":{"availableCount":2,"credits":[{"id":"opaque","grantedAt":1,"expiresAt":4102444800,"resetType":"codexRateLimits","status":"available"}]}
	}`), &envelope)
	if err != nil {
		t.Fatal(err)
	}
	observed := capacityObservationFromEnvelope(envelope, time.Unix(1, 0), false)
	if observed.Plan == nil || *observed.Plan != "pro" || observed.Overall == nil || observed.Overall.Primary == nil {
		t.Fatalf("overall = %#v", observed)
	}
	if observed.Overall.Secondary != nil {
		t.Fatalf("out-of-range window was accepted: %#v", observed.Overall.Secondary)
	}
	if len(observed.AdditionalBuckets) != 2 || observed.AdditionalBuckets[0].LimitID != "alpha" || observed.AdditionalBuckets[1].LimitID != "spark" || observed.AdditionalBuckets[1].Reached != domain.CodexCapacityNotReached {
		t.Fatalf("additional buckets = %#v", observed.AdditionalBuckets)
	}
	if observed.ResetCredits == nil || observed.ResetCredits.AvailableCount != 2 || observed.ResetCredits.NearestExpiresAt == nil {
		t.Fatalf("reset credits = %#v", observed.ResetCredits)
	}
}

func TestCapacityNormalizationRejectsNonFinitePercent(t *testing.T) {
	nan := math.NaN()
	if got := normalizeCapacityWindow(&capacityWireWindow{UsedPercent: &nan}); got != nil {
		t.Fatalf("NaN capacity window was accepted: %#v", got)
	}
	infinite := math.Inf(1)
	if got := normalizeCapacityWindow(&capacityWireWindow{UsedPercent: &infinite}); got != nil {
		t.Fatalf("infinite capacity window was accepted: %#v", got)
	}
}

func TestSparseCapacityNormalizationKeepsUnknownReachedState(t *testing.T) {
	var envelope capacityReadEnvelope
	if err := json.Unmarshal([]byte(`{"rateLimits":{"limitId":"codex","primary":{"usedPercent":10}}}`), &envelope); err != nil {
		t.Fatal(err)
	}
	observed := capacityObservationFromEnvelope(envelope, time.Now(), true)
	if observed.Overall == nil || observed.Overall.Reached != domain.CodexCapacityReachUnknown {
		t.Fatalf("reached = %#v", observed.Overall)
	}
}

func TestUsageNormalizationNamesUnitsAndSelectsLatestValidDay(t *testing.T) {
	lifetime := int64(54571452296)
	peak := int64(2000000000)
	longestTurn := int64(26340)
	streak := int64(2)
	longestStreak := int64(99)
	observedAt := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.FixedZone("IST", 5*60*60+30*60))
	observed := usageObservationFromResponse(codexproto.GetAccountTokenUsageResponse{
		DailyUsageBuckets: []codexproto.AccountTokenUsageDailyBucket{
			{StartDate: "2026-08-30", Tokens: 12},
			{StartDate: "not-a-date", Tokens: 99},
			{StartDate: "2026-08-31", Tokens: 34904480},
		},
		Summary: codexproto.AccountTokenUsageSummary{LifetimeTokens: &lifetime, PeakDailyTokens: &peak, LongestRunningTurnSec: &longestTurn, CurrentStreakDays: &streak, LongestStreakDays: &longestStreak},
	}, observedAt)
	if observed.LatestDayTokens == nil || *observed.LatestDayTokens != 34904480 || observed.LatestDayStartDate == nil || *observed.LatestDayStartDate != "2026-08-31" {
		t.Fatalf("latest day = %#v", observed)
	}
	if observed.LifetimeTokens == nil || *observed.LifetimeTokens != lifetime || observed.CurrentStreakDays == nil || *observed.CurrentStreakDays != streak {
		t.Fatalf("summary = %#v", observed)
	}
	if observed.PeakDailyTokens == nil || *observed.PeakDailyTokens != peak || observed.LongestRunningTurnSeconds == nil || *observed.LongestRunningTurnSeconds != longestTurn || observed.LongestStreakDays == nil || *observed.LongestStreakDays != longestStreak {
		t.Fatalf("extended summary = %#v", observed)
	}
	if observed.ObservedAt.Location() != time.UTC {
		t.Fatalf("observed at = %v, want UTC", observed.ObservedAt)
	}
}
