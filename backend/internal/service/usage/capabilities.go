package usage

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

// SupportedHarness reports whether the harness has a certified usage pipeline.
func SupportedHarness(h domain.AgentHarness) bool {
	switch h {
	case domain.HarnessClaudeCode, domain.HarnessCodex, domain.HarnessKimi:
		return true
	default:
		return false
	}
}
