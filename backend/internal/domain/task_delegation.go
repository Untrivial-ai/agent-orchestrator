package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// TaskDelegationRequestFingerprint binds an idempotency key to one local task
// request. It excludes runtime observations so a retry still finds the first
// attempt after the daemon restarts.
type TaskDelegationRequestFingerprint string

const taskDelegationRequestFingerprintPrefix = "v1:"

// NewTaskDelegationRequestFingerprint encodes a canonical request payload.
func NewTaskDelegationRequestFingerprint(payload []byte) TaskDelegationRequestFingerprint {
	sum := sha256.Sum256(payload)
	return TaskDelegationRequestFingerprint(taskDelegationRequestFingerprintPrefix + hex.EncodeToString(sum[:]))
}

// Valid reports whether the fingerprint uses the current canonical encoding.
func (f TaskDelegationRequestFingerprint) Valid() bool {
	value := string(f)
	if len(value) != len(taskDelegationRequestFingerprintPrefix)+sha256.Size*2 ||
		!strings.HasPrefix(value, taskDelegationRequestFingerprintPrefix) {
		return false
	}
	digest := strings.TrimPrefix(value, taskDelegationRequestFingerprintPrefix)
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(decoded) == sha256.Size && digest == strings.ToLower(digest)
}

// TaskDelegationState tracks whether the reserved local request has committed
// a worker session.
type TaskDelegationState string

const (
	TaskDelegationPending   TaskDelegationState = "pending"
	TaskDelegationCompleted TaskDelegationState = "completed"
)

// TaskDelegation is the durable result associated with one local delegation
// idempotency key.
type TaskDelegation struct {
	IdempotencyKey     string
	RequestFingerprint TaskDelegationRequestFingerprint
	WorkerID           SessionID
	State              TaskDelegationState
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

var (
	// ErrTaskDelegationIdempotencyConflict means a key was reused for a
	// materially different request.
	ErrTaskDelegationIdempotencyConflict = errors.New("domain: task delegation idempotency conflict")
	// ErrTaskDelegationInProgress means the first attempt has not recorded a
	// final worker yet.
	ErrTaskDelegationInProgress = errors.New("domain: task delegation is in progress")
)
