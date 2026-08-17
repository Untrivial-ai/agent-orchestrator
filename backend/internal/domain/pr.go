package domain

import (
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

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
	URL string
	// URLAlias is a transient provider-resolved URL that should point at URL.
	// It is persisted in the alias table, not in the pr row itself.
	URLAlias     string
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
	// ProviderID is immutable within a provider and host and survives repository
	// renames or transfers.
	ProviderID string

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
	AutoInjectCI     bool
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
	TargetSHA        string
	SubmittedAt      time.Time
	AutoInjectReview bool
}

// CIState is the aggregate CI status of a PR.
type CIState = contract.CIState

// CI states.
const (
	CIUnknown = contract.CIUnknown
	CIPending = contract.CIPending
	CIPassing = contract.CIPassing
	CIFailing = contract.CIFailing
)

// ReviewDecision is the aggregate human-review verdict on a PR.
type ReviewDecision = contract.ReviewDecision

// Review decisions.
const (
	ReviewNone           = contract.ReviewNone
	ReviewApproved       = contract.ReviewApproved
	ReviewChangesRequest = contract.ReviewChangesRequest
	ReviewRequired       = contract.ReviewRequired
)

// Mergeability is whether a PR can currently be merged.
type Mergeability = contract.Mergeability

// Mergeability states.
const (
	MergeUnknown     = contract.MergeUnknown
	MergeMergeable   = contract.MergeMergeable
	MergeConflicting = contract.MergeConflicting
	MergeBlocked     = contract.MergeBlocked
	MergeUnstable    = contract.MergeUnstable
)

// PRState is the normalized lifecycle of one tracked pull request as stored in
// the pr table.
type PRState = contract.PRState

// PR states.
const (
	PRStateDraft  = contract.PRStateDraft
	PRStateOpen   = contract.PRStateOpen
	PRStateMerged = contract.PRStateMerged
	PRStateClosed = contract.PRStateClosed
)

// PRCheckStatus is one CI check run's normalized status.
type PRCheckStatus = contract.PRCheckStatus

// PR check statuses.
const (
	PRCheckUnknown    = contract.PRCheckUnknown
	PRCheckQueued     = contract.PRCheckQueued
	PRCheckInProgress = contract.PRCheckInProgress
	PRCheckPassed     = contract.PRCheckPassed
	PRCheckFailed     = contract.PRCheckFailed
	PRCheckSkipped    = contract.PRCheckSkipped
	PRCheckCancelled  = contract.PRCheckCancelled
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
