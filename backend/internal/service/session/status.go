package session

import (
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// noSignalGrace is how long after spawn/restore a session may stay silent
// before its idle reading is downgraded to StatusNoSignal. It covers the
// agent's TUI boot plus the gap to the first activity-bearing hook callback
// (for Codex that is UserPromptSubmit, seconds after the auto-submitted spawn
// prompt — its SessionStart hook fires earlier but carries no activity state);
// past it, a silent session is indistinguishable from one with a broken hook
// pipeline, and the dashboard must not claim a confident "idle".
const noSignalGrace = 90 * time.Second

// deriveStatus computes the display status. signalCapable says whether this
// session's harness has an activity hook pipeline at all; only then can
// prolonged silence mean the pipeline is broken (no_signal) rather than the
// permanent, normal silence of a hook-less harness.
//
// A session may own several PRs at once (independent or stacked). The PR-derived
// status is the worst-wins aggregate across its open PRs; stacked children whose
// parent is still open are exempt from the aggregation since they cannot merge
// until the parent does. Merged/closed PRs only matter once no open PR remains.
func deriveStatus(rec domain.SessionRecord, prs []domain.PRFacts, now time.Time, signalCapable bool) domain.SessionStatus {
	if rec.IsTerminated {
		if anyMerged(prs) {
			return domain.StatusMerged
		}
		return domain.StatusTerminated
	}

	switch rec.Activity.State {
	case domain.ActivityActive:
		return domain.StatusWorking
	case domain.ActivityExited:
		return domain.StatusExited
	case domain.ActivityWaitingInput, domain.ActivityBlocked:
		// PR facts are a newer, more actionable signal than a sticky
		// waiting_input/blocked activity state: once a worker has raised a
		// PR, its pipeline status (pr_open, ci_failed, review_pending, ...)
		// is more useful than a stale "needs input" pinned from the last
		// hook callback. Only fall back to needs_input when there is no
		// open (or merged) PR to derive a status from.
		if scmStatus := deriveSCMStatus(prs); scmStatus != "" {
			return scmStatus
		}
		return domain.StatusNeedsInput
	}

	if scmStatus := deriveSCMStatus(prs); scmStatus != "" {
		return scmStatus
	}

	// No hook callback has ever arrived for this spawn/restore even though the
	// harness has a hook pipeline. The seeded LastActivityAt marks the launch,
	// so once the grace passes the honest status is "no signal", not "idle".
	//
	// Chat sessions are exempt regardless of harness. no_signal means "AO cannot
	// tell what this agent is doing", which is inferred from silence because a TUI
	// agent reports through a hook pipeline AO does not own. In chat mode AO holds
	// the provider connection itself: it knows the controller's state directly, and
	// a controller that dies is reported as exited rather than guessed at from
	// silence. An idle chat session waiting on the user emits nothing by design, so
	// inferring a broken pipeline from that silence would be wrong every time.
	if signalCapable && rec.Mode != domain.SessionModeChat &&
		rec.FirstSignalAt.IsZero() && now.Sub(rec.Activity.LastActivityAt) > noSignalGrace {
		return domain.StatusNoSignal
	}
	return domain.StatusIdle
}

// deriveSCMStatus returns the session's stack-aware PR context independently
// of runtime activity. It is empty when the session has no open or merged PR.
func deriveSCMStatus(prs []domain.PRFacts) domain.SessionStatus {
	open := openPRs(prs)
	if len(open) > 0 {
		return aggregatePRStatus(open)
	}
	if anyMerged(prs) {
		return domain.StatusMerged
	}
	return ""
}

// openPRs returns the PRs that are neither merged nor closed, preserving order.
func openPRs(prs []domain.PRFacts) []domain.PRFacts {
	out := make([]domain.PRFacts, 0, len(prs))
	for _, p := range prs {
		if !p.Merged && !p.Closed {
			out = append(out, p)
		}
	}
	return out
}

func anyMerged(prs []domain.PRFacts) bool {
	for _, p := range prs {
		if p.Merged {
			return true
		}
	}
	return false
}

// aggregatePRStatus is the worst-wins reduction over a session's open PRs.
// A stacked child blocked by an open parent cannot merge yet, so its readiness
// signals (mergeable/approved/review-pending/open) are not actionable for the
// session and are suppressed. Its problem signals are still actionable: failing
// CI, draft state, and requested-changes/unresolved-comments must stay visible
// so a broken child is not hidden behind the stack. If no PR contributes any
// signal (a degenerate stack with no visible root), it falls back to aggregating
// the raw status across all open PRs so the session never goes dark.
func aggregatePRStatus(open []domain.PRFacts) domain.SessionStatus {
	stacks := buildStacks(open)
	candidates := make([]domain.SessionStatus, 0, len(open))
	for _, p := range open {
		s := prPipelineStatus(p)
		if stacks[p.URL].Blocked && !isActionableChildSignal(s) {
			continue
		}
		candidates = append(candidates, s)
	}
	if len(candidates) == 0 {
		for _, p := range open {
			candidates = append(candidates, prPipelineStatus(p))
		}
	}
	worst := candidates[0]
	for _, s := range candidates[1:] {
		if statusSeverity(s) < statusSeverity(worst) {
			worst = s
		}
	}
	return worst
}

// isActionableChildSignal reports whether a blocked stacked child's pipeline
// status is a problem the user can act on now, independent of the child's
// inability to merge until its parent does.
func isActionableChildSignal(s domain.SessionStatus) bool {
	switch s {
	case domain.StatusCIFailed, domain.StatusDraft, domain.StatusChangesRequested:
		return true
	default:
		return false
	}
}

// statusSeverity ranks PR pipeline statuses from most to least urgent so the
// aggregate surfaces the PR that most needs attention. mergeable is least urgent
// so a session only reports mergeable when every aggregated PR is mergeable.
func statusSeverity(s domain.SessionStatus) int {
	switch s {
	case domain.StatusCIFailed:
		return 0
	case domain.StatusChangesRequested:
		return 1
	case domain.StatusDraft:
		return 2
	case domain.StatusReviewPending:
		return 3
	case domain.StatusPROpen:
		return 4
	case domain.StatusApproved:
		return 5
	case domain.StatusMergeable:
		return 6
	default:
		return 7
	}
}

func prPipelineStatus(pr domain.PRFacts) domain.SessionStatus {
	switch {
	case pr.CI == domain.CIFailing:
		return domain.StatusCIFailed
	case pr.Draft:
		return domain.StatusDraft
	case pr.Review == domain.ReviewChangesRequest || pr.ReviewComments:
		return domain.StatusChangesRequested
	case pr.Mergeability == domain.MergeMergeable:
		return domain.StatusMergeable
	case pr.Review == domain.ReviewApproved:
		return domain.StatusApproved
	case pr.Review == domain.ReviewRequired:
		return domain.StatusReviewPending
	default:
		return domain.StatusPROpen
	}
}
