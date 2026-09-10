package session

import (
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

// noSignalGrace is how long after spawn/restore a session may stay silent
// before its idle reading is downgraded to StatusNoSignal. It covers the
// agent's TUI boot plus the gap to the first activity-bearing hook callback
// (for Codex that is UserPromptSubmit, seconds after the auto-submitted spawn
// prompt — its SessionStart hook fires earlier but carries no activity state);
// past it, a silent session is indistinguishable from one with a broken hook
// pipeline, and the dashboard must not claim a confident "idle".
const noSignalGrace = 90 * time.Second

func deriveStatus(rec domain.SessionRecord, prs []domain.PRFacts, now time.Time, signalCapable bool) domain.SessionStatus {
	status := contract.DeriveStatus(
		toContractSessionFacts(rec, signalCapable),
		toContractPRFacts(prs),
		now,
		noSignalGrace,
	)
	return domain.SessionStatus(status)
}

func deriveSCMStatus(prs []domain.PRFacts) domain.SessionStatus {
	return domain.SessionStatus(contract.DeriveSCMStatus(toContractPRFacts(prs)))
}

func toContractSessionFacts(rec domain.SessionRecord, signalCapable bool) contract.SessionFacts {
	return contract.SessionFacts{
		Activity:       contract.ActivityState(rec.Activity.State),
		LastActivityAt: rec.Activity.LastActivityAt,
		HasSignal:      !rec.FirstSignalAt.IsZero(),
		SignalExpected: signalCapable && rec.Mode != domain.SessionModeChat,
		IsTerminated:   rec.IsTerminated,
	}
}

func toContractPRFacts(prs []domain.PRFacts) []contract.PRFacts {
	facts := make([]contract.PRFacts, len(prs))
	for i, pr := range prs {
		facts[i] = contract.PRFacts{
			URL:            pr.URL,
			Draft:          pr.Draft,
			Merged:         pr.Merged,
			Closed:         pr.Closed,
			CI:             pr.CI,
			Review:         pr.Review,
			Mergeability:   pr.Mergeability,
			ReviewComments: pr.ReviewComments,
			SourceBranch:   pr.SourceBranch,
			TargetBranch:   pr.TargetBranch,
		}
	}
	return facts
}
