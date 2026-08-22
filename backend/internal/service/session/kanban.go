package session

import (
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

func deriveKanbanColumn(rec domain.SessionRecord, prs []domain.PRFacts, runs []domain.CurrentHeadReviewRun) domain.KanbanColumn {
	return contract.DeriveKanbanColumn(
		toContractKanbanSessionFacts(rec),
		toContractKanbanPRFacts(prs, runs),
	)
}

func toContractKanbanSessionFacts(rec domain.SessionRecord) contract.KanbanSessionFacts {
	return contract.KanbanSessionFacts{
		IsTerminated:     rec.IsTerminated,
		AutoReview:       rec.AutoReviewEnabled,
		AutoInjectReview: rec.AutoInjectReview,
		AutoInjectCI:     rec.AutoInjectCI,
	}
}

func toContractKanbanPRFacts(prs []domain.PRFacts, runs []domain.CurrentHeadReviewRun) []contract.KanbanPRFacts {
	byPR := make(map[string]contract.KanbanReviewRunFacts, len(runs))
	for _, run := range runs {
		facts := byPR[run.PRURL]
		facts.Present = true
		facts.Running = facts.Running || run.Status == domain.ReviewRunRunning
		facts.ChangesRequested = facts.ChangesRequested || run.Verdict == domain.VerdictChangesRequested
		byPR[run.PRURL] = facts
	}

	out := make([]contract.KanbanPRFacts, len(prs))
	for i, pr := range prs {
		out[i] = contract.KanbanPRFacts{
			URL:          pr.URL,
			Draft:        pr.Draft,
			Merged:       pr.Merged,
			Closed:       pr.Closed,
			CI:           pr.CI,
			Review:       pr.Review,
			Mergeability: pr.Mergeability,
			UpdatedAt:    pr.UpdatedAt,
			ReviewRun:    byPR[pr.URL],
			ExternalReview: contract.KanbanExternalReviewFacts{
				Approved:         pr.ExternalApproved,
				ChangesRequested: pr.ExternalChangesRequested,
			},
		}
	}
	return out
}
