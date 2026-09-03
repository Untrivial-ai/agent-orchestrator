package domain

import (
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

type PushApprovalStatus string

const (
	PushApprovalPending  PushApprovalStatus = "PENDING"
	PushApprovalApproved PushApprovalStatus = "APPROVED"
	PushApprovalConsumed PushApprovalStatus = "CONSUMED"
	PushApprovalExpired  PushApprovalStatus = "EXPIRED"
	PushApprovalRevoked  PushApprovalStatus = "REVOKED"
	PushApprovalFailed   PushApprovalStatus = "FAILED"
)

type PushApproval struct {
	ID              string
	ProjectID       ProjectID
	SessionID       SessionID
	Repository      string
	Remote          string
	RemoteURL       string
	Branch          string
	ExpectedHeadSHA string
	CreatedAt       time.Time
	ApprovedAt      *time.Time
	ApprovedBy      string
	ExpiresAt       time.Time
	ConsumedAt      *time.Time
	Status          PushApprovalStatus
	Result          string
	ErrorMessage    string
}

func (a PushApproval) ValidateForCreate() error {
	for name, value := range map[string]string{
		"id": a.ID, "project_id": string(a.ProjectID), "session_id": string(a.SessionID),
		"repository": a.Repository, "remote": a.Remote, "remote_url": a.RemoteURL,
		"branch": a.Branch, "expected_head_sha": a.ExpectedHeadSHA,
	} {
		if strings.TrimSpace(value) == "" {
			return errors.New("push approval " + name + " is required")
		}
	}
	if a.CreatedAt.IsZero() || a.ExpiresAt.IsZero() || !a.ExpiresAt.After(a.CreatedAt) {
		return errors.New("push approval requires a future expiry")
	}
	if len(a.ExpectedHeadSHA) != 40 && len(a.ExpectedHeadSHA) != 64 {
		return errors.New("push approval expected_head_sha must be a 40- or 64-character hexadecimal object ID")
	}
	if _, err := hex.DecodeString(a.ExpectedHeadSHA); err != nil {
		return errors.New("push approval expected_head_sha must be hexadecimal")
	}
	return nil
}

type GitAction string

const (
	GitActionPushRequested GitAction = "PUSH_REQUESTED"
	GitActionPushApproved  GitAction = "PUSH_APPROVED"
	GitActionPushCancelled GitAction = "PUSH_CANCELLED"
	GitActionPushRejected  GitAction = "PUSH_REJECTED"
	GitActionPushStarted   GitAction = "PUSH_STARTED"
	GitActionPushSucceeded GitAction = "PUSH_SUCCEEDED"
	GitActionPushFailed    GitAction = "PUSH_FAILED"
)

type GitActionAudit struct {
	ID           string
	ProjectID    ProjectID
	SessionID    SessionID
	Action       GitAction
	Repository   string
	Remote       string
	Branch       string
	HeadSHA      string
	ApprovalID   string
	RequestedBy  string
	ExecutedBy   string
	StartedAt    time.Time
	FinishedAt   *time.Time
	Result       string
	ErrorMessage string
}
