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
	Number             int
	URL                string
	Title              string
	State              contract.PRState
	Draft              bool
	HeadSHA            string
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
