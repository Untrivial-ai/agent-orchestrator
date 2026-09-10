package contract

import "time"

// KanbanColumn is the derived delivery-lifecycle placement of a session. It
// answers where the session sits between first commit and merge, and which
// loop is turning it. It is independent of the display SessionStatus and is
// never persisted.
type KanbanColumn string

// KanbanColumn values shown as board lanes by AO clients.
const (
	// KanbanBuilding is a session with no PR yet.
	KanbanBuilding KanbanColumn = "building"
	// KanbanValidating is a PR inside an AO-driven loop: a review pass running
	// on the current head, auto review holding the PR until its own pass
	// approves, AO addressing review feedback, or AO fixing CI.
	KanbanValidating KanbanColumn = "validating"
	// KanbanNeedsReview is the review-feedback loop: the PR is in its review
	// cycle and the next turn is a person's, whether that is giving the review,
	// answering the feedback already on it, or deciding what to do about a
	// failing check. It is not limited to PRs awaiting a first human review,
	// and it does not mean the work is idle.
	KanbanNeedsReview KanbanColumn = "needs_review"
	// KanbanReady is a PR merged, closed, mergeable, or approved by a person.
	KanbanReady KanbanColumn = "ready"
	// KanbanArchive is a terminated session.
	KanbanArchive KanbanColumn = "archive"
)

// KanbanSessionFacts are the session-level durable facts the Kanban reducers
// read: the worker facts DeriveStatus already reads, plus which follow-up loops
// AO drives on the session's behalf. The inject flags decide whether the
// review-feedback loop is turned by AO or by a person; the embedded worker
// facts are what a session with no PR yet, or a worker that stopped mid-loop,
// has to report.
type KanbanSessionFacts struct {
	SessionFacts
	AutoReview       bool
	AutoInjectReview bool
	AutoInjectCI     bool
}

// KanbanReviewRunFacts summarize AO's own review passes against one PR's
// current head commit. Passes recorded for an earlier head are excluded before
// this struct is built, so a stale run can never decide the column.
type KanbanReviewRunFacts struct {
	// Present reports that AO recorded at least one pass for the current head.
	Present bool
	// Running reports that a pass for the current head is still in flight.
	Running bool
	// ChangesRequested reports that a pass for the current head asked the
	// worker for changes.
	ChangesRequested bool
	// Outcome reports that a pass for the current head returned a verdict.
	// Present without Outcome is a head AO tried and failed to review, which
	// still owes the PR the pass auto review promised it.
	Outcome bool
	// Failed reports that a pass for the current head ended without producing
	// a verdict.
	Failed bool
	// Cancelled reports that a pass for the current head was cancelled.
	Cancelled bool
}

// KanbanExternalReviewFacts are the provider review verdicts on one PR that AO
// did not author. AO's own provider reviews are matched by review id and
// excluded, because the aggregate ReviewDecision mixes both sources and cannot
// tell whose turn the review-feedback loop is on.
type KanbanExternalReviewFacts struct {
	Approved         bool
	ChangesRequested bool
	Comments         bool
}

// KanbanPRFacts are the per-PR facts the column reducer reads.
type KanbanPRFacts struct {
	URL            string
	Draft          bool
	Merged         bool
	Closed         bool
	CI             CIState
	Review         ReviewDecision
	Mergeability   Mergeability
	UpdatedAt      time.Time
	ReviewRun      KanbanReviewRunFacts
	ExternalReview KanbanExternalReviewFacts
}

func derivePRKanbanColumn(session KanbanSessionFacts, pr KanbanPRFacts) KanbanColumn {
	switch {
	case pr.Merged || pr.Closed:
		return KanbanReady
	case pr.Draft:
		return KanbanValidating
	case externallyApproved(pr) || pr.Mergeability == MergeMergeable:
		return KanbanReady
	case aoOwnsNextStep(session, pr):
		return KanbanValidating
	// Auto review owns this head until its own pass approves it. A head AO has
	// not reviewed yet, a pass that failed or was cancelled, and a pass that
	// asked for changes are all "not approved yet" -- auto review's job is to
	// keep re-reviewing this PR until it can approve, whether or not anything
	// is configured to act on what it finds in between. Without AutoReview, a
	// changes-requested verdict is as far as AO's involvement goes, so it does
	// release the PR from Validating -- see aoOwnsNextStep above.
	case session.AutoReview && !approvedByAO(pr):
		return KanbanValidating
	// Fallthrough: the PR is in its review cycle and no AO loop is turning it,
	// so the next turn is a person's -- give the review, answer the feedback
	// already on it, or decide what to do about a failing check.
	default:
		return KanbanNeedsReview
	}
}

// approvedByAO reports whether AO's own review pass approved the PR's current
// head. A pass that requested changes, one that has not run yet, and one that
// failed or was cancelled without a verdict are all "not approved."
func approvedByAO(pr KanbanPRFacts) bool {
	return pr.ReviewRun.Outcome && !pr.ReviewRun.ChangesRequested
}

// externallyApproved requires both the provider's aggregate decision (which
// honors dismissed reviews) and a surviving approval AO did not author.
func externallyApproved(pr KanbanPRFacts) bool {
	return pr.Review == ReviewApproved && pr.ExternalReview.Approved
}

// aoOwnsNextStep reports whether AO itself is turning the PR's review-feedback
// loop: its review pass on the current head is still running, it is addressing
// review feedback, or it is fixing failing CI. When it is not, the same loop
// continues with a person taking the next turn.
func aoOwnsNextStep(session KanbanSessionFacts, pr KanbanPRFacts) bool {
	if pr.ReviewRun.Running {
		return true
	}
	if session.AutoInjectReview && pr.ReviewRun.ChangesRequested {
		return true
	}
	return session.AutoInjectCI && pr.CI == CIFailing
}

func liveKanbanPRs(prs []KanbanPRFacts) []KanbanPRFacts {
	live := make([]KanbanPRFacts, 0, len(prs))
	for _, pr := range prs {
		if !pr.Merged && !pr.Closed {
			live = append(live, pr)
		}
	}
	return live
}

// outranksKanban picks the more actionable of two placements, breaking ties on
// the most recently updated PR and finally on URL so the board never flickers
// between equally ranked PRs.
func outranksKanban(candidate KanbanColumn, pr KanbanPRFacts, current KanbanColumn, chosen KanbanPRFacts) bool {
	if kanbanPriority(candidate) != kanbanPriority(current) {
		return kanbanPriority(candidate) < kanbanPriority(current)
	}
	if !pr.UpdatedAt.Equal(chosen.UpdatedAt) {
		return pr.UpdatedAt.After(chosen.UpdatedAt)
	}
	return pr.URL < chosen.URL
}

func kanbanPriority(column KanbanColumn) int {
	switch column {
	case KanbanReady:
		return 0
	case KanbanNeedsReview:
		return 1
	case KanbanValidating:
		return 2
	default:
		return 3
	}
}

// DisplayStatus is the short phrase shown inside a session's Kanban column. It
// answers what is happening right now in the column the session already sits
// in, so it changes with CI, review runs, activity, and approvals while the
// column stays put. Values are already renderable: clients print them as-is.
type DisplayStatus string

// DisplayStatus values shown on AO session cards, grouped by the column that
// can produce them.
const (
	// Building.
	DisplayWorking    DisplayStatus = "Working"
	DisplayBlocked    DisplayStatus = "Blocked"
	DisplayExited     DisplayStatus = "Exited"
	DisplayNoSignal   DisplayStatus = "No signal"
	DisplayAwaitingPR DisplayStatus = "Awaiting PR"
	// Validating.
	DisplayFixingCI           DisplayStatus = "Fixing CI failures"
	DisplayAddressingComments DisplayStatus = "Addressing comments"
	DisplayNeedsReview        DisplayStatus = "Needs review"
	DisplayReviewScheduled    DisplayStatus = "Review scheduled"
	DisplayReviewing          DisplayStatus = "Reviewing"
	DisplayReviewPending      DisplayStatus = "Review pending"
	DisplayDraft              DisplayStatus = "Draft"
	// In review.
	DisplayCIFailing        DisplayStatus = "CI failing"
	DisplayCommented        DisplayStatus = "Commented"
	DisplayChangesRequested DisplayStatus = "Changes requested"
	DisplayNeedsHumanReview DisplayStatus = "Needs human review"
	// Ready.
	DisplayMergeable DisplayStatus = "Mergeable"
	DisplayApproved  DisplayStatus = "Approved"
	DisplayMerged    DisplayStatus = "Merged"
	DisplayClosed    DisplayStatus = "Closed without merge"
	// Archive.
	DisplayTerminated DisplayStatus = "Terminated"
)

// KanbanPresentation is a session's derived board presentation: the column it
// belongs to and the display status derived inside that column.
type KanbanPresentation struct {
	Column        KanbanColumn
	DisplayStatus DisplayStatus
}

// DeriveKanbanPresentation derives a session's board placement and the phrase
// shown on its card, in that order. The column is chosen first from lifecycle
// facts; the display status is then derived from the facts that column cares
// about, so a session never shows a phrase belonging to a stage it is not in.
//
// With several PRs the column is picked per PR and ranked, and the winning PR
// is the one whose facts the display status reads. A merged or closed PR
// therefore cannot speak for a session that still has live work.
func DeriveKanbanPresentation(
	session KanbanSessionFacts,
	prs []KanbanPRFacts,
	now time.Time,
	noSignalGrace time.Duration,
) KanbanPresentation {
	if session.IsTerminated {
		return KanbanPresentation{Column: KanbanArchive, DisplayStatus: DisplayTerminated}
	}
	if len(prs) == 0 {
		return KanbanPresentation{
			Column:        KanbanBuilding,
			DisplayStatus: buildingDisplayStatus(session, now, noSignalGrace),
		}
	}
	// A terminal PR must not hide a live one still moving through either loop;
	// merged/closed placements count only once nothing is live.
	pool := liveKanbanPRs(prs)
	if len(pool) == 0 {
		pool = prs
	}

	column := KanbanColumn("")
	var chosen KanbanPRFacts
	for _, pr := range pool {
		candidate := derivePRKanbanColumn(session, pr)
		if column == "" || outranksKanban(candidate, pr, column, chosen) {
			column, chosen = candidate, pr
		}
	}
	return KanbanPresentation{
		Column:        column,
		DisplayStatus: displayStatusInColumn(column, session, chosen, now, noSignalGrace),
	}
}

func displayStatusInColumn(
	column KanbanColumn,
	session KanbanSessionFacts,
	pr KanbanPRFacts,
	now time.Time,
	noSignalGrace time.Duration,
) DisplayStatus {
	switch column {
	case KanbanValidating:
		return validatingDisplayStatus(session, pr)
	case KanbanNeedsReview:
		return inReviewDisplayStatus(session, pr)
	case KanbanReady:
		return readyDisplayStatus(pr)
	default:
		return buildingDisplayStatus(session, now, noSignalGrace)
	}
}

// buildingDisplayStatus explains worker progress, because a session with no PR
// has produced no delivery facts to report yet.
func buildingDisplayStatus(session KanbanSessionFacts, now time.Time, noSignalGrace time.Duration) DisplayStatus {
	switch {
	case session.Activity == ActivityActive:
		return DisplayWorking
	case session.Activity == ActivityBlocked || session.Activity == ActivityWaitingInput:
		return DisplayBlocked
	case session.Activity == ActivityExited:
		return DisplayExited
	case silentPastGrace(session.SessionFacts, now, noSignalGrace):
		return DisplayNoSignal
	default:
		return DisplayAwaitingPR
	}
}

// validatingDisplayStatus reports the AO-driven loop turning the PR. A worker
// that needs a person outranks the loop it was running, and the work AO is
// doing outranks the review pass that asked for it.
func validatingDisplayStatus(session KanbanSessionFacts, pr KanbanPRFacts) DisplayStatus {
	switch {
	case session.Activity == ActivityBlocked || session.Activity == ActivityWaitingInput:
		return DisplayBlocked
	case session.Activity == ActivityExited:
		return DisplayExited
	case pr.CI == CIFailing && session.AutoInjectCI:
		return DisplayFixingCI
	case pr.CI == CIFailing:
		return DisplayCIFailing
	case changesRequestedOn(pr) && session.AutoInjectReview:
		return DisplayAddressingComments
	case pr.ReviewRun.ChangesRequested:
		return DisplayNeedsReview
	case session.AutoReview && !pr.ReviewRun.Present:
		return DisplayReviewScheduled
	case pr.ReviewRun.Running:
		return DisplayReviewing
	case pr.ReviewRun.Failed:
		return DisplayNeedsReview
	case pr.ReviewRun.Cancelled:
		return DisplayReviewPending
	case pr.Draft:
		return DisplayDraft
	default:
		return DisplayNeedsReview
	}
}

// inReviewDisplayStatus reports the review-feedback loop from the person's
// side. By the column rule, aoOwnsNextStep already routes a PR with failing CI
// under AutoInjectCI, or an AO-addressed changes request, to Validating before
// this ever runs -- so these guards do not change today's output. They stay
// here, matching validatingDisplayStatus's shape, so this function reports the
// AO-policy phrase correctly on its own rather than depending on a rule
// enforced in a different function for its correctness.
func inReviewDisplayStatus(session KanbanSessionFacts, pr KanbanPRFacts) DisplayStatus {
	switch {
	case pr.CI == CIFailing && session.AutoInjectCI:
		return DisplayFixingCI
	case pr.CI == CIFailing:
		return DisplayCIFailing
	case pr.ExternalReview.Comments && session.AutoInjectReview:
		return DisplayAddressingComments
	case pr.ExternalReview.ChangesRequested && session.AutoInjectReview:
		return DisplayAddressingComments
	case pr.ExternalReview.ChangesRequested:
		return DisplayChangesRequested
	case pr.ExternalReview.Comments:
		return DisplayCommented
	default:
		return DisplayNeedsHumanReview
	}
}

// changesRequestedOn reports whether anyone asked the worker for changes on the
// PR, whether that was AO's own pass or a person.
func changesRequestedOn(pr KanbanPRFacts) bool {
	return pr.ReviewRun.ChangesRequested || pr.ExternalReview.ChangesRequested
}

// readyDisplayStatus reports how the PR landed, or what is left between an
// approval and the merge button. Merged and closed come first: they are what
// happened to the PR, and no merge-readiness reading can override them.
func readyDisplayStatus(pr KanbanPRFacts) DisplayStatus {
	switch {
	case pr.Merged:
		return DisplayMerged
	case pr.Closed:
		return DisplayClosed
	case pr.Mergeability == MergeMergeable:
		return DisplayMergeable
	case pr.CI == CIFailing:
		return DisplayCIFailing
	default:
		return DisplayApproved
	}
}
