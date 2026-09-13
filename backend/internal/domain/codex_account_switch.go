package domain

import "time"

// CodexAccountSwitchSourceKind describes what occupied the device credential
// store before a durable switch began.
type CodexAccountSwitchSourceKind string

const (
	// CodexAccountSwitchSourceManaged means the device credential belongs to a saved AO account.
	CodexAccountSwitchSourceManaged CodexAccountSwitchSourceKind = "managed"
	// CodexAccountSwitchSourceDevice means the device has an unmatched credential.
	CodexAccountSwitchSourceDevice CodexAccountSwitchSourceKind = "device"
	// CodexAccountSwitchSourceNone means the device has no Codex credential.
	CodexAccountSwitchSourceNone CodexAccountSwitchSourceKind = "none"
)

// CodexAccountSwitchSource is the daemon-private, reconciled source snapshot
// used to admit a switch.
type CodexAccountSwitchSource struct {
	Kind      CodexAccountSwitchSourceKind
	AccountID string
	Revision  int64
}

// CodexAccountSwitchPhase is the durable global credential-switch phase.
type CodexAccountSwitchPhase string

const (
	// CodexAccountSwitchRequested is the initial durable switch phase.
	CodexAccountSwitchRequested CodexAccountSwitchPhase = "requested"
	// CodexAccountSwitchCheckpointCredential journals the global source credential.
	CodexAccountSwitchCheckpointCredential CodexAccountSwitchPhase = "checkpointing_source"
	// CodexAccountSwitchActivatingAccount stages the selected target credential.
	CodexAccountSwitchActivatingAccount CodexAccountSwitchPhase = "activating_target"
	// CodexAccountSwitchVerifyingAccount verifies the device-global identity.
	CodexAccountSwitchVerifyingAccount CodexAccountSwitchPhase = "verifying_target"
	// CodexAccountSwitchRollbackRequired requires restoring the source credential.
	CodexAccountSwitchRollbackRequired CodexAccountSwitchPhase = "rollback_required"
	// CodexAccountSwitchRecoveryRequired requires exact recorded recovery work.
	CodexAccountSwitchRecoveryRequired CodexAccountSwitchPhase = "recovery_required"
	// CodexAccountSwitchCompleted means target activation succeeded.
	CodexAccountSwitchCompleted CodexAccountSwitchPhase = "completed"
	// CodexAccountSwitchFailed means the source remained or was restored safely.
	CodexAccountSwitchFailed CodexAccountSwitchPhase = "failed"
)

// Terminal reports whether no more switch work may run automatically.
func (p CodexAccountSwitchPhase) Terminal() bool {
	return p == CodexAccountSwitchCompleted || p == CodexAccountSwitchFailed
}

// CodexAccountSwitch is the durable global account-switch operation.
type CodexAccountSwitch struct {
	ID                     string                       `json:"id"`
	SourceKind             CodexAccountSwitchSourceKind `json:"sourceKind" enum:"managed,device,none"`
	SourceAccountID        string                       `json:"sourceAccountId,omitempty"`
	TargetAccountID        string                       `json:"targetAccountId"`
	Phase                  CodexAccountSwitchPhase      `json:"phase"`
	FailureCode            string                       `json:"failureCode,omitempty"`
	CanRecover             bool                         `json:"canRecover"`
	CredentialsCommittedAt *time.Time                   `json:"credentialsCommittedAt,omitempty"`
	CreatedAt              time.Time                    `json:"createdAt"`
	UpdatedAt              time.Time                    `json:"updatedAt"`
	CompletedAt            *time.Time                   `json:"completedAt,omitempty"`
	// Daemon-private idempotency data.
	IdempotencyKey          string `json:"-"`
	RequestFingerprint      string `json:"-"`
	ExpectedAccountRevision int64  `json:"-"`
}
