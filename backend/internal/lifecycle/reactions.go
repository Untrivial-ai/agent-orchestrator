package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/sessionguard"
)

const reviewMaxNudge = 3

// ReviewDeliveryOutcome reports what ApplyReviewBatch did with completed
// AO-internal review passes.
type ReviewDeliveryOutcome string

const (
	// ReviewDeliveryNoop means lifecycle did not send or confirm a review nudge
	// because the result was not relevant for delivery.
	ReviewDeliveryNoop ReviewDeliveryOutcome = "no_op"
	// ReviewDeliverySent means the worker nudge was sent or was already covered
	// by sendOnce dedup state and may be stamped delivered.
	ReviewDeliverySent ReviewDeliveryOutcome = "sent"
)

// ReviewResult is the already-persisted result of an AO-internal review pass.
// Lifecycle treats it as input to the reaction reducer; it does not write the
// review_run row.
type ReviewResult struct {
	RunID          string
	BatchID        string
	WorkerID       domain.SessionID
	PRURL          string
	TargetSHA      string
	Verdict        domain.ReviewVerdict
	Body           string
	GithubReviewID string
	DeliveredAt    *time.Time
}

// ApplyReviewBatch reacts to one reviewer CLI submission after the review
// service has decided which current-head changes-requested results are
// deliverable.
func (m *Manager) ApplyReviewBatch(ctx context.Context, workerID domain.SessionID, batchID string, results []ReviewResult) (ReviewDeliveryOutcome, error) {
	if batchID == "" || len(results) == 0 {
		return ReviewDeliveryNoop, nil
	}
	rec, ok, err := m.store.GetSession(ctx, workerID)
	if err != nil || !ok {
		return ReviewDeliveryNoop, err
	}
	if cannotNudge(rec) {
		return ReviewDeliveryNoop, nil
	}
	if m.guard == nil {
		return ReviewDeliveryNoop, nil
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].PRURL != results[j].PRURL {
			return results[i].PRURL < results[j].PRURL
		}
		return results[i].RunID < results[j].RunID
	})
	var msg strings.Builder
	fmt.Fprintf(&msg, "[AO reviewer] AO's internal code reviewer submitted %d review(s) requesting changes.\n", len(results))
	var sigParts []string
	for i, r := range results {
		fmt.Fprintf(&msg, "\nReview %d\nPR: %s\nVerdict: %s", i+1, domain.SanitizeControlChars(r.PRURL), domain.SanitizeControlChars(string(r.Verdict)))
		if r.TargetSHA != "" {
			fmt.Fprintf(&msg, "\nHead commit: %s", domain.SanitizeControlChars(r.TargetSHA))
		}
		if r.GithubReviewID != "" {
			safeReviewID := domain.SanitizeControlChars(r.GithubReviewID)
			fmt.Fprintf(&msg, "\nGitHub review: %s", safeReviewID)
			fmt.Fprintf(&msg, "\nOnce you have addressed it, reply on GitHub review %s with how you addressed it, then resolve the review comment threads you addressed.", safeReviewID)
		}
		if r.Body != "" {
			fmt.Fprintf(&msg, "\n\nReview body:\n%s\n", domain.SanitizeControlChars(r.Body))
		}
		sigParts = append(sigParts, strings.Join([]string{r.RunID, r.PRURL, r.TargetSHA, r.GithubReviewID, r.Body}, "\x00"))
	}
	anchorPR := results[0].PRURL
	key := "review-batch:" + anchorPR + ":" + batchID
	sig := strings.Join(sigParts, "\x01")
	outcome, err := m.sendOnce(ctx, workerID, anchorPR, key, sig, msg.String(), reviewMaxNudge)
	if err != nil {
		return ReviewDeliveryNoop, err
	}
	if outcome == sendOnceSuppressed {
		// The worker went terminated/exited/needs-input between the entry guard and the
		// paste: nothing reached it, so do NOT let the caller stamp the run
		// delivered — it must re-fire once the session is workable again.
		return ReviewDeliveryNoop, nil
	}
	return ReviewDeliverySent, nil
}

type reactionState struct {
	mu       sync.Mutex
	seen     map[string]string
	attempts map[string]int
	// loaded tracks PR URLs whose persisted dedup payload has been merged into
	// seen/attempts during this process. Lazy: we only pay the DB read on the
	// first reaction touching each PR after startup.
	loaded map[string]bool
}

func newReactionState() reactionState {
	return reactionState{seen: map[string]string{}, attempts: map[string]int{}, loaded: map[string]bool{}}
}

// reactionPayload is the JSON document persisted in pr.last_nudge_signature.
// Keeping the schema explicit (and stable) lets the daemon restart and resume
// the existing dedup state without re-nudging an agent.
type reactionPayload struct {
	Seen     map[string]string `json:"seen,omitempty"`
	Attempts map[string]int    `json:"attempts,omitempty"`
}

// pendingNudge is one actionable PR nudge queued by ApplyPRObservation. Queuing
// each condition's nudge (instead of sending inline and returning) keeps the
// conditions independent — none can suppress another — and centralizes the
// send + dedup in a single loop.
type pendingNudge struct {
	key         string
	sig         string
	msg         string
	maxAttempts int
}

// ApplyPRObservation reacts to a fetched PR observation after the PR service has
// persisted it. It does not write PR rows; it owns PR-driven lifecycle effects
// and sends actionable agent nudges such as rebase, fix-CI, and
// address-review-feedback prompts.
func (m *Manager) ApplyPRObservation(ctx context.Context, id domain.SessionID, o ports.PRObservation) error {
	if !o.Fetched {
		return nil
	}
	// A PR reaching a terminal state (merged or closed) no longer ends the
	// session on its own: a session may own several PRs. Terminate only when no
	// open PR remains and at least one of them merged. The observer persists the
	// PR row before calling lifecycle, so the store already reflects this
	// transition when sessionComplete reads it.
	if o.Merged || o.Closed {
		rec, ok, err := m.store.GetSession(ctx, id)
		if err != nil || !ok {
			return err
		}
		if rec.IsTerminated || !rec.TerminateOnPRMerge {
			return nil
		}
		done, err := m.sessionComplete(ctx, id)
		if err != nil {
			return err
		}
		if done {
			return m.terminateCompletedSession(ctx, id)
		}
		return nil
	}
	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil || !ok {
		return err
	}
	if cannotNudge(rec) {
		return nil
	}
	reviews, err := m.store.ListPRReviews(ctx, o.URL)
	if err != nil {
		return fmt.Errorf("list persisted reviews for %s: %w", o.URL, err)
	}
	comments, err := m.store.ListPRComments(ctx, o.URL)
	if err != nil {
		return fmt.Errorf("list persisted comments for %s: %w", o.URL, err)
	}
	o.Comments = prCommentObservations(comments)
	// A single PR can trip several actionable conditions at once (failing CI,
	// unresolved review comments, a merge conflict). Queue every applicable nudge
	// and send them together, so each surfaces independently instead of one
	// returning early and hiding the rest — the bug this reducer had, where a CI
	// failure suppressed review feedback on the same PR. Each nudge self-dedups
	// via sendOnce; a send error short-circuits, and nudges already sent have
	// persisted their own dedup signature so the next poll retries only the rest.
	ident := prIdentity(o)
	var nudges []pendingNudge
	var ciPolicyErr error

	if o.CI == domain.CIFailing {
		pr, ok, err := m.store.GetPR(ctx, o.URL)
		switch {
		case err != nil:
			ciPolicyErr = fmt.Errorf("load CI injection policy for %s: %w", o.URL, err)
		case !ok:
			ciPolicyErr = fmt.Errorf("load CI injection policy for %s: PR not found", o.URL)
		case pr.AutoInjectCI:
			checks := failedPRChecks(o.Checks)
			if len(checks) > 0 {
				msg := formatCIFailureMessage(checks)
				if ident != "your PR" {
					msg = strings.Replace(msg, "your PR", ident, 1)
				}
				if o.URL != "" {
					msg += "\nPR: " + domain.SanitizeControlChars(o.URL)
				}
				nudges = append(nudges, pendingNudge{key: "ci:" + o.URL, sig: ciFailureSignature(checks), msg: msg, maxAttempts: 0})
			}
		}
	}

	if hasUnresolvedComments(o.Comments) {
		comments := unresolvedReviewComments(o.Comments)
		for _, comment := range comments {
			if !comment.AutoInjectReview {
				continue
			}
			commentSlice := []ports.PRCommentObservation{comment}
			msg := formatReviewCommentsMessage(commentSlice)
			if ident != "your PR" {
				msg = strings.Replace(msg, "your PR", ident, 1)
			}
			if o.URL != "" {
				msg += "\nPR: " + domain.SanitizeControlChars(o.URL)
			}
			sig := reviewCommentsSignature(commentSlice)
			if sig == "" {
				sig = string(o.Review)
			}
			nudges = append(nudges, pendingNudge{key: "comment:" + o.URL, sig: sig, msg: msg, maxAttempts: reviewMaxNudge})
		}
	}

	if o.Review == domain.ReviewChangesRequest {
		for _, review := range reviews {
			if review.State != domain.ReviewChangesRequest || !review.AutoInjectReview {
				continue
			}
			msg := formatReviewChangesRequestedMessage(review)
			if ident != "your PR" {
				msg = strings.Replace(msg, "your PR", ident, 1)
			}
			if o.URL != "" && review.URL == "" {
				msg += "\nPR: " + domain.SanitizeControlChars(o.URL)
			}
			nudges = append(nudges, pendingNudge{key: "review:" + o.URL + ":" + review.ID, sig: string(review.State), msg: msg, maxAttempts: reviewMaxNudge})
		}
	}

	// Only the merge-conflict nudge needs a store read (the parent-stack check).
	// A read error there must NOT discard the CI/review nudges already queued
	// above — returning early here would re-introduce the "one condition
	// suppresses the others" coupling this queue was built to remove. So on error
	// we skip only the merge-conflict nudge (it self-heals on the next poll) and
	// defer the error past the send loop, still surfacing it so the observer logs
	// and re-polls.
	var blockedCheckErr error
	if o.Mergeability == domain.MergeConflicting {
		// Only the bottom of a stack is eligible for the rebase nudge. A PR
		// stacked on an open parent is expected to report conflicts against its
		// parent branch until the parent merges and it retargets, so nudging the
		// agent to rebase it now would be noise. Mergeability UNKNOWN (the brief
		// post-retarget recompute window) never reaches here.
		blocked, err := m.prBlockedByOpenParent(ctx, id, o.URL)
		switch {
		case err != nil:
			blockedCheckErr = err
		case !blocked:
			msg := "There are merge conflicts on " + ident + ". Rebase onto the base branch and resolve them."
			if o.URL != "" {
				msg += "\nPR: " + domain.SanitizeControlChars(o.URL)
			}
			nudges = append(nudges, pendingNudge{key: "merge-conflict:" + o.URL, sig: string(o.Mergeability), msg: msg, maxAttempts: 0})
		}
	}

	for _, n := range nudges {
		if _, err := m.sendOnce(ctx, id, o.URL, n.key, n.sig, n.msg, n.maxAttempts); err != nil {
			return err
		}
	}
	// Surface deferred policy/parent-stack read errors only after independent
	// nudges have been sent, so one failed lookup cannot hide another reaction.
	if ciPolicyErr != nil {
		return ciPolicyErr
	}
	return blockedCheckErr
}

func (m *Manager) terminateCompletedSession(ctx context.Context, id domain.SessionID) error {
	if m.sessionMutationInProgress(id) {
		return nil
	}
	m.mu.Lock()
	terminator := m.completionTerminator
	m.mu.Unlock()
	if terminator == nil {
		return fmt.Errorf("terminate completed session %s: session terminator is not configured", id)
	}
	if _, err := terminator.Kill(ctx, id); err != nil {
		return fmt.Errorf("terminate completed session %s: %w", id, err)
	}
	return nil
}

// sessionComplete reports whether the session has reached the multi-PR
// completion bar: at least one PR merged and no PR still open. A session with no
// PRs, or with any open PR, is not complete.
func (m *Manager) sessionComplete(ctx context.Context, id domain.SessionID) (bool, error) {
	prs, err := m.store.ListPRsBySession(ctx, id)
	if err != nil {
		return false, err
	}
	merged := false
	for _, pr := range prs {
		if !pr.Merged && !pr.Closed {
			return false, nil
		}
		if pr.Merged {
			merged = true
		}
	}
	return merged, nil
}

// prBlockedByOpenParent reports whether the PR at prURL is stacked on top of
// another still-open PR in the same session — i.e. its target branch is the
// source branch of a sibling open PR. Such a PR is not the bottom of its stack
// and is exempt from merge-conflict nudges. Branch facts are read from the
// store, which the observer has already updated for this observation.
func (m *Manager) prBlockedByOpenParent(ctx context.Context, id domain.SessionID, prURL string) (bool, error) {
	prs, err := m.store.ListPRsBySession(ctx, id)
	if err != nil {
		return false, err
	}
	openSources := make(map[string]bool, len(prs))
	for _, pr := range prs {
		if !pr.Merged && !pr.Closed && pr.SourceBranch != "" {
			openSources[pr.SourceBranch] = true
		}
	}
	for _, pr := range prs {
		if pr.URL == prURL {
			return pr.TargetBranch != "" && openSources[pr.TargetBranch], nil
		}
	}
	return false, nil
}

// ApplySCMObservation is the provider-neutral lifecycle entrypoint used by the
// SCM observer. The existing reaction logic still operates on PRObservation, so
// lifecycle performs the compatibility projection internally instead of leaking
// the old PR DTO back into the observer/provider boundary.
func (m *Manager) ApplySCMObservation(ctx context.Context, id domain.SessionID, o ports.SCMObservation) error {
	if !o.Fetched {
		return nil
	}
	if err := m.ApplyPRObservation(ctx, id, scmToPRObservation(o)); err != nil {
		return err
	}
	intent, err := m.notificationIntentForCurrentSCM(ctx, id, o)
	if err != nil {
		return err
	}
	m.emitNotification(ctx, intent)
	m.resolveNotifications(ctx, readyToMergeResolutions(id, o, m.clock())...)
	return nil
}

// readyToMergeResolutions reports the ready-to-merge notification this
// observation made stale. The PR either got merged/closed, or stopped being
// mergeable — either way the "this is ready for you to merge" ping no longer
// describes anything the user can act on.
func readyToMergeResolutions(id domain.SessionID, o ports.SCMObservation, now time.Time) []ports.NotificationResolution {
	if scmObservationIsReadyToMerge(o) {
		return nil
	}
	return []ports.NotificationResolution{{
		Type:       domain.NotificationReadyToMerge,
		SessionID:  id,
		PRURL:      firstSCMNonEmpty(o.PR.URL, o.PR.HTMLURL),
		ResolvedAt: timeOr(o.ObservedAt, now),
	}}
}

func (m *Manager) notificationIntentForCurrentSCM(ctx context.Context, id domain.SessionID, o ports.SCMObservation) (*ports.NotificationIntent, error) {
	// Serialize the session snapshot with activity transitions so ready-to-merge
	// notifications do not race against a simultaneous waiting_input update.
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return m.notificationIntentForSCM(rec, o), nil
}

func (m *Manager) notificationIntentForSCM(rec domain.SessionRecord, o ports.SCMObservation) *ports.NotificationIntent {
	prURL := firstSCMNonEmpty(o.PR.URL, o.PR.HTMLURL)
	base := ports.NotificationIntent{
		SessionID:          rec.ID,
		ProjectID:          rec.ProjectID,
		PRURL:              prURL,
		CreatedAt:          timeOr(o.ObservedAt, m.clock()),
		SessionDisplayName: rec.DisplayName,
		PRNumber:           o.PR.Number,
		PRTitle:            o.PR.Title,
		PRSourceBranch:     o.PR.SourceBranch,
		PRTargetBranch:     o.PR.TargetBranch,
		Provider:           o.Provider,
		Repo:               o.Repo,
	}
	if o.PR.Merged {
		base.Type = domain.NotificationPRMerged
		return &base
	}
	if o.PR.Closed {
		base.Type = domain.NotificationPRClosedUnmerged
		return &base
	}
	if rec.IsTerminated || rec.Activity.State.NeedsInput() || !scmObservationIsReadyToMerge(o) {
		return nil
	}
	base.Type = domain.NotificationReadyToMerge
	return &base
}

// scmObservationIsReadyToMerge projects a live observation into the shared
// readiness rule (domain.MergeReadiness). Startup reconciliation applies the
// same rule to the stored facts, so the two paths cannot disagree about what
// "ready to merge" means.
func scmObservationIsReadyToMerge(o ports.SCMObservation) bool {
	return domain.MergeReadiness{
		Draft:              o.PR.Draft,
		Merged:             o.PR.Merged,
		Closed:             o.PR.Closed,
		CI:                 domain.CIState(o.CI.Summary),
		Review:             domain.ReviewDecision(o.Review.Decision),
		Mergeability:       domain.Mergeability(o.Mergeability.State),
		UnresolvedComments: hasUnresolvedSCMComments(o.Review.Threads),
	}.ReadyToMerge()
}

func hasUnresolvedSCMComments(threads []ports.SCMReviewThreadObservation) bool {
	for _, th := range threads {
		if th.Resolved || th.IsBot {
			continue
		}
		for _, c := range th.Comments {
			if !c.IsBot {
				return true
			}
		}
	}
	return false
}

func scmToPRObservation(o ports.SCMObservation) ports.PRObservation {
	pr := ports.PRObservation{
		Fetched:      o.Fetched,
		URL:          firstSCMNonEmpty(o.PR.URL, o.PR.HTMLURL),
		Number:       o.PR.Number,
		Title:        o.PR.Title,
		SourceBranch: o.PR.SourceBranch,
		TargetBranch: o.PR.TargetBranch,
		Draft:        o.PR.Draft,
		Merged:       o.PR.Merged,
		Closed:       o.PR.Closed,
		CI:           domain.CIState(o.CI.Summary),
		Review:       domain.ReviewDecision(o.Review.Decision),
		Mergeability: domain.Mergeability(o.Mergeability.State),
	}
	if pr.CI == "" {
		pr.CI = domain.CIUnknown
	}
	if pr.Review == "" {
		pr.Review = domain.ReviewNone
	}
	if pr.Mergeability == "" {
		pr.Mergeability = domain.MergeUnknown
	}

	checkCommit := firstSCMNonEmpty(o.CI.HeadSHA, o.PR.HeadSHA)
	for _, ch := range o.CI.FailedChecks {
		status := domain.PRCheckStatus(ch.Status)
		if status == "" {
			status = domain.PRCheckFailed
		}
		logTail := ch.LogTail
		if logTail == "" {
			logTail = o.CI.FailureLogTail
		}
		pr.Checks = append(pr.Checks, ports.PRCheckObservation{
			Name:       ch.Name,
			CommitHash: checkCommit,
			Status:     status,
			URL:        ch.URL,
			LogTail:    logTail,
		})
	}
	return pr
}

func prCommentObservations(comments []domain.PullRequestComment) []ports.PRCommentObservation {
	out := make([]ports.PRCommentObservation, 0, len(comments))
	for _, comment := range comments {
		if comment.Resolved || comment.IsBot {
			continue
		}
		out = append(out, ports.PRCommentObservation{
			ID:               comment.ID,
			ThreadID:         comment.ThreadID,
			Author:           comment.Author,
			File:             comment.File,
			Line:             comment.Line,
			Body:             comment.Body,
			URL:              comment.URL,
			Resolved:         comment.Resolved,
			AutoInjectReview: comment.AutoInjectReview,
		})
	}
	return out
}

// ApplyTrackerFacts reacts to a fetched Tracker issue observation. It owns the
// issue-driven side of session lifecycle and the initial bot-mention nudge;
// it does NOT persist tracker rows (the future Tracker observer in #35 owns
// the read-side persistence path).
//
// Reactions today:
//   - Issue terminal (state == done or cancelled) → MarkTerminated. The
//     reducer is idempotent — repeat observations on an already-terminated
//     session are no-ops because MarkTerminated skips when IsTerminated.
//   - Assignee changed → log only. No session-state reaction yet; the policy
//     for "assignee changed away from AO" is reserved for the write-side work
//     tracked by #40.
//   - New bot comment → one-time nudge using the same sendOnce + dedup
//     signature pattern as the SCM lane. Dedup is in-memory only for now;
//     cross-restart persistence lands with the Tracker observer (issue #35)
//     when issue-row signature storage is on the table.
func (m *Manager) ApplyTrackerFacts(ctx context.Context, id domain.SessionID, o ports.TrackerObservation) error {
	if !o.Fetched {
		return nil
	}
	if isTerminalTrackerState(o.Issue.State) {
		if m.sessionMutationInProgress(id) {
			return nil
		}
		return m.MarkTerminated(ctx, id)
	}
	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil || !ok {
		return err
	}
	if cannotNudge(rec) {
		return nil
	}
	if o.Changed.Assignee {
		slog.Default().Info("lifecycle: tracker issue assignee changed",
			"session", id, "issue", o.Issue.URL, "assignee", o.Issue.Assignee)
	}
	if o.Changed.Comments {
		bodies, ids := newBotCommentContent(o.Comments)
		if len(ids) > 0 {
			msg := "A bot left a new comment on your tracker issue. Address it and update the session."
			if joined := strings.Join(bodies, "\n\n"); strings.TrimSpace(joined) != "" {
				msg += "\n\n" + joined
			}
			// Empty prURL routes sendOnce through its in-memory-only branch:
			// the PR-row signature load/persist is skipped, so the dedup
			// survives only for the lifetime of this Manager. Cross-restart
			// persistence ships with #35.
			_, err := m.sendOnce(ctx, id, "", "tracker-bot:"+o.Issue.URL, strings.Join(ids, ","), msg, 0)
			return err
		}
	}
	return nil
}

// cannotNudge is the lifecycle entry guard for automated pane delivery. The
// just-in-time sessionguard remains authoritative, but refusing exited agents
// here avoids repeated dedup/store work for a pane that now contains a shell.
func cannotNudge(rec domain.SessionRecord) bool {
	return rec.IsTerminated || rec.Activity.State.NeedsInput() || rec.Activity.State == domain.ActivityExited
}

func isTerminalTrackerState(state domain.NormalizedIssueState) bool {
	return state == domain.IssueDone || state == domain.IssueCancelled
}

func newBotCommentContent(comments []ports.TrackerCommentObservation) ([]string, []string) {
	bodies := make([]string, 0, len(comments))
	ids := make([]string, 0, len(comments))
	for _, c := range comments {
		if !c.IsBot {
			continue
		}
		// Both an ID and a body are required: ID anchors the dedup
		// signature (an empty ID collapses to "" which collides with
		// the zero value of m.react.seen[key] and silently suppresses
		// the nudge), and a body is what we actually need to surface
		// to the agent.
		if c.ID == "" || strings.TrimSpace(c.Body) == "" {
			continue
		}
		bodies = append(bodies, c.Body)
		ids = append(ids, c.ID)
	}
	return bodies, ids
}

func firstSCMNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// prIdentity renders a short, sanitized PR identity ("PR #123 \"Title\"
// (feat/x → main)") for nudge messages so an agent in a multi-PR session can
// tell which PR — and where in a stack — a nudge refers to. Title and branch
// names are provider-controlled and reach the agent's live pane, so both are
// control-char sanitized. Falls back to "your PR" when the number is unknown.
func prIdentity(o ports.PRObservation) string {
	if o.Number <= 0 {
		return "your PR"
	}
	id := fmt.Sprintf("PR #%d", o.Number)
	if o.Title != "" {
		id += fmt.Sprintf(" %q", domain.SanitizeControlChars(o.Title))
	}
	if o.SourceBranch != "" && o.TargetBranch != "" {
		id += fmt.Sprintf(" (%s → %s)", domain.SanitizeControlChars(o.SourceBranch), domain.SanitizeControlChars(o.TargetBranch))
	}
	return id
}

func hasUnresolvedComments(comments []ports.PRCommentObservation) bool {
	for _, c := range comments {
		if !c.Resolved {
			return true
		}
	}
	return false
}

func failedPRChecks(checks []ports.PRCheckObservation) []ports.PRCheckObservation {
	failed := make([]ports.PRCheckObservation, 0, len(checks))
	for _, ch := range checks {
		if ch.Status == domain.PRCheckFailed {
			failed = append(failed, ch)
		}
	}
	return failed
}

func ciFailureSignature(checks []ports.PRCheckObservation) string {
	parts := make([]string, 0, len(checks))
	for _, ch := range checks {
		parts = append(parts, strings.Join([]string{ch.Name, ch.CommitHash, string(ch.Status), ch.URL, ch.LogTail}, "\x00"))
	}
	return strings.Join(parts, "\x01")
}

func formatCIFailureMessage(checks []ports.PRCheckObservation) string {
	var msg strings.Builder
	msg.WriteString("CI is failing on your PR.\n")
	for _, ch := range checks {
		name := domain.SanitizeControlChars(ch.Name)
		if strings.TrimSpace(name) == "" {
			name = "unnamed check"
		}
		status := domain.SanitizeControlChars(string(ch.Status))
		if strings.TrimSpace(status) == "" {
			status = "failed"
		}
		fmt.Fprintf(&msg, "\nFailed: %s (%s)", name, status)
		if ch.URL != "" {
			fmt.Fprintf(&msg, "\nFailure URL: %s", domain.SanitizeControlChars(ch.URL))
		}
		if ch.LogTail != "" {
			// LogTail is raw CI job output; sanitize before it reaches the
			// agent's live pane so embedded escape sequences can't drive the
			// terminal (the dedup signature stays on the raw bytes). The fence
			// grows to contain embedded backtick fences without mutating logs.
			tail := domain.SanitizeControlChars(ch.LogTail)
			fence := markdownCodeFence(tail)
			lineCount := len(strings.Split(tail, "\n"))
			lineLabel := "lines"
			if lineCount == 1 {
				lineLabel = "line"
			}
			fmt.Fprintf(&msg, "\n\nLog tail (last %d %s):\n%s\n%s\n%s", lineCount, lineLabel, fence, tail, fence)
		}
		msg.WriteString("\n")
	}
	msg.WriteString("\nUse the included log tail and failure URL first; fetch full CI logs only if you need additional context. Fix the issues and push again.")
	return msg.String()
}

func unresolvedReviewComments(comments []ports.PRCommentObservation) []ports.PRCommentObservation {
	unresolved := make([]ports.PRCommentObservation, 0, len(comments))
	for _, c := range comments {
		if c.Resolved {
			continue
		}
		unresolved = append(unresolved, c)
	}
	return unresolved
}

func reviewCommentsSignature(comments []ports.PRCommentObservation) string {
	parts := make([]string, 0, len(comments))
	for _, c := range comments {
		id := strings.TrimSpace(c.ID)
		threadID := strings.TrimSpace(c.ThreadID)
		if id == "" && threadID == "" {
			continue
		}
		parts = append(parts, threadID+"\x00"+id)
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x01")
}

func formatReviewChangesRequestedMessage(review domain.PullRequestReview) string {
	var msg strings.Builder
	author := domain.SanitizeControlChars(review.Author)
	if strings.TrimSpace(author) == "" {
		author = "unknown reviewer"
	}
	fmt.Fprintf(&msg, "A changes-requested review from @%s is on your PR.", author)
	if body := domain.SanitizeControlChars(review.Body); strings.TrimSpace(body) != "" {
		fmt.Fprintf(&msg, "\n\nReview body:\n%s", body)
	}
	if review.URL != "" {
		fmt.Fprintf(&msg, "\n\nReview URL: %s", domain.SanitizeControlChars(review.URL))
	}
	if review.ID != "" {
		fmt.Fprintf(&msg, "\nReview ID: %s", domain.SanitizeControlChars(review.ID))
	}
	msg.WriteString("\n\nAddress the requested changes and push. You should not need to re-fetch the review unless you need additional context beyond what AO has provided here.")
	return msg.String()
}

func formatReviewCommentsMessage(comments []ports.PRCommentObservation) string {
	if len(comments) == 0 {
		return "A reviewer left feedback on your PR. Address it and push. Fetch the review details only if you need additional context beyond what AO has provided here."
	}
	var msg strings.Builder
	fmt.Fprintf(&msg, "The following %d unresolved review comment(s) are on your PR as of just now. You should not need to re-fetch this data unless you need additional context.\n", len(comments))
	for i, c := range comments {
		location := "(general)"
		if c.File != "" {
			location = domain.SanitizeControlChars(c.File)
			if c.Line > 0 {
				location = fmt.Sprintf("%s:%d", location, c.Line)
			}
		}
		author := domain.SanitizeControlChars(c.Author)
		if strings.TrimSpace(author) == "" {
			author = "unknown reviewer"
		}
		// Comment bodies are attacker-influenced (anyone who can comment on the
		// PR) and get pasted into the agent's live pane; strip control/escape
		// chars before formatting them.
		body := domain.SanitizeControlChars(c.Body)
		fmt.Fprintf(&msg, "\n%d. %s (@%s):\n%s", i+1, location, author, body)
		if c.URL != "" {
			fmt.Fprintf(&msg, "\n   %s", domain.SanitizeControlChars(c.URL))
		}
		if c.ThreadID != "" {
			fmt.Fprintf(&msg, "\n   Thread ID: %s", domain.SanitizeControlChars(c.ThreadID))
		}
		msg.WriteString("\n")
	}
	msg.WriteString("\nAddress each comment and push fixes. Use the thread ID to resolve each thread directly after pushing when available. You should not need to re-fetch review data unless you need additional context beyond what is provided here.")
	return msg.String()
}

func markdownCodeFence(s string) string {
	maxRun := 0
	run := 0
	for _, r := range s {
		if r == '`' {
			run++
			if run > maxRun {
				maxRun = run
			}
			continue
		}
		run = 0
	}
	if maxRun < 3 {
		return "```"
	}
	return strings.Repeat("`", maxRun+1)
}

// sendOnceOutcome tells a caller whether a nudge is accounted for (actually
// sent, or already covered by dedup state) versus suppressed by the just-in-time
// session guard. It matters for review delivery: a suppressed nudge must NOT be
// stamped delivered, or the feedback is lost when the session later unblocks.
type sendOnceOutcome int

const (
	// sendOnceAccounted: the message was sent, or a prior identical send is
	// already recorded (dedup hit) or the attempt budget is spent. In every
	// case the caller may treat the nudge as delivered — nothing more to do.
	sendOnceAccounted sendOnceOutcome = iota
	// sendOnceSuppressed: the just-in-time guard skipped the paste because the
	// session is terminated or awaiting the user (blocked/waiting_input). The
	// message did NOT reach the worker; the caller must not mark it delivered so
	// it re-fires on the next observation once the session is workable again.
	sendOnceSuppressed
)

func (m *Manager) sendOnce(ctx context.Context, id domain.SessionID, prURL, key, sig, msg string, maxAttempts int) (sendOnceOutcome, error) {
	if m.guard == nil {
		return sendOnceAccounted, nil
	}
	m.react.mu.Lock()
	defer m.react.mu.Unlock()

	if prURL != "" && !m.react.loaded[prURL] {
		if err := m.loadPRSignaturesLocked(ctx, prURL); err != nil {
			return sendOnceAccounted, err
		}
		m.react.loaded[prURL] = true
	}

	if m.react.seen[key] == sig {
		return sendOnceAccounted, nil
	}
	attempts := m.react.attempts[key]
	if maxAttempts > 0 && attempts >= maxAttempts {
		return sendOnceAccounted, nil
	}
	// The guard re-reads the session immediately before pasting: the caller's
	// NeedsInput() entry check ran before this function's dedup/persist I/O, so
	// a permission hook could have stored blocked (or the session could have
	// exited/terminated) in the meantime. A suppressed write returns SUPPRESSED (not
	// accounted), so a review caller won't stamp it delivered and it re-fires
	// once the session is workable again. A store failure inside the guard also
	// suppresses (fail closed, nothing was written); a messenger failure means
	// the write was attempted and stays accounted, matching the pre-guard
	// behavior.
	outcome, err := m.guard.Nudge(ctx, id, msg)
	if err != nil {
		if outcome != sessionguard.Sent {
			return sendOnceSuppressed, err
		}
		return sendOnceAccounted, err
	}
	if outcome != sessionguard.Sent {
		return sendOnceSuppressed, nil
	}
	// Order: Send → in-memory mutation → durable persist. Sending first means a
	// transient persist failure does NOT swallow a real send (the agent saw the
	// message; subsequent polls in this process suppress re-sends via the
	// in-memory dedup). A persist failure that survives until a daemon restart
	// degrades to one extra nudge — preferred over the inverse (persist before
	// send, then crash mid-call) which would silently lose a real nudge.
	m.react.seen[key] = sig
	m.react.attempts[key] = attempts + 1
	if prURL != "" {
		if err := m.persistPRSignaturesLocked(ctx, prURL); err != nil {
			return sendOnceAccounted, err
		}
	}
	return sendOnceAccounted, nil
}

// loadPRSignaturesLocked merges any previously persisted reaction-dedup state
// for prURL into the in-memory maps. Caller must hold m.react.mu.
func (m *Manager) loadPRSignaturesLocked(ctx context.Context, prURL string) error {
	raw, err := m.store.GetPRLastNudgeSignature(ctx, prURL)
	if err != nil {
		return err
	}
	if raw == "" {
		return nil
	}
	// A corrupt persisted payload must not crash the lifecycle write path;
	// the worst case from a swallow is re-firing a nudge once.
	var p reactionPayload
	_ = json.Unmarshal([]byte(raw), &p)
	for k, v := range p.Seen {
		if _, ok := m.react.seen[k]; !ok {
			m.react.seen[k] = v
		}
	}
	for k, v := range p.Attempts {
		if cur, ok := m.react.attempts[k]; !ok || v > cur {
			m.react.attempts[k] = v
		}
	}
	return nil
}

// persistPRSignaturesLocked serialises every reaction-dedup entry whose key
// references prURL and writes the JSON payload back via the store. Caller must
// hold m.react.mu. A failed persist surfaces upward so the in-memory mutation
// (which the messenger already acted on) is not silently divergent from disk.
func (m *Manager) persistPRSignaturesLocked(ctx context.Context, prURL string) error {
	payload := reactionPayload{Seen: map[string]string{}, Attempts: map[string]int{}}
	for k, v := range m.react.seen {
		if reactionKeyTargetsPR(k, prURL) {
			payload.Seen[k] = v
		}
	}
	for k, v := range m.react.attempts {
		if reactionKeyTargetsPR(k, prURL) {
			payload.Attempts[k] = v
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return m.store.UpdatePRLastNudgeSignature(ctx, prURL, string(raw))
}

// reactionKeyTargetsPR matches the "<type>:<url>[:<extra>]" reaction keys used
// by ApplyPRObservation. Anchoring on the second colon-delimited segment keeps
// PR-specific keys grouped with the row that survives a restart.
func reactionKeyTargetsPR(key, prURL string) bool {
	if prURL == "" {
		return false
	}
	parts := strings.SplitN(key, ":", 2)
	if len(parts) != 2 {
		return false
	}
	rest := parts[1]
	return rest == prURL || strings.HasPrefix(rest, prURL+":")
}
