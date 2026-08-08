package domain

import "time"

// ---- PR read model ----

// PRFacts is the per-session PR snapshot the status derivation reads from the
// pr table.
type PRFacts struct {
	URL            string
	Number         int
	Draft          bool
	Merged         bool
	Closed         bool
	CI             CIState
	Review         ReviewDecision
	Mergeability   Mergeability
	ReviewComments bool // has unresolved review comments (any author) to address
	SourceBranch   string
	TargetBranch   string
	HeadSHA        string
	UpdatedAt      time.Time
}

// PullRequest is the app-level representation of one tracked pull request as
// persisted by the PR store. It is intentionally separate from the sqlc
// generated sqlite row type so storage details do not leak outside sqlite.
type PullRequest struct {
	URL          string
	SessionID    SessionID
	Number       int
	Draft        bool
	Merged       bool
	Closed       bool
	CI           CIState
	Review       ReviewDecision
	Mergeability Mergeability
	UpdatedAt    time.Time
	// StateChangedAt is when the current normalized PR lifecycle state became
	// active. It is seeded from provider timestamps and updated when AO observes
	// a draft/open/merged/closed transition.
	StateChangedAt time.Time

	Provider string
	Host     string
	Repo     string

	SourceBranch   string
	TargetBranch   string
	HeadSHA        string
	Title          string
	Additions      int
	Deletions      int
	ChangedFiles   int
	Author         string
	BaseSHA        string
	MergeCommitSHA string

	ProviderState            string
	ProviderMergeable        string
	ProviderMergeStateStatus string
	HTMLURL                  string

	CreatedAtProvider time.Time
	UpdatedAtProvider time.Time
	MergedAtProvider  time.Time
	ClosedAtProvider  time.Time

	MetadataHash string
	CIHash       string
	ReviewHash   string

	ObservedAt       time.Time
	CIObservedAt     time.Time
	ReviewObservedAt time.Time
}

// PullRequestCheck is one normalized CI check run for a pull request.
type PullRequestCheck struct {
	Name       string
	CommitHash string
	Status     PRCheckStatus
	Conclusion string
	URL        string
	Details    string
	LogTail    string
	CreatedAt  time.Time
}

// PullRequestComment is one normalized review comment for a pull request.
type PullRequestComment struct {
	ThreadID         string
	ID               string
	Author           string
	File             string
	Line             int
	Body             string
	URL              string
	Resolved         bool
	IsBot            bool
	CreatedAt        time.Time
	AutoInjectReview bool
}

// PullRequestReviewThread is one normalized review thread for a pull request.
type PullRequestReviewThread struct {
	ThreadID     string
	Path         string
	Line         int
	Resolved     bool
	IsBot        bool
	SemanticHash string
	UpdatedAt    time.Time
}

// PullRequestReview is one submitted provider review for a pull request.
type PullRequestReview struct {
	ID               string
	Author           string
	State            ReviewDecision
	URL              string
	Body             string
	IsBot            bool
	SubmittedAt      time.Time
	AutoInjectReview bool
}

// CIState is the aggregate CI status of a PR.
type CIState string

// CI states.
const (
	CIUnknown CIState = "unknown"
	CIPending CIState = "pending"
	CIPassing CIState = "passing"
	CIFailing CIState = "failing"
)

// ReviewDecision is the aggregate human-review verdict on a PR.
type ReviewDecision string

// Review decisions.
const (
	ReviewNone           ReviewDecision = "none"
	ReviewApproved       ReviewDecision = "approved"
	ReviewChangesRequest ReviewDecision = "changes_requested"
	ReviewRequired       ReviewDecision = "review_required"
)

// Mergeability is whether a PR can currently be merged.
type Mergeability string

// Mergeability states.
const (
	MergeUnknown     Mergeability = "unknown"
	MergeMergeable   Mergeability = "mergeable"
	MergeConflicting Mergeability = "conflicting"
	MergeBlocked     Mergeability = "blocked"
	MergeUnstable    Mergeability = "unstable"
)

// PRState is the normalized lifecycle of one tracked pull request as stored in
// the pr table.
type PRState string

// PR states.
const (
	PRStateDraft  PRState = "draft"
	PRStateOpen   PRState = "open"
	PRStateMerged PRState = "merged"
	PRStateClosed PRState = "closed"
)

// PRCheckStatus is one CI check run's normalized status.
type PRCheckStatus string

// PR check statuses.
const (
	PRCheckUnknown    PRCheckStatus = "unknown"
	PRCheckQueued     PRCheckStatus = "queued"
	PRCheckInProgress PRCheckStatus = "in_progress"
	PRCheckPassed     PRCheckStatus = "passed"
	PRCheckFailed     PRCheckStatus = "failed"
	PRCheckSkipped    PRCheckStatus = "skipped"
	PRCheckCancelled  PRCheckStatus = "cancelled"
)

// MergeReadiness is the set of durable PR facts that decide whether a PR is
// waiting on nothing but the user's merge click. It is deliberately the stored
// shape rather than a provider observation, so the live SCM path and the
// startup reconciliation path can share one rule instead of writing it twice
// and drifting apart.
type MergeReadiness struct {
	Draft              bool
	Merged             bool
	Closed             bool
	CI                 CIState
	Review             ReviewDecision
	Mergeability       Mergeability
	UnresolvedComments bool
}

// ReadyToMerge reports whether the PR has no known blocker left. An unknown or
// still-running CI result is treated as a blocker: AO only claims readiness it
// can actually prove.
func (r MergeReadiness) ReadyToMerge() bool {
	if r.Merged || r.Closed || r.Draft {
		return false
	}
	ci := r.CI
	if ci == "" {
		ci = CIUnknown
	}
	switch ci {
	case CIFailing, CIPending, CIUnknown:
		return false
	}
	if r.Review == ReviewChangesRequest || r.UnresolvedComments {
		return false
	}
	return r.Mergeability == MergeMergeable
}

// MergeReadinessOf projects stored PR facts into the shared readiness rule.
// hasUnresolvedComments comes from the pr_comment rows AO keeps for the PR,
// which only ever hold unresolved human threads.
func MergeReadinessOf(pr PullRequest, hasUnresolvedComments bool) MergeReadiness {
	return MergeReadiness{
		Draft:              pr.Draft,
		Merged:             pr.Merged,
		Closed:             pr.Closed,
		CI:                 pr.CI,
		Review:             pr.Review,
		Mergeability:       pr.Mergeability,
		UnresolvedComments: hasUnresolvedComments,
	}
}
