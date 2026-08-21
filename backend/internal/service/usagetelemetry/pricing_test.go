package usagetelemetry

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func i64(v int64) *int64 { return &v }

func TestModelCost(t *testing.T) {
	t.Parallel()
	// 1M fresh input + 1M output on Opus = $15 + $75 = $90.
	opus := domain.UsageMetricTotals{InputTokens: i64(1_000_000), OutputTokens: i64(1_000_000)}
	if got := round2(modelCost("claude-opus-4-8", opus)); got != 90 {
		t.Fatalf("opus cost = %v, want 90", got)
	}
	// Uncached input takes precedence over the (larger, cache-inclusive) input.
	split := domain.UsageMetricTotals{InputTokens: i64(5_000_000), UncachedInputTokens: i64(1_000_000), OutputTokens: i64(0)}
	if got := round2(modelCost("claude-sonnet-5", split)); got != 3 {
		t.Fatalf("sonnet uncached-input cost = %v, want 3", got)
	}
	// Reasoning tokens are billed at the output rate.
	reasoning := domain.UsageMetricTotals{OutputTokens: i64(500_000), ReasoningTokens: i64(500_000)}
	if got := round2(modelCost("claude-opus-4-8", reasoning)); got != 75 {
		t.Fatalf("opus reasoning-as-output cost = %v, want 75", got)
	}
	// Cache read is far cheaper than fresh input.
	cache := domain.UsageMetricTotals{CacheReadTokens: i64(1_000_000)}
	if got := round2(modelCost("claude-opus-4-8", cache)); got != 1.5 {
		t.Fatalf("opus cache-read cost = %v, want 1.5", got)
	}
	// Unknown model: no dollar estimate.
	if got := modelCost("some-other-llm", opus); got != 0 {
		t.Fatalf("unknown model cost = %v, want 0", got)
	}
}

func TestPriceForHaikuTierPrecedence(t *testing.T) {
	t.Parallel()
	one := domain.UsageMetricTotals{OutputTokens: i64(1_000_000)}
	if got := round2(modelCost("claude-3-haiku-20240307", one)); got != 1.25 {
		t.Fatalf("claude-3-haiku output cost = %v, want 1.25", got)
	}
	if got := round2(modelCost("claude-3-5-haiku-latest", one)); got != 4 {
		t.Fatalf("claude-3-5-haiku output cost = %v, want 4", got)
	}
}
