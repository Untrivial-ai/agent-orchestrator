package domain

import (
	"encoding/json"
	"time"
)

type SCMEffects struct {
	CIFailureStarted  bool
	CIFailureResolved bool
	FeedbackQueued    bool
}

type CIFeedback struct {
	ID             string
	ApplicationKey string
	OrgID          string
	SessionID      string
	PullRequestID  string
	Payload        json.RawMessage
	AttemptCount   int
	WorkerID       string
	WorkerEpoch    int64
	LeaseOwner     string
	LeaseUntil     *time.Time
}
