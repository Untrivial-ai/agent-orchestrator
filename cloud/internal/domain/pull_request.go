package domain

import (
	"encoding/json"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

// PullRequest is a durable pull request raised or tracked by AO Cloud.
type PullRequest struct {
	ID                 string
	OrgID              string
	SessionID          string
	Provider           string
	Repository         string
	Author             string
	AuthorAvatarURL    string
	Number             int
	URL                string
	Title              string
	State              contract.PRState
	Draft              bool
	HeadSHA            string
	BaseSHA            string
	MergeCommitSHA     string
	SourceBranch       string
	TargetBranch       string
	Additions          int
	Deletions          int
	ChangedFiles       int
	CIState            contract.CIState
	ReviewState        contract.ReviewDecision
	Mergeability       contract.Mergeability
	Checks             json.RawMessage
	ClaimedBySessionID *string
	ClaimedAt          *time.Time
	ReleasedAt         *time.Time
	AOReviewState      contract.AOReviewState
	ReviewPartial      bool
	CreatedAtProvider  *time.Time
	UpdatedAtProvider  *time.Time
	MergedAtProvider   *time.Time
	ClosedAtProvider   *time.Time
	ObservedAt         time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// RaisePullRequest is the input to open a new pull request for a session.
type RaisePullRequest struct {
	Title      string
	Body       string
	HeadBranch string
	BaseBranch string
}

// PullRequestRef identifies a pull request for background status polling.
type PullRequestRef struct {
	ID         string
	OrgID      string
	Provider   string
	Repository string
	Number     int
}

// PullRequestObservation is a freshly fetched lifecycle and status snapshot.
type PullRequestObservation struct {
	State        contract.PRState
	Draft        bool
	HeadSHA      string
	Additions    int
	Deletions    int
	ChangedFiles int
	CIState      contract.CIState
	ReviewState  contract.ReviewDecision
	Mergeability contract.Mergeability
	Checks       json.RawMessage
}

// PullRequestCheck is one normalized check run or legacy commit status.
type PullRequestCheck struct {
	ProviderID string          `json:"providerId,omitempty"`
	Name       string          `json:"name"`
	Status     string          `json:"status"`
	Conclusion string          `json:"conclusion"`
	URL        string          `json:"url,omitempty"`
	HeadSHA    string          `json:"headSha,omitempty"`
	Raw        json.RawMessage `json:"-"`
}

// PullRequestReview is one submitted provider review summary.
type PullRequestReview struct {
	ProviderID       string                  `json:"providerId"`
	DatabaseID       int64                   `json:"databaseId,omitempty"`
	Author           string                  `json:"author"`
	State            contract.ReviewDecision `json:"state"`
	Body             string                  `json:"body,omitempty"`
	URL              string                  `json:"url,omitempty"`
	TargetSHA        string                  `json:"targetSha,omitempty"`
	IsBot            bool                    `json:"isBot"`
	AutoInjectReview bool                    `json:"autoInjectReview"`
	SubmittedAt      *time.Time              `json:"submittedAt,omitempty"`
}

// PullRequestReviewThread is one provider review thread.
type PullRequestReviewThread struct {
	ProviderID string `json:"providerId"`
	Path       string `json:"path,omitempty"`
	Line       int    `json:"line,omitempty"`
	Resolved   bool   `json:"resolved"`
	Outdated   bool   `json:"outdated"`
	IsBot      bool   `json:"isBot"`
}

// PullRequestReviewComment is one comment within a review thread.
type PullRequestReviewComment struct {
	ProviderID       string `json:"providerId"`
	DatabaseID       int64  `json:"databaseId,omitempty"`
	ThreadProviderID string `json:"threadProviderId"`
	ReviewProviderID string `json:"reviewProviderId,omitempty"`
	Author           string `json:"author"`
	Body             string `json:"body"`
	URL              string `json:"url,omitempty"`
	Path             string `json:"path,omitempty"`
	Line             int    `json:"line,omitempty"`
	Resolved         bool   `json:"resolved"`
	Outdated         bool   `json:"outdated"`
	IsBot            bool   `json:"isBot"`
	AutoInjectReview bool   `json:"autoInjectReview"`
}

// PullRequestSnapshot is the authoritative Cloud SCM read model fetched after
// a webhook invalidates a tracked PR.
type PullRequestSnapshot struct {
	Observation       PullRequestObservation
	Author            string
	AuthorAvatarURL   string
	Title             string
	URL               string
	SourceBranch      string
	TargetBranch      string
	BaseSHA           string
	MergeCommitSHA    string
	CreatedAtProvider *time.Time
	UpdatedAtProvider *time.Time
	MergedAtProvider  *time.Time
	ClosedAtProvider  *time.Time
	Checks            []PullRequestCheck
	Reviews           []PullRequestReview
	Threads           []PullRequestReviewThread
	Comments          []PullRequestReviewComment
	ReviewsPartial    bool
}

// PullRequestTransition describes one atomic authoritative refresh and the
// newly observed feedback eligible for side effects.
type PullRequestTransition struct {
	Previous    PullRequest
	Current     PullRequest
	NewReviews  []PullRequestReview
	NewComments []PullRequestReviewComment
}

// ReviewRun is one automated review of a pull request commit.
type ReviewRun struct {
	ID               string
	OrgID            string
	PullRequestID    string
	ReviewSessionID  string
	TargetSHA        string
	Status           contract.AOReviewRunStatus
	Verdict          contract.AOReviewVerdict
	Body             string
	ProviderReviewID string
	LastError        string
	CreatedAt        time.Time
	CompletedAt      *time.Time
	DeliveredAt      *time.Time
}

// SubmitReviewResult is the result reported by a review session.
type SubmitReviewResult struct {
	Verdict contract.AOReviewVerdict
	Body    string
}

// ReviewRunPullRequest joins a review run with its pull request identity.
type ReviewRunPullRequest struct {
	ReviewRun
	PullRequestProvider      string
	PullRequestRepository    string
	PullRequestNumber        int
	PullRequestURL           string
	PullRequestTitle         string
	PullRequestAOReviewState contract.AOReviewState
}
