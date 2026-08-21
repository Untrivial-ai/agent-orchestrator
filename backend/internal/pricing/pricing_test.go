package pricing

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestRatesForMatchesFamilyAcrossVersions(t *testing.T) {
	cases := map[string]Rates{
		"claude-opus-4-8":         {Input: 15, Output: 75, CacheWrite: 18.75, CacheRead: 1.50},
		"claude-sonnet-4-5":       {Input: 3, Output: 15, CacheWrite: 3.75, CacheRead: 0.30},
		"claude-3-5-haiku-latest": {Input: 0.80, Output: 4, CacheWrite: 1.00, CacheRead: 0.08},
		"CLAUDE-OPUS-5":           {Input: 15, Output: 75, CacheWrite: 18.75, CacheRead: 1.50},
	}
	for model, want := range cases {
		got, ok := RatesFor(model)
		if !ok {
			t.Fatalf("RatesFor(%q) not recognized", model)
		}
		if got != want {
			t.Fatalf("RatesFor(%q) = %+v, want %+v", model, got, want)
		}
	}
}

func TestRatesForUnknownOrEmpty(t *testing.T) {
	for _, model := range []string{"", "   ", "gpt-4o", "gemini-2.5-pro"} {
		if _, ok := RatesFor(model); ok {
			t.Fatalf("RatesFor(%q) unexpectedly recognized", model)
		}
	}
}

func TestCostSonnetSplitTokens(t *testing.T) {
	cost, ok := Cost("claude-sonnet-4-5", 1_000_000, 1_000_000, 1_000_000, 1_000_000)
	if !ok {
		t.Fatal("expected sonnet to be priced")
	}
	want := 3.0 + 15.0 + 0.30 + 3.75
	if !approx(cost, want) {
		t.Fatalf("cost = %v, want %v", cost, want)
	}
}

func TestCostUnknownModelIsZeroAndFalse(t *testing.T) {
	cost, ok := Cost("gpt-4o", 1_000_000, 1_000_000, 0, 0)
	if ok || cost != 0 {
		t.Fatalf("unknown model: got (%v,%v), want (0,false)", cost, ok)
	}
}

func TestCostClampsNegativeTokens(t *testing.T) {
	cost, ok := Cost("claude-opus-4-8", -5, -5, -5, -5)
	if !ok || cost != 0 {
		t.Fatalf("negative tokens: got (%v,%v), want (0,true)", cost, ok)
	}
}
