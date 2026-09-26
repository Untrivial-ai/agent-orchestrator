package contract

// SummaryFacts are the server-owned facts used to derive a short Kanban card
// summary at read time. Conversation content is intentionally not part of this
// contract, so prompts and agent/tool protocol output cannot leak into cards.
type SummaryFacts struct {
	// IsTerminated suppresses the summary: archived cards show no activity line.
	IsTerminated bool
	// LatestAssistantUpdate is the latest user-facing assistant update observed
	// for the session. Empty when nothing has been observed yet.
	LatestAssistantUpdate string
	// LatestUserPrompt is the latest real user-authored task direction.
	LatestUserPrompt string
	// OpenPRs counts pull requests that are neither merged nor closed.
	OpenPRs int
	// CIFailing reports whether any open PR has failing checks.
	CIFailing bool
	// DisplayStatus is the column phrase already derived for the card. It is
	// the fallback when no richer fact is available.
	DisplayStatus DisplayStatus
}

// SummarizeSession derives the generic activity line for a Kanban card from
// server-owned state only. Conversation prompts and assistant/tool output are
// deliberately excluded: they are not summaries and may contain protocol
// markup or implementation chatter. Empty means the card renders no summary.
func SummarizeSession(facts SummaryFacts) string {
	if facts.IsTerminated {
		return ""
	}
	if pr := prSummary(facts); pr != "" {
		return pr
	}
	return statusSummary(facts.DisplayStatus)
}

func statusSummary(status DisplayStatus) string {
	switch status {
	case DisplayWorking:
		return "Working on the task"
	case DisplayBlocked:
		return "Waiting for input"
	case DisplayExited:
		return "Agent exited before opening a pull request"
	case DisplayNoSignal:
		return "No recent agent activity"
	case DisplayAwaitingPR:
		return "Preparing a pull request"
	case DisplayFixingCI:
		return "Fixing failing checks"
	case DisplayAddressingComments:
		return "Addressing review comments"
	case DisplayNeedsReview:
		return "Changes are ready for review"
	case DisplayReviewScheduled:
		return "Review scheduled"
	case DisplayReviewing:
		return "Review in progress"
	case DisplayReviewPending:
		return "Waiting for review"
	case DisplayDraft:
		return "Draft pull request in progress"
	case DisplayCIFailing:
		return "Fixes are needed before merge"
	case DisplayCommented:
		return "Review comments need attention"
	case DisplayChangesRequested:
		return "Requested changes need attention"
	case DisplayNeedsHumanReview:
		return "Waiting for your review"
	case DisplayMergeable:
		return "Changes are ready to merge"
	case DisplayApproved:
		return "Changes are approved"
	case DisplayMerged:
		return "Changes have been merged"
	case DisplayClosed:
		return "Pull request closed without merging"
	case DisplayTerminated:
		return "Session ended"
	default:
		return ""
	}
}

func prSummary(facts SummaryFacts) string {
	switch {
	case facts.CIFailing:
		return "CI failing on open PR"
	case facts.OpenPRs > 0:
		return "PR open"
	default:
		return ""
	}
}
