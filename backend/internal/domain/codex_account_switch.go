package domain

import "time"

// CodexAccountSwitchPhase is the durable global credential-switch phase.
type CodexAccountSwitchPhase string

const (
	// CodexAccountSwitchRequested is the initial durable switch phase.
	CodexAccountSwitchRequested CodexAccountSwitchPhase = "requested"
	// CodexAccountSwitchStoppingSessions stops exact controller generations.
	CodexAccountSwitchStoppingSessions CodexAccountSwitchPhase = "stopping_sessions"
	// CodexAccountSwitchSessionsStopped means no affected Codex writer remains.
	CodexAccountSwitchSessionsStopped CodexAccountSwitchPhase = "sessions_stopped"
	// CodexAccountSwitchCheckpointCredential journals the global source credential.
	CodexAccountSwitchCheckpointCredential CodexAccountSwitchPhase = "checkpointing_source"
	// CodexAccountSwitchActivatingAccount stages the selected target credential.
	CodexAccountSwitchActivatingAccount CodexAccountSwitchPhase = "activating_target"
	// CodexAccountSwitchVerifyingAccount verifies the device-global identity.
	CodexAccountSwitchVerifyingAccount CodexAccountSwitchPhase = "verifying_target"
	// CodexAccountSwitchRestartingSessions resumes previously running controllers.
	CodexAccountSwitchRestartingSessions CodexAccountSwitchPhase = "restarting_sessions"
	// CodexAccountSwitchRollbackRequired requires restoring the source credential.
	CodexAccountSwitchRollbackRequired CodexAccountSwitchPhase = "rollback_required"
	// CodexAccountSwitchRecoveryRequired requires exact recorded recovery work.
	CodexAccountSwitchRecoveryRequired CodexAccountSwitchPhase = "recovery_required"
	// CodexAccountSwitchCompleted means activation and restarts succeeded.
	CodexAccountSwitchCompleted CodexAccountSwitchPhase = "completed"
	// CodexAccountSwitchFailed means the source remained or was restored safely.
	CodexAccountSwitchFailed CodexAccountSwitchPhase = "failed"
)

// Terminal reports whether no more switch work may run automatically.
func (p CodexAccountSwitchPhase) Terminal() bool {
	return p == CodexAccountSwitchCompleted || p == CodexAccountSwitchFailed
}

// CodexAccountSwitchSession records safe restart progress for one AO session.
type CodexAccountSwitchSession struct {
	SessionID     SessionID   `json:"sessionId"`
	InterfaceMode SessionMode `json:"interfaceMode" enum:"tui,chat"`
	WasRunning    bool        `json:"wasRunning"`
	StopState     string      `json:"stopState"`
	RestartState  string      `json:"restartState"`
	ErrorCode     string      `json:"errorCode,omitempty"`
	StoppedAt     *time.Time  `json:"stoppedAt,omitempty"`
	RestartedAt   *time.Time  `json:"restartedAt,omitempty"`
	// Daemon-private fencing and resume identity.
	NativeSessionID         string `json:"-"`
	SourceHandleID          string `json:"-"`
	SourceGeneration        string `json:"-"`
	ReviewerWasRunning      bool   `json:"-"`
	ReviewerSourceHandleID  string `json:"-"`
	ReviewerNativeSessionID string `json:"-"`
	ReviewerStopState       string `json:"-"`
	ReviewerRestartState    string `json:"-"`
}

// CodexAccountSwitch is the durable global account-switch operation.
type CodexAccountSwitch struct {
	ID                     string                      `json:"id"`
	SourceAccountID        string                      `json:"sourceAccountId"`
	TargetAccountID        string                      `json:"targetAccountId"`
	Phase                  CodexAccountSwitchPhase     `json:"phase"`
	FailureCode            string                      `json:"failureCode,omitempty"`
	Sessions               []CodexAccountSwitchSession `json:"sessions"`
	CanRecover             bool                        `json:"canRecover"`
	CredentialsCommittedAt *time.Time                  `json:"credentialsCommittedAt,omitempty"`
	CreatedAt              time.Time                   `json:"createdAt"`
	UpdatedAt              time.Time                   `json:"updatedAt"`
	CompletedAt            *time.Time                  `json:"completedAt,omitempty"`
	// Daemon-private idempotency data.
	IdempotencyKey          string `json:"-"`
	RequestFingerprint      string `json:"-"`
	ExpectedAccountRevision int64  `json:"-"`
}
