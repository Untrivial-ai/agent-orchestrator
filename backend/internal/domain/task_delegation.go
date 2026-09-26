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

// TaskDelegationStartupState separates durable identity from runtime readiness.
type TaskDelegationStartupState string

// Startup checkpoints prevent replay from repeating uncertain runtime effects.
const (
	TaskDelegationStartupLegacy   TaskDelegationStartupState = "legacy"
	TaskDelegationStartupSeeded   TaskDelegationStartupState = "seeded"
	TaskDelegationStartupStarting TaskDelegationStartupState = "starting"
	TaskDelegationStartupReady    TaskDelegationStartupState = "ready"
)

// Task delegation states distinguish a reserved request from one with a durable worker.
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
	StartupState       TaskDelegationStartupState
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Ready accepts legacy completed records but never unfinished runtime startup.
func (d TaskDelegation) Ready() bool {
	return d.State == TaskDelegationCompleted && d.WorkerID != "" &&
		(d.StartupState == TaskDelegationStartupReady || d.StartupState == TaskDelegationStartupLegacy)
}

var (
	// ErrTaskDelegationRecoveryRequired prevents retrying an uncertain startup.
	ErrTaskDelegationRecoveryRequired = errors.New("domain: task delegation needs recovery")
	// ErrTaskDelegationIdempotencyConflict means a key was reused for a
	// materially different request.
	ErrTaskDelegationIdempotencyConflict = errors.New("domain: task delegation idempotency conflict")
	// ErrTaskDelegationInProgress means the first attempt has not recorded a
	// final worker yet.
	ErrTaskDelegationInProgress = errors.New("domain: task delegation is in progress")
)
