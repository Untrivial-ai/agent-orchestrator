package usage

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

// sourceKindForHarness returns the root usage artifact shape for a harness.
// Claude and Codex may register additional child sources after the root.
func sourceKindForHarness(h domain.AgentHarness) (domain.UsageSourceKind, bool) {
	switch h {
	case domain.HarnessClaudeCode:
		return domain.UsageSourceClaudeMain, true
	case domain.HarnessCodex:
		return domain.UsageSourceCodexRollout, true
	case domain.HarnessCopilot:
		return domain.UsageSourceCopilotShutdown, true
	case domain.HarnessKimi:
		return domain.UsageSourceKimiWire, true
	case domain.HarnessPi:
		return domain.UsageSourcePiSession, true
	case domain.HarnessQwen:
		return domain.UsageSourceQwenMonthly, true
	default:
		return "", false
	}
}
