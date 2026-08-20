package usage

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

// SupportedHarness reports whether the harness has a certified usage pipeline.
func SupportedHarness(h domain.AgentHarness) bool {
	switch h {
	case domain.HarnessClaudeCode, domain.HarnessCodex, domain.HarnessCopilot, domain.HarnessKimi, domain.HarnessPi:
		return true
	default:
		return false
	}
}

// MetricCoverage reports the optional token buckets present in each certified
// native format. Source support is enabled separately by SupportedHarness so a
// parser can be developed without exposing an incomplete collection pipeline.
func MetricCoverage(h domain.AgentHarness) domain.UsageMetricCoverage {
	switch h {
	case domain.HarnessClaudeCode, domain.HarnessKimi, domain.HarnessPi:
		return domain.UsageMetricCoverage{UncachedInput: true, CacheRead: true, CacheWrite: true}
	case domain.HarnessCodex, domain.HarnessCopilot:
		return domain.UsageMetricCoverage{UncachedInput: true, CacheRead: true, CacheWrite: true, Reasoning: true}
	case domain.HarnessQwen:
		return domain.UsageMetricCoverage{UncachedInput: true, CacheRead: true, Reasoning: true}
	default:
		return domain.UsageMetricCoverage{}
	}
}
