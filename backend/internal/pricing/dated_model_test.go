package pricing

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func int64Ptr(v int64) *int64 { return &v }

func datedTestEvent(model string) domain.ModelUsageEvent {
	return domain.ModelUsageEvent{
		ProviderID:        domain.UsageProviderAnthropic,
		BillingProviderID: "anthropic",
		ModelID:           model,
		Tokens: domain.UsageTokenMetrics{
			InputTokens: int64Ptr(100), CachedInputTokens: int64Ptr(0),
			UncachedInputTokens: int64Ptr(100), OutputTokens: int64Ptr(10),
		},
	}
}

// A provider may serve a dated snapshot id for a model the catalog lists only
// undated. Pricing that at the undated rates keeps the estimate available
// instead of silently dropping it under a model name the UI renders normally.
func TestEstimateFallsBackToUndatedCatalogEntry(t *testing.T) {
	snapshot := decodeTestSnapshot(t, testBaseModels("0.000001"))

	estimate, err := snapshot.Estimate(datedTestEvent("claude-test-20260115"))
	if err != nil {
		t.Fatalf("Estimate dated model: %v", err)
	}
	if estimate.TotalNanos == nil {
		t.Fatal("dated model priced as unknown; want fallback to the undated entry")
	}

	undated, err := snapshot.Estimate(datedTestEvent("claude-test"))
	if err != nil {
		t.Fatalf("Estimate undated model: %v", err)
	}
	if *estimate.TotalNanos != *undated.TotalNanos {
		t.Fatalf("dated total = %d, undated total = %d; want equal",
			*estimate.TotalNanos, *undated.TotalNanos)
	}
}

// A catalog that lists the dated id itself must win outright: those entries
// carry their own rates, and collapsing the date first would price a dated
// model at whatever its undated sibling happens to charge.
func TestEstimatePrefersExactDatedCatalogEntryOverUndatedSibling(t *testing.T) {
	snapshot := decodeTestSnapshot(t, map[string][]testModel{
		"anthropic": {
			{ID: "claude-test", Input: "0.000001", Output: "0.000001"},
			{ID: "claude-test-20260115", Input: "0.000009", Output: "0.000009"},
		},
		"openai": {{ID: "gpt-test", Input: "0.000001", Output: "0.000001"}},
		"zai":    {{ID: "glm-test", Input: "0.000001", Output: "0.000001"}},
	})

	dated, err := snapshot.Estimate(datedTestEvent("claude-test-20260115"))
	if err != nil {
		t.Fatalf("Estimate dated model: %v", err)
	}
	undated, err := snapshot.Estimate(datedTestEvent("claude-test"))
	if err != nil {
		t.Fatalf("Estimate undated model: %v", err)
	}
	if dated.TotalNanos == nil || undated.TotalNanos == nil {
		t.Fatal("both models should price")
	}
	if *dated.TotalNanos == *undated.TotalNanos {
		t.Fatalf("dated model priced at the undated rate (%d); want its own catalog rates",
			*dated.TotalNanos)
	}
}

func TestProviderForModelResolvesDatedSuffix(t *testing.T) {
	snapshot := decodeTestSnapshot(t, testBaseModels("0.000001"))

	if got := snapshot.ProviderForModel("claude-test-20260115"); got != "anthropic" {
		t.Fatalf("ProviderForModel(dated) = %q, want %q", got, "anthropic")
	}
	// A date-shaped suffix must not conjure a provider for a model nothing lists.
	if got := snapshot.ProviderForModel("not-a-model-20260115"); got != "" {
		t.Fatalf("ProviderForModel(unknown dated) = %q, want %q", got, "")
	}
}

// Only a full 8-digit trailing component is a dated snapshot. Trimming anything
// shorter would turn a versioned model name into a different model.
func TestLookupIgnoresNonDateSuffixes(t *testing.T) {
	snapshot := decodeTestSnapshot(t, testBaseModels("0.000001"))

	for _, model := range []string{"claude-test-2026", "claude-test-202601159", "claude-test-2026011"} {
		if got := snapshot.ProviderForModel(model); got != "" {
			t.Fatalf("ProviderForModel(%q) = %q, want %q", model, got, "")
		}
	}
}
