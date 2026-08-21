// Package usagetelemetry emits per-session token-usage ranking telemetry from
// the daemon's usage subsystem. AO's usage pipeline (internal/observe/usage,
// internal/service/usage) already parses agent transcripts and persists
// per-session token totals; this package adds the two things that pipeline does
// not: an estimated dollar cost, and a single ao.session.token_usage event per
// session end attributed to the project's GitHub owner so spend can be ranked
// per organisation/account.
package usagetelemetry

import (
	"math"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// price is the per-million-token USD rate for one model tier. Cache-write and
// cache-read are billed at their own rates, matching Anthropic's published
// prompt-caching pricing. Reasoning tokens are billed as output.
type price struct {
	inputPerM      float64
	outputPerM     float64
	cacheWritePerM float64
	cacheReadPerM  float64
}

// priceTable maps a model-id substring to its tier price, most specific first.
// Rates are public list prices in USD per million tokens and are deliberately
// approximate: the estimate is for relative ranking, not billing. Update as
// pricing changes. An unknown model yields a zero estimate (tokens are still
// counted; only the dollar figure is withheld).
var priceTable = []struct {
	match string
	price price
}{
	{"claude-3-opus", price{15, 75, 18.75, 1.50}},
	{"opus", price{15, 75, 18.75, 1.50}},
	{"claude-3-5-haiku", price{0.80, 4, 1, 0.08}},
	{"claude-3-haiku", price{0.25, 1.25, 0.30, 0.03}},
	{"haiku", price{0.80, 4, 1, 0.08}},
	{"sonnet", price{3, 15, 3.75, 0.30}},
	// Codex / GPT tiers priced at published GPT-5-class rates as a coarse proxy.
	{"gpt-5", price{1.25, 10, 1.25, 0.125}},
	{"gpt", price{1.25, 10, 1.25, 0.125}},
}

// modelCost estimates the USD cost of one model's token vector. freshInput is
// the uncached input (UncachedInputTokens when the pipeline recorded it, else
// InputTokens). Reasoning tokens are priced at the output rate. Returns 0 for
// an unknown model tier.
func modelCost(modelID string, m domain.UsageMetricTotals) float64 {
	p, ok := priceFor(modelID)
	if !ok {
		return 0
	}
	const perMillion = 1_000_000.0
	freshInput := deref(m.UncachedInputTokens)
	if freshInput == 0 {
		freshInput = deref(m.InputTokens)
	}
	output := deref(m.OutputTokens) + deref(m.ReasoningTokens)
	return float64(freshInput)/perMillion*p.inputPerM +
		float64(output)/perMillion*p.outputPerM +
		float64(deref(m.CacheWriteTokens))/perMillion*p.cacheWritePerM +
		float64(deref(m.CacheReadTokens))/perMillion*p.cacheReadPerM
}

func priceFor(modelID string) (price, bool) {
	m := strings.ToLower(strings.TrimSpace(modelID))
	if m == "" {
		return price{}, false
	}
	for _, e := range priceTable {
		if strings.Contains(m, e.match) {
			return e.price, true
		}
	}
	return price{}, false
}

func deref(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
