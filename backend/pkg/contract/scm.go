package contract

import "time"

// PRState is the normalized lifecycle of a pull request.
type PRState string

// Pull-request lifecycle states.
const (
	PRStateDraft  PRState = "draft"
	PRStateOpen   PRState = "open"
	PRStateMerged PRState = "merged"
	PRStateClosed PRState = "closed"
)

// PRCheckStatus is one CI check run's normalized status.
type PRCheckStatus string

// Pull-request check states.
const (
	PRCheckUnknown    PRCheckStatus = "unknown"
	PRCheckQueued     PRCheckStatus = "queued"
	PRCheckInProgress PRCheckStatus = "in_progress"
	PRCheckPassed     PRCheckStatus = "passed"
	PRCheckFailed     PRCheckStatus = "failed"
	PRCheckSkipped    PRCheckStatus = "skipped"
	PRCheckCancelled  PRCheckStatus = "cancelled"
)

// AOReviewRunStatus is the lifecycle state of one AO review pass.
type AOReviewRunStatus string

// AO review-run states.
const (
	AOReviewRunRunning   AOReviewRunStatus = "running"
	AOReviewRunComplete  AOReviewRunStatus = "complete"
	AOReviewRunDelivered AOReviewRunStatus = "delivered"
	AOReviewRunFailed    AOReviewRunStatus = "failed"
	AOReviewRunCancelled AOReviewRunStatus = "cancelled"
)

// AOReviewVerdict is the outcome of an AO review pass. An empty verdict means
// the pass has not produced an outcome.
type AOReviewVerdict string

// AO review verdicts.
const (
	AOReviewVerdictNone             AOReviewVerdict = ""
	AOReviewVerdictApproved         AOReviewVerdict = "approved"
	AOReviewVerdictChangesRequested AOReviewVerdict = "changes_requested"
)

// Valid reports whether v is a verdict a reviewer may submit.
func (v AOReviewVerdict) Valid() bool {
	return v == AOReviewVerdictApproved || v == AOReviewVerdictChangesRequested
}

// AOReviewState is the current AO review state for one pull request head.
type AOReviewState string

// AO review states for the current pull-request head.
const (
	AOReviewNeedsReview      AOReviewState = "needs_review"
	AOReviewRunning          AOReviewState = "running"
	AOReviewUpToDate         AOReviewState = "up_to_date"
	AOReviewChangesRequested AOReviewState = "changes_requested"
	AOReviewIneligible       AOReviewState = "ineligible"
)

// PullRequestFailingCheck is one failed or cancelled CI check.
type PullRequestFailingCheck struct {
	Name       string        `json:"name"`
	Status     PRCheckStatus `json:"status"`
	Conclusion string        `json:"conclusion"`
	URL        string        `json:"url,omitempty"`
}

// PullRequestCISummary is the latest aggregate CI observation.
type PullRequestCISummary struct {
	State         CIState                   `json:"state"`
	FailingChecks []PullRequestFailingCheck `json:"failingChecks"`
	AutoInjectCI  bool                      `json:"autoInjectCI"`
}

// PullRequestReviewCommentLink points to one review comment.
type PullRequestReviewCommentLink struct {
	URL              string `json:"url,omitempty"`
	ReviewID         string `json:"reviewId,omitempty"`
	File             string `json:"file,omitempty"`
	Line             int    `json:"line,omitempty"`
	Body             string `json:"body,omitempty"`
	AutoInjectReview bool   `json:"autoInjectReview"`
}

// PullRequestUnresolvedReviewer groups review comments by reviewer.
type PullRequestUnresolvedReviewer struct {
	ReviewerID string                         `json:"reviewerId"`
	Count      int                            `json:"count"`
	Links      []PullRequestReviewCommentLink `json:"links"`
	ReviewURL  string                         `json:"reviewUrl,omitempty"`
	IsBot      bool                           `json:"isBot,omitempty"`
}

// PullRequestSubmittedReview is one provider review summary.
type PullRequestSubmittedReview struct {
	Reviewer         string         `json:"reviewerId"`
	Verdict          ReviewDecision `json:"verdict"`
	Body             string         `json:"body,omitempty"`
	URL              string         `json:"reviewUrl,omitempty"`
	SubmittedAt      time.Time      `json:"submittedAt"`
	IsBot            bool           `json:"isBot,omitempty"`
	AutoInjectReview bool           `json:"autoInjectReview"`
}

// PullRequestReviewSummary is the latest aggregate provider review observation.
type PullRequestReviewSummary struct {
	Decision                   ReviewDecision                  `json:"decision"`
	HasUnresolvedHumanComments bool                            `json:"hasUnresolvedHumanComments"`
	UnresolvedBy               []PullRequestUnresolvedReviewer `json:"unresolvedBy"`
	ResolvedBy                 []PullRequestUnresolvedReviewer `json:"resolvedBy,omitempty"`
	Reviews                    []PullRequestSubmittedReview    `json:"reviews"`
}

// PullRequestConflictFile is one file involved in a merge conflict.
type PullRequestConflictFile struct {
	Path string `json:"path"`
	URL  string `json:"url,omitempty"`
}

// PullRequestMergeabilitySummary reports whether a PR can merge and why.
type PullRequestMergeabilitySummary struct {
	State         Mergeability              `json:"state"`
	Reasons       []string                  `json:"reasons"`
	PRURL         string                    `json:"pullRequestUrl"`
	ConflictFiles []PullRequestConflictFile `json:"conflictFiles"`
}

// PullRequestSummary is the normalized raw SCM read model for one pull request.
type PullRequestSummary struct {
	URL              string                         `json:"url"`
	HTMLURL          string                         `json:"htmlUrl,omitempty"`
	Number           int                            `json:"number"`
	Title            string                         `json:"title"`
	State            PRState                        `json:"state"`
	Provider         string                         `json:"provider"`
	Repo             string                         `json:"repository"`
	Author           string                         `json:"author"`
	SourceBranch     string                         `json:"sourceBranch"`
	TargetBranch     string                         `json:"targetBranch"`
	HeadSHA          string                         `json:"headSha"`
	Additions        int                            `json:"additions"`
	Deletions        int                            `json:"deletions"`
	ChangedFiles     int                            `json:"changedFiles"`
	CI               PullRequestCISummary           `json:"ci"`
	Review           PullRequestReviewSummary       `json:"review"`
	Mergeability     PullRequestMergeabilitySummary `json:"mergeability"`
	StateChangedAt   time.Time                      `json:"stateChangedAt,omitempty"`
	CreatedAt        time.Time                      `json:"createdAt,omitempty"`
	UpdatedAt        time.Time                      `json:"updatedAt"`
	ObservedAt       time.Time                      `json:"observedAt"`
	CIObservedAt     time.Time                      `json:"ciObservedAt"`
	ReviewObservedAt time.Time                      `json:"reviewObservedAt"`
}

// AOReviewRun is one transport-neutral AO review pass.
type AOReviewRun struct {
	ID               string            `json:"id"`
	ReviewID         string            `json:"reviewId"`
	SessionID        string            `json:"sessionId"`
	BatchID          string            `json:"batchId"`
	Harness          string            `json:"harness"`
	PRURL            string            `json:"pullRequestUrl"`
	TargetSHA        string            `json:"targetSha"`
	Status           AOReviewRunStatus `json:"status"`
	Verdict          AOReviewVerdict   `json:"verdict"`
	Body             string            `json:"body"`
	ProviderReviewID string            `json:"providerReviewId"`
	CreatedAt        time.Time         `json:"createdAt"`
	DeliveredAt      *time.Time        `json:"deliveredAt,omitempty"`
	AutoInjectReview bool              `json:"autoInjectReview"`
}

// AOPullRequestReviewState is AO's current review state for one PR head.
type AOPullRequestReviewState struct {
	PRURL          string        `json:"pullRequestUrl"`
	PRNumber       int           `json:"pullRequestNumber"`
	Title          string        `json:"title"`
	TargetSHA      string        `json:"targetSha"`
	Status         AOReviewState `json:"status"`
	StaleTargetSHA string        `json:"staleTargetSha,omitempty"`
	LatestRun      *AOReviewRun  `json:"latestRun,omitempty"`
	PreviousRun    *AOReviewRun  `json:"previousRun,omitempty"`
}
