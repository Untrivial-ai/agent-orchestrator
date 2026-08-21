// Package pricing turns model token counts into a USD cost estimate.
//
// The rate table is the single source of truth for cost telemetry. Rates are
// published Anthropic list prices in USD per million tokens; cache-write is the
// 5-minute write rate and cache-read is the cache-hit rate. Update the table
// here when prices change. Unknown models return ok=false and a zero cost so
// callers can still record token counts without inventing a price.
package pricing

import "strings"

// Rates are USD per one million tokens for each token class.
type Rates struct {
	Input      float64
	Output     float64
	CacheWrite float64
	CacheRead  float64
}

// modelRates maps a coarse model family to its rates. Model ids vary across
// versions (claude-opus-4-8, claude-sonnet-4-5, claude-3-5-haiku-latest, ...),
// so lookup matches on the family token contained in the id rather than the
// exact string, which keeps the table stable across point releases.
var modelRates = []struct {
	family string
	rates  Rates
}{
	{"opus", Rates{Input: 15, Output: 75, CacheWrite: 18.75, CacheRead: 1.50}},
	{"sonnet", Rates{Input: 3, Output: 15, CacheWrite: 3.75, CacheRead: 0.30}},
	{"haiku", Rates{Input: 0.80, Output: 4, CacheWrite: 1.00, CacheRead: 0.08}},
}

// RatesFor returns the rate card for a model id and whether it was recognized.
func RatesFor(model string) (Rates, bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return Rates{}, false
	}
	for _, r := range modelRates {
		if strings.Contains(m, r.family) {
			return r.rates, true
		}
	}
	return Rates{}, false
}

// Cost estimates the USD cost of a turn. inputTokens should be the fresh
// (uncached) input count, since cache reads and writes are priced separately.
// It returns the dollar figure and whether the model was priced; an unpriced
// model yields (0, false) so the caller records tokens with a zero cost rather
// than a wrong one. Negative token counts are clamped to zero.
func Cost(model string, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int64) (float64, bool) {
	rates, ok := RatesFor(model)
	if !ok {
		return 0, false
	}
	const perToken = 1.0 / 1_000_000.0
	cost := float64(nonNeg(inputTokens))*rates.Input*perToken +
		float64(nonNeg(outputTokens))*rates.Output*perToken +
		float64(nonNeg(cacheReadTokens))*rates.CacheRead*perToken +
		float64(nonNeg(cacheWriteTokens))*rates.CacheWrite*perToken
	return cost, true
}

func nonNeg(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}
