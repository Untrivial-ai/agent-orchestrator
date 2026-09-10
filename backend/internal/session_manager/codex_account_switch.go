package sessionmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var (
	// ErrCodexAccountSwitchInProgress means the daemon-wide mutation gate is held.
	ErrCodexAccountSwitchInProgress = ports.ErrCodexAccountSwitchInProgress
	// ErrCodexAccountAlreadyActive rejects selecting the current account.
	ErrCodexAccountAlreadyActive = ports.ErrCodexAccountAlreadyActive
	// ErrCodexActiveAccountUnavailable rejects switching without a reconciled source account.
	ErrCodexActiveAccountUnavailable = ports.ErrCodexActiveAccountUnavailable
	// ErrCodexAccountSwitchNotFound means the durable operation does not exist.
	ErrCodexAccountSwitchNotFound = ports.ErrCodexAccountSwitchNotFound
	// ErrCodexAccountRevisionConflict reports a stale active-account revision.
	ErrCodexAccountRevisionConflict = ports.ErrCodexAccountRevisionConflict
	// ErrCodexAccountSwitchIdempotencyConflict rejects reused mismatched keys.
	ErrCodexAccountSwitchIdempotencyConflict = ports.ErrCodexAccountSwitchIdempotencyConflict
	// ErrCodexRunningSessionNotResumable blocks switching before credential mutation.
	ErrCodexRunningSessionNotResumable = ports.ErrCodexRunningSessionNotResumable
)

const (
	codexSwitchStopIntent            = "stop_in_progress"
	codexSwitchReviewerStopIntent    = "reviewer_stop_in_progress"
	codexSwitchRestartIntentPrefix   = "restart_in_progress:"
	codexSwitchRestartUnknownPrefix  = "restart_unconfirmed:"
	codexSwitchReviewerRestartIntent = "reviewer_restart_in_progress"
)

func codexAccountSwitchFingerprint(target string, revision int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("v1\x00%s\x00%d", target, revision)))
	return "v1:" + hex.EncodeToString(sum[:])
}

func (m *Manager) codexAccountSwitchDependencies() (ports.CodexAccountCredentialManager, ports.CodexAccountSwitchStore, error) {
	credentials, ok := m.agentReadiness.(ports.CodexAccountCredentialManager)
	if !ok {
		return nil, nil, errors.New("codex account credential manager is unavailable")
	}
	store, ok := m.store.(ports.CodexAccountSwitchStore)
	if !ok {
		return nil, nil, errors.New("codex account switch store is unavailable")
	}
	return credentials, store, nil
}

func (m *Manager) acquireCodexAccountSwitchGate(ctx context.Context) error {
	lease, err := m.codexOperationGate.AcquireExclusive(ctx)
	if err != nil {
		return err
	}
	m.codexAccountSwitchMu.Lock()
	defer m.codexAccountSwitchMu.Unlock()
	if m.codexAccountSwitchWorkerRunning || m.codexAccountSwitchLease != nil {
		lease.Release()
		return ErrCodexAccountSwitchInProgress
	}
	m.codexAccountSwitchLease = lease
	m.codexAccountSwitchWorkerRunning = true
	return nil
}

// claimCodexAccountSwitchRecoveryWorker starts one recovery worker while the
// durable global mutation fence remains active. Recovery-required operations
// intentionally retain that fence between HTTP requests so no new Codex
// process can start against an ambiguous runtime credential.
func (m *Manager) claimCodexAccountSwitchRecoveryWorker(ctx context.Context) bool {
	m.codexAccountSwitchMu.Lock()
	if m.codexAccountSwitchWorkerRunning {
		m.codexAccountSwitchMu.Unlock()
		return false
	}
	if m.codexAccountSwitchLease != nil {
		m.codexAccountSwitchWorkerRunning = true
		m.codexAccountSwitchMu.Unlock()
		return true
	}
	m.codexAccountSwitchMu.Unlock()
	lease, err := m.codexOperationGate.AcquireExclusive(ctx)
	if err != nil {
		return false
	}
	m.codexAccountSwitchMu.Lock()
	defer m.codexAccountSwitchMu.Unlock()
	if m.codexAccountSwitchWorkerRunning || m.codexAccountSwitchLease != nil {
		lease.Release()
		return false
	}
	m.codexAccountSwitchLease = lease
	m.codexAccountSwitchWorkerRunning = true
	return true
}

func (m *Manager) finishCodexAccountSwitchWorker(keepFence bool) {
	m.codexAccountSwitchMu.Lock()
	m.codexAccountSwitchWorkerRunning = false
	var release ports.CodexOperationLease
	if !keepFence {
		release = m.codexAccountSwitchLease
		m.codexAccountSwitchLease = nil
	}
	m.codexAccountSwitchMu.Unlock()
	if release != nil {
		release.Release()
	}
	if keepFence {
		m.publishCodexAccountSwitchChanged()
	}
}

func (m *Manager) codexAccountSwitchWorkerActive() bool {
	m.codexAccountSwitchMu.Lock()
	defer m.codexAccountSwitchMu.Unlock()
	return m.codexAccountSwitchWorkerRunning
}

func (m *Manager) finishCodexAccountSwitchMutation(credentials ports.CodexAccountCredentialManager, keepFence bool) {
	m.finishCodexAccountSwitchWorker(keepFence)
	if !keepFence {
		credentials.EndCodexAccountMutation()
	}
}

func (m *Manager) codexAccountSwitchIsActive() bool {
	return m.codexOperationGate != nil && m.codexOperationGate.ExclusivePendingOrHeld()
}

// CodexAccountSwitchInProgress is the daemon-wide admission fence consumed by
// controller owners outside Session Manager.
func (m *Manager) CodexAccountSwitchInProgress() bool { return m.codexAccountSwitchIsActive() }

// StartCodexAccountSwitch admits and starts one daemon-owned global account switch.
func (m *Manager) StartCodexAccountSwitch(ctx context.Context, cfg ports.CodexAccountSwitchConfig) (domain.CodexAccountSwitch, error) {
	cfg.TargetAccountID = strings.TrimSpace(cfg.TargetAccountID)
	cfg.IdempotencyKey = strings.TrimSpace(cfg.IdempotencyKey)
	if cfg.IdempotencyKey == "" {
		return domain.CodexAccountSwitch{}, errors.New("idempotency key is required")
	}
	credentials, store, err := m.codexAccountSwitchDependencies()
	if err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	fingerprint := codexAccountSwitchFingerprint(cfg.TargetAccountID, cfg.ExpectedAccountRevision)
	if existing, ok, readErr := store.GetCodexAccountSwitchByIdempotency(ctx, cfg.IdempotencyKey); readErr != nil {
		return domain.CodexAccountSwitch{}, readErr
	} else if ok {
		if existing.RequestFingerprint != fingerprint {
			return existing, ErrCodexAccountSwitchIdempotencyConflict
		}
		return m.loadCodexAccountSwitchSessions(ctx, store, existing)
	}
	if _, active, readErr := store.GetActiveCodexAccountSwitch(ctx); readErr != nil {
		return domain.CodexAccountSwitch{}, readErr
	} else if active {
		return domain.CodexAccountSwitch{}, ErrCodexAccountSwitchInProgress
	}
	// Bootstrap reconciliation mutates the active pointer and therefore needs
	// this same token. Complete it before admission, then perform all account
	// and target revalidation below while the token is held.
	if err := credentials.WaitCodexAccountBootstrap(ctx); err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	if err := m.acquireCodexAccountSwitchGate(ctx); err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	releaseSwitchGate := true
	defer func() {
		if releaseSwitchGate {
			m.finishCodexAccountSwitchWorker(false)
		}
	}()
	if err := credentials.BeginCodexAccountMutation(ctx); err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	releaseMutation := true
	defer func() {
		if releaseMutation {
			credentials.EndCodexAccountMutation()
		}
	}()
	current := credentials.CurrentCodexActiveAccount()
	if strings.TrimSpace(current.AccountID) == "" || current.Revision < 1 {
		return domain.CodexAccountSwitch{}, ErrCodexActiveAccountUnavailable
	}
	if current.AccountID == cfg.TargetAccountID {
		return domain.CodexAccountSwitch{}, ErrCodexAccountAlreadyActive
	}
	if current.Revision != cfg.ExpectedAccountRevision {
		return domain.CodexAccountSwitch{}, ErrCodexAccountRevisionConflict
	}
	if err := credentials.VerifyCodexAccountForSwitch(ctx, cfg.TargetAccountID); err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	if credentials.CodexAccountLoginInProgress() {
		return domain.CodexAccountSwitch{}, ports.ErrCodexAccountLoginInProgress
	}

	now := m.clock()
	sw := domain.CodexAccountSwitch{
		ID: uuid.NewString(), SourceAccountID: current.AccountID, TargetAccountID: cfg.TargetAccountID,
		Phase:          domain.CodexAccountSwitchRequested,
		IdempotencyKey: cfg.IdempotencyKey, RequestFingerprint: fingerprint,
		ExpectedAccountRevision: cfg.ExpectedAccountRevision, CreatedAt: now, UpdatedAt: now,
	}
	preliminary, err := m.buildCodexAccountSwitchSnapshot(ctx)
	if err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	releaseOperations, err := m.acquireCodexSwitchSessionOperations(ctx, preliminary)
	if err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	cleanupAdmission := releaseOperations
	defer func() {
		if cleanupAdmission != nil {
			cleanupAdmission()
		}
	}()
	abortChatIntake, err := m.armCodexSwitchChatInterrupt(ctx, preliminary)
	if err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	previousCleanup := cleanupAdmission
	cleanupAdmission = func() { abortChatIntake(); previousCleanup() }
	releaseTerminalInput, err := m.freezeCodexSwitchTerminalInput(ctx, preliminary)
	if err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	previousCleanup = cleanupAdmission
	cleanupAdmission = func() { releaseTerminalInput(); previousCleanup() }
	if err := m.prepareCodexSwitchChatInterrupt(ctx, preliminary); err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	sw.Sessions, err = m.buildCodexAccountSwitchSnapshot(ctx)
	if err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	created, inserted, err := store.CreateCodexAccountSwitch(ctx, sw)
	if err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	sw = created
	if !inserted {
		return m.loadCodexAccountSwitchSessions(ctx, store, sw)
	}
	releaseMutation = false
	releaseSwitchGate = false
	cleanupAdmission = nil
	m.agentSwitchWorkers.Add(1)
	go func() {
		defer m.agentSwitchWorkers.Done()
		m.runCodexAccountSwitchWithAdmission(m.backgroundContext, credentials, store, sw, &codexSwitchAdmission{
			releaseOperations: releaseOperations,
			abortChatIntake:   abortChatIntake,
			releaseInput:      releaseTerminalInput,
		})
	}()
	return sw, nil
}

func (m *Manager) buildCodexAccountSwitchSnapshot(ctx context.Context) ([]domain.CodexAccountSwitchSession, error) {
	records, err := m.store.ListAllSessions(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	reviewers := m.codexReviewerLifecycle()
	result := make([]domain.CodexAccountSwitchSession, 0)
	for _, rec := range records {
		if rec.IsTerminated {
			continue
		}
		reviewerRunning := false
		reviewerHandleID := ""
		reviewerNativeID := ""
		if reviewers != nil {
			snapshot, snapshotErr := reviewers.SnapshotCodexReviewer(ctx, rec.ID)
			err = snapshotErr
			if err != nil {
				return nil, err
			}
			reviewerRunning = snapshot.Running
			reviewerHandleID = snapshot.HandleID
			reviewerNativeID = snapshot.NativeSessionID
			if reviewerRunning {
				if strings.TrimSpace(reviewerHandleID) == "" || strings.TrimSpace(reviewerNativeID) == "" {
					return nil, fmt.Errorf("%w: %s reviewer", ErrCodexRunningSessionNotResumable, rec.ID)
				}
			}
		}
		if rec.Harness != domain.HarnessCodex && !reviewerRunning {
			continue
		}
		mode := domain.NormalizeSessionMode(rec.Mode)
		nativeID := ""
		generation := strings.TrimSpace(rec.Metadata.RuntimeLaunchID)
		wasRunning := false
		if rec.Harness == domain.HarnessCodex && mode == domain.SessionModeChat {
			nativeID = strings.TrimSpace(rec.Metadata.ProviderConversationID)
			generation = strings.TrimSpace(rec.Metadata.ControllerGeneration)
			wasRunning = m.chat != nil && m.chat.HasLiveChatController(rec.ID)
		} else if rec.Harness == domain.HarnessCodex {
			nativeID = strings.TrimSpace(rec.Metadata.AgentSessionID)
			if strings.TrimSpace(rec.Metadata.RuntimeHandleID) != "" {
				wasRunning, err = m.codexTUIWorkloadRunning(ctx, rec)
				if err != nil {
					return nil, err
				}
			}
		}
		if rec.Harness == domain.HarnessCodex && wasRunning && nativeID == "" {
			if mode != domain.SessionModeTUI || !m.codexTUIFreshRestartSafe(ctx, rec) {
				return nil, fmt.Errorf("%w: %s", ErrCodexRunningSessionNotResumable, rec.ID)
			}
		}
		if rec.Harness == domain.HarnessCodex && wasRunning && generation == "" {
			return nil, fmt.Errorf("%w: %s controller generation", ErrCodexRunningSessionNotResumable, rec.ID)
		}
		if !wasRunning && !reviewerRunning {
			continue
		}
		item := domain.CodexAccountSwitchSession{
			SessionID: rec.ID, NativeSessionID: nativeID, InterfaceMode: mode,
			SourceHandleID: strings.TrimSpace(rec.Metadata.RuntimeHandleID), SourceGeneration: generation,
			WasRunning: wasRunning, StopState: "pending", RestartState: "pending",
			ReviewerWasRunning: reviewerRunning, ReviewerStopState: "skipped", ReviewerRestartState: "skipped",
			ReviewerSourceHandleID: reviewerHandleID, ReviewerNativeSessionID: reviewerNativeID,
		}
		if !wasRunning {
			item.RestartState = "skipped"
		}
		if reviewerRunning {
			item.ReviewerStopState = "pending"
			item.ReviewerRestartState = "pending"
		}
		result = append(result, item)
	}
	return result, nil
}

type codexSwitchAdmission struct {
	releaseOperations func()
	abortChatIntake   func()
	releaseInput      func()
}

// codexTUIWorkloadRunning distinguishes a live terminal host from the Codex
// process it was created to supervise. Interactive runtimes intentionally keep
// their shell alive after Codex exits so users retain scrollback; that shell is
// not a running Codex writer and must not block a global account switch.
//
// Runtime implementations without workload inspection retain the conservative
// legacy behavior: a live host is treated as a live workload. Probe failures are
// inconclusive and fail admission before any credential mutation.
func (m *Manager) codexTUIWorkloadRunning(ctx context.Context, rec domain.SessionRecord) (bool, error) {
	handle := ports.RuntimeHandle{ID: strings.TrimSpace(rec.Metadata.RuntimeHandleID)}
	hostAlive, err := m.runtime.IsAlive(ctx, handle)
	if err != nil {
		return false, fmt.Errorf("inspect Codex runtime host for %s: %w", rec.ID, err)
	}
	if !hostAlive {
		return false, nil
	}
	launchID := strings.TrimSpace(rec.Metadata.RuntimeLaunchID)
	inspector, ok := m.runtime.(ports.SupervisedProcessInspector)
	if !ok || launchID == "" {
		return true, nil
	}
	workloadAlive, err := inspector.IsSupervisedProcessAlive(ctx, handle, ports.SupervisedProcessRef{
		SessionID: rec.ID,
		LaunchID:  launchID,
	})
	if err != nil {
		return false, fmt.Errorf("inspect Codex workload for %s: %w", rec.ID, err)
	}
	return workloadAlive, nil
}

// codexTUIFreshRestartSafe positively proves that a live Codex process has not
// started a native conversation yet. The terminal-surface proof is shared with
// interface switching: missing hooks, metadata, or rollout files alone are not
// enough. An admitted switch session with an empty NativeSessionID therefore
// durably means "restart this untouched controller fresh"; older code never
// persisted that shape for a running Codex session.
func (m *Manager) codexTUIFreshRestartSafe(ctx context.Context, rec domain.SessionRecord) bool {
	if m.agents == nil {
		return false
	}
	agent, ok := m.agents.Agent(rec.Harness)
	return ok && m.nativeConversationNotStarted(ctx, rec, agent)
}

func codexAccountSwitchRestartPolicy(item domain.CodexAccountSwitchSession) (forceFresh, requireNativeHistory bool) {
	forceFresh = item.WasRunning && item.InterfaceMode == domain.SessionModeTUI && strings.TrimSpace(item.NativeSessionID) == ""
	return forceFresh, !forceFresh
}

func validateCodexSwitchWorkerIdentity(rec domain.SessionRecord, item domain.CodexAccountSwitchSession) error {
	if rec.ID != item.SessionID || rec.Harness != domain.HarnessCodex || domain.NormalizeSessionMode(rec.Mode) != item.InterfaceMode {
		return errors.New("codex session identity changed")
	}
	handleID := strings.TrimSpace(rec.Metadata.RuntimeHandleID)
	generation := strings.TrimSpace(rec.Metadata.RuntimeLaunchID)
	nativeID := strings.TrimSpace(rec.Metadata.AgentSessionID)
	if item.InterfaceMode == domain.SessionModeChat {
		handleID = ""
		generation = strings.TrimSpace(rec.Metadata.ControllerGeneration)
		nativeID = strings.TrimSpace(rec.Metadata.ProviderConversationID)
	}
	if handleID != item.SourceHandleID || generation != item.SourceGeneration || nativeID != item.NativeSessionID {
		return errors.New("codex controller identity changed")
	}
	return nil
}

func (m *Manager) ensureCodexSwitchChatController(ctx context.Context, rec domain.SessionRecord, item domain.CodexAccountSwitchSession, generation string) (domain.SessionRecord, error) {
	if m.chat == nil {
		return rec, errors.New("codex Chat lifecycle is unavailable")
	}
	if m.chat.HasLiveChatController(rec.ID) {
		if rec.ID != item.SessionID ||
			strings.TrimSpace(rec.Metadata.ProviderConversationID) != item.NativeSessionID ||
			strings.TrimSpace(rec.Metadata.ControllerGeneration) != strings.TrimSpace(generation) {
			return rec, errors.New("codex Chat recovery found a different controller identity")
		}
		return rec, nil
	}
	result, err := m.resumeAgentRecordWithReservedGeneration(
		ctx, "Codex account switch recovery", rec, false, true, generation,
	)
	if err != nil {
		return rec, err
	}
	if result.Session.ID != item.SessionID ||
		strings.TrimSpace(result.Session.Metadata.ProviderConversationID) != item.NativeSessionID ||
		strings.TrimSpace(result.Session.Metadata.ControllerGeneration) != strings.TrimSpace(generation) {
		return rec, errors.New("codex Chat recovery attached a different controller identity")
	}
	return result.Session, nil
}

func (m *Manager) runCodexAccountSwitchWithAdmission(ctx context.Context, credentials ports.CodexAccountCredentialManager, store ports.CodexAccountSwitchStore, sw domain.CodexAccountSwitch, admission *codexSwitchAdmission) {
	ctx = codexExclusiveOperationContext(ctx)
	defer func() { m.finishCodexAccountSwitchMutation(credentials, retainCodexAccountSwitchFence(sw.Phase)) }()
	sessions := sw.Sessions
	var err error
	if admission == nil {
		sessions, err = store.ListCodexAccountSwitchSessions(ctx, sw.ID)
		if err != nil {
			if advanceErr := m.advanceCodexAccountSwitch(ctx, store, &sw, domain.CodexAccountSwitchRecoveryRequired, "switch_state_unavailable"); advanceErr != nil {
				m.logger.Error("Codex account switch: failed to persist recovery state", "switchID", sw.ID, "error", advanceErr)
			}
			return
		}
		admission = &codexSwitchAdmission{}
		admission.releaseOperations, err = m.acquireCodexSwitchSessionOperations(ctx, sessions)
		if err != nil {
			m.failCodexAccountSwitchAndLog(ctx, store, &sw, "session_operation_in_progress")
			return
		}
	}
	releaseOperations := admission.releaseOperations
	defer func() {
		if !retainCodexSwitchSessionOperations(sw.Phase) {
			releaseOperations()
		}
	}()
	if admission.abortChatIntake == nil {
		admission.abortChatIntake = func() {}
		admission.releaseInput = func() {}
		if sw.Phase == domain.CodexAccountSwitchRequested || sw.Phase == domain.CodexAccountSwitchStoppingSessions {
			admission.abortChatIntake, err = m.armCodexSwitchChatInterrupt(ctx, sessions)
			if err != nil {
				m.failCodexAccountSwitchAndLog(ctx, store, &sw, "stop_unconfirmed")
				return
			}
			admission.releaseInput, err = m.freezeCodexSwitchTerminalInput(ctx, sessions)
			if err != nil {
				admission.abortChatIntake()
				m.failCodexAccountSwitchAndLog(ctx, store, &sw, "stop_unconfirmed")
				return
			}
			if err := m.prepareCodexSwitchChatInterrupt(ctx, sessions); err != nil {
				admission.releaseInput()
				admission.abortChatIntake()
				m.failCodexAccountSwitchAndLog(ctx, store, &sw, "stop_unconfirmed")
				return
			}
		}
	}
	abortChatIntake := admission.abortChatIntake
	chatStopped := false
	defer func() {
		if !chatStopped {
			abortChatIntake()
		}
	}()
	releaseTerminalInput := admission.releaseInput
	defer releaseTerminalInput()
	m.dispatchCodexAccountSwitch(ctx, credentials, store, &sw, sessions)
	chatStopped = sw.Phase != domain.CodexAccountSwitchRequested && sw.Phase != domain.CodexAccountSwitchStoppingSessions
}

func (m *Manager) dispatchCodexAccountSwitch(ctx context.Context, credentials ports.CodexAccountCredentialManager, store ports.CodexAccountSwitchStore, sw *domain.CodexAccountSwitch, sessions []domain.CodexAccountSwitchSession) {
	for {
		switch sw.Phase {
		case domain.CodexAccountSwitchRequested:
			if m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchStoppingSessions, "") != nil {
				return
			}
		case domain.CodexAccountSwitchStoppingSessions:
			if err := m.stopCodexSwitchSessions(ctx, store, sw.ID, sessions); err != nil {
				_ = m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchRecoveryRequired, "stop_unconfirmed")
				return
			}
			if m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchSessionsStopped, "") != nil {
				return
			}
		case domain.CodexAccountSwitchSessionsStopped:
			if m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchCheckpointCredential, "") != nil {
				return
			}
		case domain.CodexAccountSwitchCheckpointCredential:
			if m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchActivatingAccount, "") != nil {
				return
			}
		case domain.CodexAccountSwitchActivatingAccount:
			active := credentials.CurrentCodexActiveAccount()
			if active.AccountID != sw.TargetAccountID {
				if active.AccountID != sw.SourceAccountID || active.Revision != sw.ExpectedAccountRevision {
					_ = m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchRecoveryRequired, "activation_unconfirmed")
					return
				}
				if _, err := credentials.CheckpointAndActivateCodexAccount(ctx, sw.ID, sw.TargetAccountID, sw.ExpectedAccountRevision); err != nil {
					if m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchRollbackRequired, "activation_unconfirmed") != nil {
						return
					}
					continue
				}
			}
			committedAt := m.clock()
			sw.CredentialsCommittedAt = &committedAt
			if m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchVerifyingAccount, "") != nil {
				return
			}
		case domain.CodexAccountSwitchVerifyingAccount:
			if err := credentials.VerifyCurrentCodexAccount(ctx, sw.TargetAccountID); err != nil {
				_ = m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchRecoveryRequired, "target_verification_unconfirmed")
				return
			}
			if m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchRestartingSessions, "") != nil {
				return
			}
		case domain.CodexAccountSwitchRestartingSessions:
			if err := m.restartCodexSwitchSessions(ctx, store, sw.ID, sessions); err != nil {
				_ = m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchRecoveryRequired, "restart_unconfirmed")
				return
			}
			completed := m.clock()
			sw.CompletedAt = &completed
			_ = m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchCompleted, "")
			return
		case domain.CodexAccountSwitchRollbackRequired:
			if err := credentials.RestoreCodexAccountCredential(ctx, sw.SourceAccountID, sw.TargetAccountID); err != nil {
				_ = m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchRecoveryRequired, "rollback_unconfirmed")
				return
			}
			if err := credentials.VerifyCurrentCodexAccount(ctx, sw.SourceAccountID); err != nil {
				_ = m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchRecoveryRequired, "rollback_unconfirmed")
				return
			}
			if err := m.restartCodexSwitchSessions(ctx, store, sw.ID, sessions); err != nil {
				_ = m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchRecoveryRequired, "restart_unconfirmed")
				return
			}
			completed := m.clock()
			sw.CompletedAt = &completed
			_ = m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchFailed, sw.FailureCode)
			return
		case domain.CodexAccountSwitchRecoveryRequired:
			active := credentials.CurrentCodexActiveAccount()
			if active.AccountID == sw.TargetAccountID {
				if sw.CredentialsCommittedAt == nil {
					committedAt := m.clock()
					sw.CredentialsCommittedAt = &committedAt
				}
				if m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchVerifyingAccount, "") != nil {
					return
				}
				continue
			}
			if active.AccountID == sw.SourceAccountID {
				if m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchRollbackRequired, sw.FailureCode) != nil {
					return
				}
				continue
			}
			return
		case domain.CodexAccountSwitchCompleted, domain.CodexAccountSwitchFailed:
			return
		default:
			return
		}
	}
}

func (m *Manager) acquireCodexSwitchSessionOperations(ctx context.Context, sessions []domain.CodexAccountSwitchSession) (func(), error) {
	acquired := make([]domain.SessionID, 0, len(sessions))
	for _, item := range sessions {
		rec, found, err := m.store.GetSession(ctx, item.SessionID)
		if err != nil {
			for i := len(acquired) - 1; i >= 0; i-- {
				m.endAgentOperation(acquired[i], agentOperationCodexAccountSwitch)
			}
			return nil, err
		}
		if !found {
			for i := len(acquired) - 1; i >= 0; i-- {
				m.endAgentOperation(acquired[i], agentOperationCodexAccountSwitch)
			}
			return nil, ErrNotFound
		}
		// A non-Codex worker may own a Codex reviewer. The review engine has its
		// own worker lock and the daemon-wide reviewer gate, so reserving the
		// worker session here would unnecessarily block unrelated Claude/other
		// harness input for the duration of the Codex credential switch.
		if rec.Harness != domain.HarnessCodex {
			continue
		}
		if err := m.beginOrReclaimCodexAccountSwitchOperation(ctx, item.SessionID); err != nil {
			for i := len(acquired) - 1; i >= 0; i-- {
				m.endAgentOperation(acquired[i], agentOperationCodexAccountSwitch)
			}
			return nil, err
		}
		acquired = append(acquired, item.SessionID)
	}
	return func() {
		for i := len(acquired) - 1; i >= 0; i-- {
			m.endAgentOperation(acquired[i], agentOperationCodexAccountSwitch)
		}
	}, nil
}

func (m *Manager) beginOrReclaimCodexAccountSwitchOperation(ctx context.Context, id domain.SessionID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.agentOpMu.Lock()
	if current, active := m.agentOperations[id]; active {
		m.agentOpMu.Unlock()
		if current == agentOperationCodexAccountSwitch {
			return nil
		}
		return errAgentOperationInProgress
	}
	m.agentOperations[id] = agentOperationCodexAccountSwitch
	drained := m.inputDrained[id]
	m.agentOpMu.Unlock()
	if drained == nil {
		return nil
	}
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		m.endAgentOperation(id, agentOperationCodexAccountSwitch)
		return ctx.Err()
	}
}

func retainCodexSwitchSessionOperations(phase domain.CodexAccountSwitchPhase) bool {
	switch phase {
	case domain.CodexAccountSwitchStoppingSessions,
		domain.CodexAccountSwitchSessionsStopped,
		domain.CodexAccountSwitchCheckpointCredential,
		domain.CodexAccountSwitchActivatingAccount,
		domain.CodexAccountSwitchVerifyingAccount,
		domain.CodexAccountSwitchRestartingSessions,
		domain.CodexAccountSwitchRollbackRequired,
		domain.CodexAccountSwitchRecoveryRequired:
		return true
	default:
		return false
	}
}

func retainCodexAccountSwitchFence(phase domain.CodexAccountSwitchPhase) bool {
	return !phase.Terminal()
}

func (m *Manager) stopCodexSwitchSessions(ctx context.Context, store ports.CodexAccountSwitchStore, switchID string, sessions []domain.CodexAccountSwitchSession) error {
	reviewers := m.codexReviewerLifecycle()
	for i := range sessions {
		item := &sessions[i]
		if item.StopState == "stopped" && (item.ReviewerStopState == "stopped" || item.ReviewerStopState == "skipped") {
			continue
		}
		if !item.WasRunning && item.StopState != "stopped" {
			previous := item.StopState
			item.StopState = "stopped"
			if err := m.persistCodexSwitchSession(ctx, store, switchID, *item, previous, item.RestartState); err != nil {
				return err
			}
		}
		if item.WasRunning && item.StopState != "stopped" {
			previousStop := item.StopState
			if item.ErrorCode != codexSwitchStopIntent {
				item.ErrorCode = codexSwitchStopIntent
				if err := m.persistCodexSwitchSession(ctx, store, switchID, *item, previousStop, item.RestartState); err != nil {
					return err
				}
			}
			rec, ok, readErr := m.store.GetSession(ctx, item.SessionID)
			if readErr != nil || !ok {
				item.StopState, item.ErrorCode = "failed", "session_missing"
				return errors.Join(errors.New("session missing"), m.persistCodexSwitchSession(ctx, store, switchID, *item, previousStop, item.RestartState))
			}
			if err := validateCodexSwitchWorkerIdentity(rec, *item); err != nil {
				item.StopState, item.ErrorCode = "failed", "source_generation_changed"
				return errors.Join(errors.New("codex source generation changed before shutdown"), m.persistCodexSwitchSession(ctx, store, switchID, *item, previousStop, item.RestartState))
			}
			alreadyStopped := rec.Activity.State == domain.ActivityExited
			if item.InterfaceMode == domain.SessionModeChat && !alreadyStopped && (m.chat == nil || !m.chat.HasLiveChatController(rec.ID)) {
				rec, readErr = m.ensureCodexSwitchChatController(ctx, rec, *item, item.SourceGeneration)
				if readErr != nil {
					item.StopState, item.ErrorCode = "failed", "stop_unconfirmed"
					return errors.Join(readErr, m.persistCodexSwitchSession(ctx, store, switchID, *item, previousStop, item.RestartState))
				}
			}
			stopErr := error(nil)
			if !alreadyStopped {
				stopErr = m.stopAgentController(ctx, rec)
			}
			if stopErr == nil {
				if !alreadyStopped {
					stopErr = m.recordAgentExited(ctx, rec)
				}
			}
			if stopErr != nil {
				item.StopState, item.ErrorCode = "failed", "stop_unconfirmed"
				return errors.Join(stopErr, m.persistCodexSwitchSession(ctx, store, switchID, *item, previousStop, item.RestartState))
			}
			now := m.clock()
			previous := item.StopState
			item.StopState, item.StoppedAt, item.ErrorCode = "stopped", &now, ""
			if err := m.persistCodexSwitchSession(ctx, store, switchID, *item, previous, item.RestartState); err != nil {
				return err
			}
		}
		if item.ReviewerWasRunning && item.ReviewerStopState != "stopped" && item.ReviewerStopState != "skipped" {
			if reviewers == nil {
				item.ReviewerStopState, item.ErrorCode = "failed", "reviewer_stop_unconfirmed"
				return errors.Join(errors.New("codex reviewer lifecycle is unavailable"), m.persistCodexSwitchSession(ctx, store, switchID, *item, item.StopState, item.RestartState))
			}
			item.ErrorCode = codexSwitchReviewerStopIntent
			if err := m.persistCodexSwitchSession(ctx, store, switchID, *item, item.StopState, item.RestartState); err != nil {
				return err
			}
			snapshot, snapshotErr := reviewers.SnapshotCodexReviewer(ctx, item.SessionID)
			stopped, stopErr := false, snapshotErr
			if snapshotErr == nil && !snapshot.Running {
				stopped = true
			} else if snapshotErr == nil && snapshot.NativeSessionID == item.ReviewerNativeSessionID && snapshot.HandleID == item.ReviewerSourceHandleID {
				stopped, stopErr = reviewers.SuspendCodexReviewerExact(ctx, item.SessionID, item.ReviewerSourceHandleID, item.ReviewerNativeSessionID)
			}
			if stopErr != nil || !stopped {
				item.ReviewerStopState, item.ErrorCode = "failed", "reviewer_stop_unconfirmed"
				persistErr := m.persistCodexSwitchSession(ctx, store, switchID, *item, item.StopState, item.RestartState)
				if stopErr != nil {
					return errors.Join(stopErr, persistErr)
				}
				return errors.Join(errors.New("codex reviewer stop was not confirmed"), persistErr)
			}
			item.ReviewerStopState, item.ErrorCode = "stopped", ""
			if err := m.persistCodexSwitchSession(ctx, store, switchID, *item, item.StopState, item.RestartState); err != nil {
				return err
			}
		}
	}
	return nil
}

func codexSwitchReservedGeneration(item domain.CodexAccountSwitchSession) string {
	for _, prefix := range []string{codexSwitchRestartIntentPrefix, codexSwitchRestartUnknownPrefix} {
		if generation, ok := strings.CutPrefix(item.ErrorCode, prefix); ok {
			return strings.TrimSpace(generation)
		}
	}
	return ""
}

func codexSwitchTargetOwner(rec domain.SessionRecord, item domain.CodexAccountSwitchSession, generation string) bool {
	if rec.ID != item.SessionID || rec.Harness != domain.HarnessCodex || domain.NormalizeSessionMode(rec.Mode) != item.InterfaceMode {
		return false
	}
	if item.InterfaceMode == domain.SessionModeChat {
		return rec.Metadata.ProviderConversationID == item.NativeSessionID && rec.Metadata.ControllerGeneration == generation
	}
	return rec.Metadata.AgentSessionID == item.NativeSessionID && rec.Metadata.RuntimeLaunchID == generation && rec.Metadata.RuntimeHandleID == item.SourceHandleID
}

func (m *Manager) restartCodexSwitchSessions(ctx context.Context, store ports.CodexAccountSwitchStore, switchID string, sessions []domain.CodexAccountSwitchSession) error {
	var errs []error
	reviewers := m.codexReviewerLifecycle()
	for i := range sessions {
		item := &sessions[i]
		if item.WasRunning && item.StopState == "stopped" && item.RestartState == "restarted" && item.InterfaceMode == domain.SessionModeChat {
			rec, ok, readErr := m.store.GetSession(ctx, item.SessionID)
			if readErr != nil || !ok {
				errs = append(errs, fmt.Errorf("reattach restarted Chat session %s: %w", item.SessionID, errors.Join(errors.New("session missing"), readErr)))
				continue
			}
			if _, attachErr := m.ensureCodexSwitchChatController(ctx, rec, *item, rec.Metadata.ControllerGeneration); attachErr != nil {
				errs = append(errs, fmt.Errorf("reattach restarted Chat session %s: %w", item.SessionID, attachErr))
				continue
			}
		}
		if item.WasRunning && item.StopState == "stopped" && item.RestartState != "restarted" && item.RestartState != "skipped" {
			previousRestart := item.RestartState
			generation := codexSwitchReservedGeneration(*item)
			if generation == "" {
				generation = strings.TrimSpace(m.newLaunchID())
				if generation == "" {
					errs = append(errs, fmt.Errorf("restart session %s: generated empty Codex restart generation", item.SessionID))
					continue
				}
				item.ErrorCode = codexSwitchRestartIntentPrefix + generation
				if err := m.persistCodexSwitchSession(ctx, store, switchID, *item, item.StopState, previousRestart); err != nil {
					return err
				}
			}
			var workerErr error
			rec, ok, readErr := m.store.GetSession(ctx, item.SessionID)
			if readErr != nil || !ok {
				item.RestartState, item.ErrorCode = "failed", codexSwitchRestartUnknownPrefix+generation
				if err := m.persistCodexSwitchSession(ctx, store, switchID, *item, item.StopState, previousRestart); err != nil {
					return err
				}
				errs = append(errs, fmt.Errorf("restart session %s: %w", item.SessionID, errors.Join(errors.New("session missing"), readErr)))
				continue
			}
			adopted := false
			if codexSwitchTargetOwner(rec, *item, generation) {
				if item.InterfaceMode == domain.SessionModeChat {
					rec, readErr = m.ensureCodexSwitchChatController(ctx, rec, *item, generation)
					adopted = readErr == nil
				} else {
					adopted, readErr = m.exactTargetGenerationAlive(ctx, ports.RuntimeHandle{ID: item.SourceHandleID}, item.SessionID, domain.AgentGenerationID(generation))
				}
				if readErr != nil {
					workerErr = readErr
				}
			} else if identityErr := validateCodexSwitchWorkerIdentity(rec, *item); identityErr != nil {
				workerErr = identityErr
			}
			if !adopted && workerErr == nil {
				forceFresh, requireNativeHistory := codexAccountSwitchRestartPolicy(*item)
				var result RestoreResult
				result, workerErr = m.resumeAgentRecordWithReservedGeneration(
					ctx, "Codex account switch", rec, forceFresh, requireNativeHistory, generation,
				)
				// An interrupted Codex Chat turn can take slightly longer than the
				// first bounded history-read window to flush its native checkpoint.
				// Retry that exact native ID and reserved generation once. The first
				// attempt has already waited for settlement, so this remains bounded
				// and avoids requiring a manual recovery click for the common race.
				if workerErr != nil && item.InterfaceMode == domain.SessionModeChat && errors.Is(workerErr, ports.ErrChatHistoryUnsettled) && ctx.Err() == nil {
					result, workerErr = m.resumeAgentRecordWithReservedGeneration(
						ctx, "Codex account switch", rec, forceFresh, requireNativeHistory, generation,
					)
				}
				if workerErr == nil && result.Session.ID != item.SessionID {
					workerErr = errors.New("codex resumed a different AO session")
				}
				if workerErr == nil && requireNativeHistory && item.InterfaceMode == domain.SessionModeTUI && result.Mode != RestoreModeNative {
					workerErr = errors.New("codex native history resume was not selected")
				}
				if workerErr == nil && requireNativeHistory {
					resumedNativeID := strings.TrimSpace(result.Session.Metadata.AgentSessionID)
					if item.InterfaceMode == domain.SessionModeChat {
						resumedNativeID = strings.TrimSpace(result.Session.Metadata.ProviderConversationID)
					}
					if resumedNativeID != item.NativeSessionID {
						workerErr = errors.New("codex resumed a different native history")
					}
				}
			}
			if workerErr != nil {
				item.RestartState, item.ErrorCode = "failed", codexSwitchRestartUnknownPrefix+generation
			} else {
				at := m.clock()
				item.RestartState, item.RestartedAt, item.ErrorCode = "restarted", &at, ""
			}
			if err := m.persistCodexSwitchSession(ctx, store, switchID, *item, item.StopState, previousRestart); err != nil {
				return errors.Join(workerErr, err)
			}
			if workerErr != nil {
				errs = append(errs, fmt.Errorf("restart session %s: %w", item.SessionID, workerErr))
			}
		}
		if item.ReviewerWasRunning && item.ReviewerStopState == "stopped" && item.ReviewerRestartState != "restarted" && item.ReviewerRestartState != "skipped" {
			workerFailureCode := ""
			if item.RestartState == "failed" {
				workerFailureCode = item.ErrorCode
			}
			var reviewerErr error
			if reviewers == nil {
				item.ReviewerRestartState = "failed"
				if workerFailureCode == "" {
					item.ErrorCode = "reviewer_restart_unconfirmed"
				}
				reviewerErr = errors.New("codex reviewer lifecycle is unavailable")
			} else {
				if workerFailureCode == "" {
					item.ErrorCode = codexSwitchReviewerRestartIntent
				}
				if err := m.persistCodexSwitchSession(ctx, store, switchID, *item, item.StopState, item.RestartState); err != nil {
					return err
				}
				snapshot, snapshotErr := reviewers.SnapshotCodexReviewer(ctx, item.SessionID)
				if snapshotErr == nil && snapshot.Running && snapshot.NativeSessionID == item.ReviewerNativeSessionID {
					item.ReviewerRestartState = "restarted"
					item.ErrorCode = workerFailureCode
				} else if err := reviewers.RestoreCodexReviewerExact(ctx, item.SessionID, item.ReviewerNativeSessionID); err != nil {
					item.ReviewerRestartState = "failed"
					if workerFailureCode == "" {
						item.ErrorCode = "reviewer_restart_unconfirmed"
					}
					reviewerErr = errors.Join(snapshotErr, err)
				} else {
					snapshot, snapshotErr = reviewers.SnapshotCodexReviewer(ctx, item.SessionID)
					if snapshotErr != nil || !snapshot.Running || snapshot.NativeSessionID != item.ReviewerNativeSessionID {
						item.ReviewerRestartState = "failed"
						if workerFailureCode == "" {
							item.ErrorCode = "reviewer_native_history_changed"
						}
						reviewerErr = errors.Join(snapshotErr, errors.New("codex reviewer did not resume the recorded native history"))
					} else {
						item.ReviewerRestartState = "restarted"
						item.ErrorCode = workerFailureCode
					}
				}
			}
			if err := m.persistCodexSwitchSession(ctx, store, switchID, *item, item.StopState, item.RestartState); err != nil {
				return errors.Join(reviewerErr, err)
			}
			if reviewerErr != nil {
				errs = append(errs, fmt.Errorf("restart reviewer for session %s: %w", item.SessionID, reviewerErr))
			}
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) armCodexSwitchChatInterrupt(ctx context.Context, sessions []domain.CodexAccountSwitchSession) (func(), error) {
	handoff, ok := m.chat.(chatHandoffLauncher)
	if !ok {
		for _, item := range sessions {
			if item.WasRunning && item.InterfaceMode == domain.SessionModeChat {
				return func() {}, errors.New("codex Chat drain is unavailable")
			}
		}
		return func() {}, nil
	}
	armed := make([]domain.SessionID, 0)
	for _, item := range sessions {
		if !item.WasRunning || item.InterfaceMode != domain.SessionModeChat {
			continue
		}
		if err := handoff.ArmChatHandoff(ctx, item.SessionID, domain.SessionInterfaceTransitionInterrupt); err != nil {
			for _, id := range armed {
				handoff.AbortChatHandoff(id)
			}
			return func() {}, err
		}
		armed = append(armed, item.SessionID)
	}
	return func() {
		for _, id := range armed {
			handoff.AbortChatHandoff(id)
		}
	}, nil
}

func (m *Manager) prepareCodexSwitchChatInterrupt(ctx context.Context, sessions []domain.CodexAccountSwitchSession) error {
	handoff, ok := m.chat.(chatHandoffLauncher)
	if !ok {
		return nil
	}
	for _, item := range sessions {
		if item.WasRunning && item.InterfaceMode == domain.SessionModeChat {
			if err := handoff.PrepareChatHandoff(ctx, item.SessionID, domain.SessionInterfaceTransitionInterrupt); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Manager) freezeCodexSwitchTerminalInput(ctx context.Context, sessions []domain.CodexAccountSwitchSession) (func(), error) {
	releases := make([]func(), 0)
	releaseAll := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for _, item := range sessions {
		if !item.WasRunning || item.InterfaceMode != domain.SessionModeTUI {
			continue
		}
		rec, ok, err := m.store.GetSession(ctx, item.SessionID)
		if err != nil || !ok {
			releaseAll()
			if err != nil {
				return func() {}, err
			}
			return func() {}, ErrNotFound
		}
		_, release := m.beginTerminalInputDrain(rec)
		if release != nil {
			releases = append(releases, release)
		}
	}
	return releaseAll, nil
}

func (m *Manager) persistCodexSwitchSession(ctx context.Context, store ports.CodexAccountSwitchStore, switchID string, item domain.CodexAccountSwitchSession, expectedStop, expectedRestart string) error {
	ok, err := store.UpdateCodexAccountSwitchSession(ctx, switchID, item, expectedStop, expectedRestart)
	if err == nil && ok {
		return nil
	}
	settleCtx, cancel := switchDurableContext(ctx)
	defer cancel()
	current, found, readErr := readCodexSwitchSession(settleCtx, store, switchID, item.SessionID)
	if readErr == nil && found && codexSwitchSessionProgressEqual(current, item) {
		return nil
	}
	if err != nil {
		return errors.Join(err, readErr)
	}
	return errors.Join(errors.New("codex account switch session changed concurrently"), readErr)
}

func readCodexSwitchSession(ctx context.Context, store ports.CodexAccountSwitchStore, switchID string, sessionID domain.SessionID) (domain.CodexAccountSwitchSession, bool, error) {
	sessions, err := store.ListCodexAccountSwitchSessions(ctx, switchID)
	if err != nil {
		return domain.CodexAccountSwitchSession{}, false, err
	}
	for _, item := range sessions {
		if item.SessionID == sessionID {
			return item, true, nil
		}
	}
	return domain.CodexAccountSwitchSession{}, false, nil
}

func codexSwitchSessionProgressEqual(left, right domain.CodexAccountSwitchSession) bool {
	return left.StopState == right.StopState && left.RestartState == right.RestartState &&
		left.ReviewerStopState == right.ReviewerStopState && left.ReviewerRestartState == right.ReviewerRestartState &&
		left.ErrorCode == right.ErrorCode
}

func (m *Manager) advanceCodexAccountSwitch(ctx context.Context, store ports.CodexAccountSwitchStore, sw *domain.CodexAccountSwitch, next domain.CodexAccountSwitchPhase, code string) error {
	expected := sw.Phase
	candidate := *sw
	candidate.Phase, candidate.FailureCode, candidate.UpdatedAt = next, code, m.clock()
	candidate.CanRecover = next == domain.CodexAccountSwitchRecoveryRequired
	ok, err := store.UpdateCodexAccountSwitch(ctx, candidate, expected)
	if err == nil && ok {
		*sw = candidate
		m.publishCodexAccountSwitchChanged()
		return nil
	}
	settleCtx, cancel := switchDurableContext(ctx)
	defer cancel()
	current, found, readErr := store.GetCodexAccountSwitch(settleCtx, sw.ID)
	if readErr != nil {
		return errors.Join(err, readErr)
	}
	if found && current.Phase == candidate.Phase && current.FailureCode == candidate.FailureCode {
		*sw = current
		sw.CanRecover = false
		return nil
	}
	if found {
		*sw = current
	}
	if err != nil {
		return err
	}
	return errors.New("codex account switch changed concurrently")
}

func (m *Manager) failCodexAccountSwitch(ctx context.Context, store ports.CodexAccountSwitchStore, sw *domain.CodexAccountSwitch, code string) error {
	completed := m.clock()
	sw.CompletedAt = &completed
	return m.advanceCodexAccountSwitch(ctx, store, sw, domain.CodexAccountSwitchFailed, code)
}

func (m *Manager) failCodexAccountSwitchAndLog(ctx context.Context, store ports.CodexAccountSwitchStore, sw *domain.CodexAccountSwitch, code string) {
	if err := m.failCodexAccountSwitch(ctx, store, sw, code); err != nil {
		m.logger.Error("Codex account switch: failed to persist failure state", "switchID", sw.ID, "error", err)
	}
}

func (m *Manager) loadCodexAccountSwitchSessions(ctx context.Context, store ports.CodexAccountSwitchStore, sw domain.CodexAccountSwitch) (domain.CodexAccountSwitch, error) {
	sessions, err := store.ListCodexAccountSwitchSessions(ctx, sw.ID)
	if err != nil {
		return sw, err
	}
	sw.Sessions = sessions
	sw.CanRecover = !sw.Phase.Terminal() && !m.codexAccountSwitchWorkerActive()
	return sw, nil
}

func (m *Manager) getCodexAccountSwitch(ctx context.Context, id string) (domain.CodexAccountSwitch, error) {
	_, store, err := m.codexAccountSwitchDependencies()
	if err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	sw, ok, err := store.GetCodexAccountSwitch(ctx, strings.TrimSpace(id))
	if err != nil {
		return sw, err
	}
	if !ok {
		return sw, ErrCodexAccountSwitchNotFound
	}
	return m.loadCodexAccountSwitchSessions(ctx, store, sw)
}

// GetActiveCodexAccountSwitch returns the sole nonterminal switch when present.
func (m *Manager) GetActiveCodexAccountSwitch(ctx context.Context) (domain.CodexAccountSwitch, bool, error) {
	_, store, err := m.codexAccountSwitchDependencies()
	if err != nil {
		return domain.CodexAccountSwitch{}, false, err
	}
	sw, ok, err := store.GetActiveCodexAccountSwitch(ctx)
	if err != nil || !ok {
		return sw, ok, err
	}
	sw, err = m.loadCodexAccountSwitchSessions(ctx, store, sw)
	return sw, err == nil, err
}

// RecoverCodexAccountSwitch retries the exact incomplete durable operation.
func (m *Manager) RecoverCodexAccountSwitch(ctx context.Context, id string) (domain.CodexAccountSwitch, error) {
	credentials, store, err := m.codexAccountSwitchDependencies()
	if err != nil {
		return domain.CodexAccountSwitch{}, err
	}
	sw, err := m.getCodexAccountSwitch(ctx, id)
	if err != nil {
		return sw, err
	}
	if sw.Phase.Terminal() {
		return sw, errors.New("codex account switch is already terminal")
	}
	if !m.claimCodexAccountSwitchRecoveryWorker(ctx) {
		return sw, ErrCodexAccountSwitchInProgress
	}
	m.agentSwitchWorkers.Add(1)
	go func() {
		defer m.agentSwitchWorkers.Done()
		m.recoverCodexAccountSwitch(m.backgroundContext, credentials, store, sw)
	}()
	return sw, nil
}

func (m *Manager) recoverCodexAccountSwitch(ctx context.Context, credentials ports.CodexAccountCredentialManager, store ports.CodexAccountSwitchStore, sw domain.CodexAccountSwitch) {
	ctx = codexExclusiveOperationContext(ctx)
	defer func() { m.finishCodexAccountSwitchMutation(credentials, retainCodexAccountSwitchFence(sw.Phase)) }()
	sessions, err := store.ListCodexAccountSwitchSessions(ctx, sw.ID)
	if err != nil {
		return
	}
	releaseOperations, err := m.acquireCodexSwitchSessionOperations(ctx, sessions)
	if err != nil {
		return
	}
	defer func() {
		if !retainCodexSwitchSessionOperations(sw.Phase) {
			releaseOperations()
		}
	}()
	m.dispatchCodexAccountSwitch(ctx, credentials, store, &sw, sessions)
}

// ReconcileCodexAccountSwitches restores the daemon-wide mutation fence before
// ordinary session adoption, then resumes the exact durable operation on the
// daemon context. The safety pass does not wait for active turns to finish.
func (m *Manager) ReconcileCodexAccountSwitches(ctx context.Context) error {
	credentials, store, err := m.codexAccountSwitchDependencies()
	if err != nil {
		return nil //nolint:nilerr // account switching is optional when its feature wiring is absent.
	}
	sw, ok, err := store.GetActiveCodexAccountSwitch(ctx)
	if err != nil || !ok {
		return err
	}
	if err := credentials.WaitCodexAccountBootstrap(ctx); err != nil {
		return err
	}
	sw, err = m.loadCodexAccountSwitchSessions(ctx, store, sw)
	if err != nil {
		return err
	}
	if err := m.acquireCodexAccountSwitchGate(ctx); err != nil {
		return err
	}
	if err := credentials.BeginCodexAccountMutation(ctx); err != nil {
		m.finishCodexAccountSwitchWorker(false)
		return err
	}
	releaseOperations, err := m.acquireCodexSwitchSessionOperations(ctx, sw.Sessions)
	if err != nil {
		m.finishCodexAccountSwitchMutation(credentials, false)
		return err
	}
	m.agentSwitchWorkers.Add(1)
	go func() {
		defer m.agentSwitchWorkers.Done()
		m.runCodexAccountSwitchWithAdmission(m.backgroundContext, credentials, store, sw, &codexSwitchAdmission{
			releaseOperations: releaseOperations,
		})
	}()
	return nil
}
