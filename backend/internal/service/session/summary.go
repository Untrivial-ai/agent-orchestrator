package session

import (
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"strings"
)

// deriveSummary builds the generic Kanban card line from server-owned PR and
// lifecycle facts. Conversation text is intentionally not included: prompts,
// assistant replies, and tool protocol messages are not card summaries.
func deriveSummary(rec domain.SessionRecord, prs []domain.PRFacts, displayStatus contract.DisplayStatus) string {
	// Model-generated card text is stored with an AO-owned prefix so raw provider
	// checkpoints can never leak into the UI. Lifecycle/PR facts remain the safe
	// fallback until the first configured-model refresh completes.
	if generated := strings.TrimPrefix(strings.TrimSpace(rec.Metadata.LatestAssistantUpdate), "ao-card-summary:"); generated != strings.TrimSpace(rec.Metadata.LatestAssistantUpdate) && generated != "" {
		return generated
	}
	open, failing := 0, false
	for _, pr := range prs {
		if pr.Merged || pr.Closed {
			continue
		}
		open++
		if pr.CI == domain.CIFailing {
			failing = true
		}
	}
	return contract.SummarizeSession(contract.SummaryFacts{
		IsTerminated:  rec.IsTerminated,
		OpenPRs:       open,
		CIFailing:     failing,
		DisplayStatus: displayStatus,
	})
}
