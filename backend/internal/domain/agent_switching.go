package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// AgentNativeSessionID is AO's stable reference to one provider-owned
// conversation. It is deliberately distinct from the provider's native ID.
type AgentNativeSessionID string

// AgentSwitchID identifies one durable agent-switch saga.
type AgentSwitchID string

// AgentSwitchRequestFingerprint is the immutable, versioned identity of the
// user-visible request bound to an idempotency key. Runtime observations such
// as the current source generation are deliberately excluded: a retry after
// partial progress must still resolve to the original saga.
type AgentSwitchRequestFingerprint string

const agentSwitchRequestFingerprintPrefix = "v1:"

// ComputeAgentSwitchRequestFingerprint deterministically binds an idempotency
// key to the stable request tuple. The retired note slot remains empty in the
// encoded payload so retries of existing note-free requests keep matching.
func ComputeAgentSwitchRequestFingerprint(sessionID SessionID, targetHarness AgentHarness, model string) AgentSwitchRequestFingerprint {
	payload, _ := json.Marshal(struct {
		SessionID     SessionID    `json:"sessionId"`
		TargetHarness AgentHarness `json:"targetHarness"`
		Note          string       `json:"note"`
		Model         string       `json:"model"`
	}{
		SessionID:     sessionID,
		TargetHarness: targetHarness,
		Note:          "",
		Model:         strings.TrimSpace(model),
	})
	sum := sha256.Sum256(payload)
	return AgentSwitchRequestFingerprint(agentSwitchRequestFingerprintPrefix + hex.EncodeToString(sum[:]))
}

// MatchesRequest preserves idempotent note-free retries created before model
// selection became part of the switch request.
func (f AgentSwitchRequestFingerprint) MatchesRequest(sessionID SessionID, targetHarness AgentHarness, model string) bool {
	if f == ComputeAgentSwitchRequestFingerprint(sessionID, targetHarness, model) {
		return true
	}
	if strings.TrimSpace(model) != "" {
		return false
	}
	payload, _ := json.Marshal(struct {
		SessionID     SessionID    `json:"sessionId"`
		TargetHarness AgentHarness `json:"targetHarness"`
		Note          string       `json:"note"`
	}{
		SessionID:     sessionID,
		TargetHarness: targetHarness,
		Note:          "",
	})
	sum := sha256.Sum256(payload)
	return f == AgentSwitchRequestFingerprint(agentSwitchRequestFingerprintPrefix+hex.EncodeToString(sum[:]))
}

// Valid reports whether a persisted fingerprint uses AO's current canonical
// encoding. Lowercase hex is required so one digest has one representation.
func (f AgentSwitchRequestFingerprint) Valid() bool {
	value := string(f)
	if len(value) != len(agentSwitchRequestFingerprintPrefix)+sha256.Size*2 ||
		!strings.HasPrefix(value, agentSwitchRequestFingerprintPrefix) {
		return false
	}
	digest := strings.TrimPrefix(value, agentSwitchRequestFingerprintPrefix)
	if digest != strings.ToLower(digest) {
		return false
	}
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(decoded) == sha256.Size
}

// AgentGenerationID identifies one concrete provider invocation. A retained
// native conversation can be used by several generations over its lifetime.
type AgentGenerationID string

// AgentNativeSession is AO's provider-neutral registry entry for one retained
// provider conversation. Multiple records with the same AO session and
// harness are allowed: failed resumes and fresh fallbacks must not overwrite a
// previously retained conversation.
type AgentNativeSession struct {
	ID          AgentNativeSessionID `json:"id"`
	AOSessionID SessionID            `json:"aoSessionId"`
	Harness     AgentHarness         `json:"harness"`
	// ConfigDir is the provider configuration home used for this conversation.
	// Providers with AO-isolated homes need it to probe or resume the correct
	// native session without consulting ambient process configuration.
	ConfigDir        string            `json:"configDir,omitempty"`
	NativeSessionID  string            `json:"nativeSessionId,omitempty"`
	TranscriptPath   string            `json:"transcriptPath,omitempty"`
	LastGenerationID AgentGenerationID `json:"lastGenerationId,omitempty"`
	CreatedAt        time.Time         `json:"createdAt"`
	LastUsedAt       time.Time         `json:"lastUsedAt"`
}

// AgentSwitchState is the durable progress of a switch saga. It is an
// operational fact, not a derived display status.
type AgentSwitchState string

const (
	// AgentSwitchPreparingHandoff is the initial durable saga state while
	// deterministic context is assembled.
	AgentSwitchPreparingHandoff AgentSwitchState = "preparing_handoff"
	// AgentSwitchStoppingSource means source teardown has begun.
	AgentSwitchStoppingSource AgentSwitchState = "stopping_source"
	// AgentSwitchSourceStopped means source absence is durably confirmed.
	AgentSwitchSourceStopped AgentSwitchState = "source_stopped"
	// AgentSwitchStartingTarget means target creation may have begun.
	AgentSwitchStartingTarget AgentSwitchState = "starting_target"
	// AgentSwitchTargetReady means exact target ownership is durable.
	AgentSwitchTargetReady AgentSwitchState = "target_ready"
	// AgentSwitchDelivering means continuation delivery is in flight or ambiguous.
	AgentSwitchDelivering AgentSwitchState = "delivering_context"
	// AgentSwitchCompleted means the exact target acknowledged the continuation.
	AgentSwitchCompleted AgentSwitchState = "completed"
	// AgentSwitchFailed means the saga ended without confirmed delivery.
	AgentSwitchFailed AgentSwitchState = "failed"
)

// Valid reports whether a switch state is persistable.
func (s AgentSwitchState) Valid() bool {
	switch s {
	case AgentSwitchPreparingHandoff, AgentSwitchStoppingSource,
		AgentSwitchSourceStopped, AgentSwitchStartingTarget,
		AgentSwitchTargetReady, AgentSwitchDelivering, AgentSwitchCompleted,
		AgentSwitchFailed:
		return true
	default:
		return false
	}
}

// Terminal reports whether no more switch progress is expected.
func (s AgentSwitchState) Terminal() bool {
	return s == AgentSwitchCompleted || s == AgentSwitchFailed
}

// ValidAgentSwitchTransition is the persistence-level state-machine contract.
// Keeping it in domain prevents alternate store callers from bypassing the
// orchestration manager's ordering guarantees.
func ValidAgentSwitchTransition(from, to AgentSwitchState) bool {
	if !from.Valid() || !to.Valid() || from.Terminal() {
		return false
	}
	if from == to {
		return true // metadata amendment within one durable phase
	}
	if to == AgentSwitchFailed {
		return true
	}
	switch from {
	case AgentSwitchPreparingHandoff:
		return to == AgentSwitchStoppingSource
	case AgentSwitchStoppingSource:
		return to == AgentSwitchSourceStopped
	case AgentSwitchSourceStopped:
		return to == AgentSwitchStartingTarget
	case AgentSwitchStartingTarget:
		return to == AgentSwitchTargetReady
	case AgentSwitchTargetReady:
		return to == AgentSwitchDelivering
	case AgentSwitchDelivering:
		return to == AgentSwitchCompleted
	default:
		return false
	}
}

// AgentSwitchTargetStartMode records whether AO launched a new provider
// conversation or resumed a retained one. The empty value means that target
// selection has not completed yet.
type AgentSwitchTargetStartMode string

const (
	// AgentSwitchTargetStartPending means target selection has not completed.
	AgentSwitchTargetStartPending AgentSwitchTargetStartMode = ""
	// AgentSwitchTargetStartFresh means AO created a new native conversation.
	AgentSwitchTargetStartFresh AgentSwitchTargetStartMode = "fresh"
	// AgentSwitchTargetStartResumed means AO resumed a retained native conversation.
	AgentSwitchTargetStartResumed AgentSwitchTargetStartMode = "resumed"
)

// Valid reports whether a target-start mode is persistable.
func (m AgentSwitchTargetStartMode) Valid() bool {
	return m == AgentSwitchTargetStartPending || m == AgentSwitchTargetStartFresh || m == AgentSwitchTargetStartResumed
}

// AgentHandoffStatus records the outcome of optional source-agent semantic
// enrichment. Deterministic context exists independently of this status.
type AgentHandoffStatus string

const (
	// AgentHandoffNotAttempted means semantic enrichment has not been requested.
	AgentHandoffNotAttempted AgentHandoffStatus = "not_attempted"
	// AgentHandoffRequested means AO asked the exact source generation for context.
	AgentHandoffRequested AgentHandoffStatus = "requested"
	// AgentHandoffReceived means a generation-fenced JSON handoff was accepted.
	AgentHandoffReceived AgentHandoffStatus = "received"
	// AgentHandoffUnavailable means source enrichment could not be requested safely.
	AgentHandoffUnavailable AgentHandoffStatus = "unavailable"
	// AgentHandoffTimedOut means the bounded enrichment window expired.
	AgentHandoffTimedOut AgentHandoffStatus = "timed_out"
	// AgentHandoffFailed means semantic enrichment failed explicitly.
	AgentHandoffFailed AgentHandoffStatus = "failed"
	// AgentHandoffRejected means submitted semantic enrichment was invalid.
	AgentHandoffRejected AgentHandoffStatus = "rejected"
)

// Valid reports whether an agent-handoff status is persistable.
func (s AgentHandoffStatus) Valid() bool {
	switch s {
	case AgentHandoffNotAttempted, AgentHandoffRequested, AgentHandoffReceived,
		AgentHandoffUnavailable, AgentHandoffTimedOut, AgentHandoffFailed,
		AgentHandoffRejected:
		return true
	default:
		return false
	}
}

// AgentSwitchSourceTranscriptStatus records whether AO could safely retain a
// provider-owned transcript reference (and, when needed, read its bounded
// tail) for the finalized switch context. It deliberately carries no path or
// raw filesystem error.
type AgentSwitchSourceTranscriptStatus string

const (
	// AgentSwitchSourceTranscriptNotAttempted covers an in-flight or legacy
	// switch whose final source boundary has not been captured yet.
	AgentSwitchSourceTranscriptNotAttempted AgentSwitchSourceTranscriptStatus = "not_attempted"
	// AgentSwitchSourceTranscriptAvailable means the provider transcript was
	// located and, when fallback context was needed, read successfully.
	AgentSwitchSourceTranscriptAvailable AgentSwitchSourceTranscriptStatus = "available"
	// AgentSwitchSourceTranscriptUnavailable means AO safely continued without
	// the provider transcript, using semantic, terminal, or deterministic facts.
	AgentSwitchSourceTranscriptUnavailable AgentSwitchSourceTranscriptStatus = "unavailable"
)

// Valid reports whether the status belongs to the persisted vocabulary. The
// empty value is accepted for old in-memory fixtures and normalized to
// not_attempted at the SQLite boundary.
func (s AgentSwitchSourceTranscriptStatus) Valid() bool {
	switch s {
	case "", AgentSwitchSourceTranscriptNotAttempted,
		AgentSwitchSourceTranscriptAvailable, AgentSwitchSourceTranscriptUnavailable:
		return true
	default:
		return false
	}
}

// Captured reports whether AO has completed transcript observation at the
// final source boundary.
func (s AgentSwitchSourceTranscriptStatus) Captured() bool {
	return s == AgentSwitchSourceTranscriptAvailable || s == AgentSwitchSourceTranscriptUnavailable
}

// AgentSwitchErrorCode is the stable, persisted reason a switch saga failed or
// requires recovery. Keep this vocabulary typed so recovery and API
// presentation cannot silently introduce incompatible spellings.
type AgentSwitchErrorCode string

// Agent switch error codes.
const (
	AgentSwitchErrorDaemonRestartPreStop             AgentSwitchErrorCode = "daemon_restart_pre_stop"
	AgentSwitchErrorDaemonRestartPostStop            AgentSwitchErrorCode = "daemon_restart_post_stop"
	AgentSwitchErrorDaemonRestartUnrecoverableTarget AgentSwitchErrorCode = "daemon_restart_unrecoverable_target"
	AgentSwitchErrorDaemonRestartBeforeDelivery      AgentSwitchErrorCode = "daemon_restart_before_delivery"
	AgentSwitchErrorDeliveryUnconfirmed              AgentSwitchErrorCode = "delivery_unconfirmed"
	AgentSwitchErrorSourceSessionTerminated          AgentSwitchErrorCode = "source_session_terminated"
	AgentSwitchErrorSourceStopUnconfirmed            AgentSwitchErrorCode = "source_stop_unconfirmed"
	AgentSwitchErrorTargetBinaryMissing              AgentSwitchErrorCode = "target_binary_missing"
	AgentSwitchErrorTargetAgentUnauthorized          AgentSwitchErrorCode = "target_agent_unauthorized"
	AgentSwitchErrorTargetStartUnconfirmed           AgentSwitchErrorCode = "target_start_unconfirmed"
	AgentSwitchErrorSourceRestoreUnconfirmed         AgentSwitchErrorCode = "source_restore_unconfirmed"
	AgentSwitchErrorRequestCancelled                 AgentSwitchErrorCode = "request_cancelled"
	AgentSwitchErrorSourceBlocked                    AgentSwitchErrorCode = "source_blocked"
	AgentSwitchErrorFailedPreStop                    AgentSwitchErrorCode = "failed_pre_stop"
	AgentSwitchErrorFailedPostStop                   AgentSwitchErrorCode = "failed_post_stop"
	AgentSwitchErrorTargetReadyFailed                AgentSwitchErrorCode = "target_ready_failed"
	AgentSwitchErrorDeliveryFailed                   AgentSwitchErrorCode = "delivery_failed"
	AgentSwitchErrorSwitchFailed                     AgentSwitchErrorCode = "switch_failed"
)

// Valid reports whether the code belongs to the persisted switch vocabulary.
func (c AgentSwitchErrorCode) Valid() bool {
	switch c {
	case "", AgentSwitchErrorDaemonRestartPreStop, AgentSwitchErrorDaemonRestartPostStop,
		AgentSwitchErrorDaemonRestartUnrecoverableTarget, AgentSwitchErrorDaemonRestartBeforeDelivery,
		AgentSwitchErrorDeliveryUnconfirmed, AgentSwitchErrorSourceSessionTerminated,
		AgentSwitchErrorSourceStopUnconfirmed, AgentSwitchErrorTargetBinaryMissing,
		AgentSwitchErrorTargetAgentUnauthorized, AgentSwitchErrorTargetStartUnconfirmed,
		AgentSwitchErrorSourceRestoreUnconfirmed,
		AgentSwitchErrorRequestCancelled,
		AgentSwitchErrorSourceBlocked, AgentSwitchErrorFailedPreStop, AgentSwitchErrorFailedPostStop,
		AgentSwitchErrorTargetReadyFailed, AgentSwitchErrorDeliveryFailed, AgentSwitchErrorSwitchFailed:
		return true
	default:
		return false
	}
}

// AgentSwitch is one durable switch saga. The optional source-authored handoff
// is tracked independently from the AO-finalized handoff that was actually
// delivered to the target. The finalized artifact also exists for fallback-
// only switches and is the durable source used to rebuild hidden continuation
// instructions when the target native session is restored.
type AgentSwitch struct {
	ID                      AgentSwitchID                     `json:"id"`
	SessionID               SessionID                         `json:"sessionId"`
	IdempotencyKey          string                            `json:"idempotencyKey"`
	RequestFingerprint      AgentSwitchRequestFingerprint     `json:"-"`
	FromHarness             AgentHarness                      `json:"fromHarness"`
	TargetHarness           AgentHarness                      `json:"targetHarness"`
	TargetNativeSessionRef  *AgentNativeSessionID             `json:"targetNativeSessionRef,omitempty"`
	TargetStartMode         AgentSwitchTargetStartMode        `json:"targetStartMode,omitempty"`
	State                   AgentSwitchState                  `json:"state"`
	AgentHandoffStatus      AgentHandoffStatus                `json:"agentHandoffStatus"`
	SemanticHandoffIncluded bool                              `json:"semanticHandoffIncluded"`
	SourceTranscriptStatus  AgentSwitchSourceTranscriptStatus `json:"sourceTranscriptStatus,omitempty"`
	AgentHandoffPath        string                            `json:"agentHandoffPath,omitempty"`
	AgentHandoffHash        string                            `json:"agentHandoffHash,omitempty"`
	FinalHandoffPath        string                            `json:"-"`
	FinalHandoffHash        string                            `json:"-"`
	SourceGenerationID      AgentGenerationID                 `json:"sourceGenerationId"`
	TargetGenerationID      AgentGenerationID                 `json:"targetGenerationId,omitempty"`
	TargetRuntimeHandleID   string                            `json:"-"`
	TargetAcknowledgedAt    *time.Time                        `json:"targetAcknowledgedAt,omitempty"`
	ErrorCode               AgentSwitchErrorCode              `json:"errorCode,omitempty"`
	RequestedAt             time.Time                         `json:"requestedAt"`
	UpdatedAt               time.Time                         `json:"updatedAt"`
}

// RequiresRecovery reports whether a nonterminal switch needs an explicit,
// ownership-safe recovery action before terminal input can reopen.
func (s AgentSwitch) RequiresRecovery() bool {
	return s.RequiresTargetStartRecovery() || s.RequiresSourceRecovery()
}

// RequiresTargetStartRecovery reports the ambiguous target-start boundary.
func (s AgentSwitch) RequiresTargetStartRecovery() bool {
	return s.State == AgentSwitchStartingTarget &&
		s.TargetRuntimeHandleID == "" &&
		s.ErrorCode == AgentSwitchErrorTargetStartUnconfirmed
}

// RequiresSourceRecovery reports a retained source-side boundary that can be
// reconciled without starting the target agent.
func (s AgentSwitch) RequiresSourceRecovery() bool {
	return s.RequiresSourceStopRecovery() || s.RequiresSourceRestore()
}

// RequiresSourceStopRecovery reports that source teardown started, but AO
// could not prove whether the source runtime stopped.
func (s AgentSwitch) RequiresSourceStopRecovery() bool {
	return s.State == AgentSwitchStoppingSource &&
		s.ErrorCode == AgentSwitchErrorSourceStopUnconfirmed
}

// RequiresSourceRestore reports that no target owns the session, but AO could
// not relaunch the conclusively stopped source. Retrying this compensation is
// safe because durable ownership still points at the source provider.
func (s AgentSwitch) RequiresSourceRestore() bool {
	return (s.State == AgentSwitchSourceStopped || s.State == AgentSwitchStartingTarget) &&
		s.ErrorCode == AgentSwitchErrorSourceRestoreUnconfirmed
}

// AgentSwitchTargetActivation is the narrow command that transfers durable
// session ownership to a prepared target generation. SourceGenerationID fences
// the immutable switch provenance; ExpectedSourceRuntimeLaunchID independently
// fences the current sessions row (and may be empty for a pre-generation legacy
// session). Native identity and transcript facts are loaded from the referenced
// registry row rather than trusted from a caller.
type AgentSwitchTargetActivation struct {
	SwitchID                      AgentSwitchID
	SessionID                     SessionID
	SourceHarness                 AgentHarness
	SourceGenerationID            AgentGenerationID
	ExpectedSourceRuntimeLaunchID string
	TargetHarness                 AgentHarness
	TargetNativeSessionRef        AgentNativeSessionID
	TargetGenerationID            AgentGenerationID
	RuntimeHandleID               string
	ActivatedAt                   time.Time
}

// AgentSwitchSourceStopConfirmation records the conclusive source-process
// boundary without transferring ownership. The sessions row remains bound to
// SourceHarness and ExpectedSourceRuntimeLaunchID, but its activity becomes
// exited in the same transaction that advances the switch saga.
type AgentSwitchSourceStopConfirmation struct {
	SwitchID                      AgentSwitchID
	SessionID                     SessionID
	SourceHarness                 AgentHarness
	SourceGenerationID            AgentGenerationID
	ExpectedSourceRuntimeLaunchID string
	TargetGenerationID            AgentGenerationID
	StoppedAt                     time.Time
}

var (
	// ErrAgentNativeSessionConflict means an idempotent create collided with a
	// record representing a different provider conversation.
	ErrAgentNativeSessionConflict = errors.New("domain: agent native session identity conflict")
	// ErrAgentSwitchIdempotencyConflict means one idempotency key was reused for
	// a materially different switch request.
	ErrAgentSwitchIdempotencyConflict = errors.New("domain: agent switch idempotency conflict")
	// ErrAgentSwitchInProgress means the session already has a non-terminal
	// durable switch saga. The active record can be retrieved for recovery.
	ErrAgentSwitchInProgress = errors.New("domain: agent switch already in progress")
)
