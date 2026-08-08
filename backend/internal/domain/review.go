package domain

import (
	"errors"
	"time"
)

// ErrDuplicateReviewRun is returned by InsertReviewRun when a run already exists
// for the same worker session and target commit (the partial unique index from
// migration 0013). It lets the review engine fall back to the recorded run
// instead of surfacing a raw storage error after a reviewer may have launched.
var ErrDuplicateReviewRun = errors.New("domain: review run already exists for session and target sha")

// Review is the per-worker, per-reviewer-harness code-review record. A repeat
// trigger for the same harness reuses this row; the per-pass facts live on
// ReviewRun.
type Review struct {
	ID        string          `json:"id"`
	SessionID SessionID       `json:"sessionId"`
	ProjectID ProjectID       `json:"projectId"`
	Harness   ReviewerHarness `json:"harness"`
	PRURL     string          `json:"prUrl"`
	// ReviewerHandleID is the runtime handle of the live reviewer pane, reused
	// across passes and exposed so the UI can attach its terminal.
	ReviewerHandleID string    `json:"reviewerHandleId"`
	AgentSessionID   string    `json:"agentSessionId"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// ReviewRun is one review pass against a worker's PR.
type ReviewRun struct {
	ID        string    `json:"id"`
	ReviewID  string    `json:"reviewId"`
	SessionID SessionID `json:"sessionId"`
	// BatchID groups review runs created by one trigger so worker feedback can
	// be delivered once after the whole trigger batch is terminal. Empty marks
	// legacy/single-run delivery.
	BatchID string          `json:"batchId"`
	Harness ReviewerHarness `json:"harness"`
	PRURL   string          `json:"prUrl"`
	// TargetSHA is the PR head commit this pass reviewed.
	TargetSHA string          `json:"targetSha"`
	Status    ReviewRunStatus `json:"status"`
	Verdict   ReviewVerdict   `json:"verdict"`
	// Body is the review text the reviewer submitted. It is recorded for AO's
	// own tracking; the reviewer also posts the review to the PR itself.
	Body string `json:"body"`
	// GithubReviewID is the id of the GitHub PR review the reviewer posted for
	// this pass (the `gh api .../pulls/{n}/reviews` object id), recorded at
	// submit time. It is empty when the reviewer could not post to the provider.
	// When the pass requests changes, AO includes it in the message to the
	// worker so the worker knows exactly which review to address and reply to.
	GithubReviewID string     `json:"githubReviewId"`
	CreatedAt      time.Time  `json:"createdAt"`
	DeliveredAt    *time.Time `json:"deliveredAt,omitempty"`
	// AutoInjectReview snapshots the session policy when this result is first
	// recorded. Later toggle changes must not rewrite or deliver this run.
	AutoInjectReview bool `json:"autoInjectReview"`
}

// ReviewRunStatus is the lifecycle state of a single review pass.
type ReviewRunStatus string

// Review run statuses.
const (
	ReviewRunRunning   ReviewRunStatus = "running"
	ReviewRunComplete  ReviewRunStatus = "complete"
	ReviewRunDelivered ReviewRunStatus = "delivered"
	ReviewRunFailed    ReviewRunStatus = "failed"
	ReviewRunCancelled ReviewRunStatus = "cancelled"
)

// ReviewVerdict is the outcome a reviewer reports. The empty verdict marks a
// run that has not produced an outcome yet.
type ReviewVerdict string

// Review verdicts.
const (
	VerdictNone             ReviewVerdict = ""
	VerdictApproved         ReviewVerdict = "approved"
	VerdictChangesRequested ReviewVerdict = "changes_requested"
)

// Valid reports whether v is a verdict a reviewer may submit (the empty verdict
// is a stored default, not a submittable one).
func (v ReviewVerdict) Valid() bool {
	return v == VerdictApproved || v == VerdictChangesRequested
}
