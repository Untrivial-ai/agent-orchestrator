package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

var _ ports.AgentSwitchStore = (*Store)(nil)

// CreateAgentNativeSession idempotently registers one provider-owned
// conversation. It deduplicates both AO's internal id and a known concrete
// config/native identity. An empty native id is intentionally not a
// dedupe key because providers may report it only after launch.
func (s *Store) CreateAgentNativeSession(ctx context.Context, rec domain.AgentNativeSession) (domain.AgentNativeSession, bool, error) {
	if err := validateAgentNativeSession(rec); err != nil {
		return domain.AgentNativeSession{}, false, err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	n, err := s.qw.InsertAgentNativeSession(ctx, agentNativeSessionToInsert(rec))
	if err != nil {
		return domain.AgentNativeSession{}, false, fmt.Errorf("create agent native session %s: %w", rec.ID, err)
	}
	if n > 0 {
		return rec, true, nil
	}

	// ON CONFLICT can mean either the internal id or the partial concrete-native
	// identity index. Return the already-recorded row for a genuine retry.
	if row, err := s.qw.GetAgentNativeSession(ctx, rec.ID); err == nil {
		existing := agentNativeSessionFromGen(row)
		if sameAgentNativeCreateIdentity(existing, rec) {
			return existing, false, nil
		}
		return existing, false, fmt.Errorf("create agent native session %s: %w", rec.ID, domain.ErrAgentNativeSessionConflict)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return domain.AgentNativeSession{}, false, fmt.Errorf("read conflicting agent native session %s: %w", rec.ID, err)
	}

	if rec.NativeSessionID != "" {
		row, err := s.qw.FindAgentNativeSession(ctx, gen.FindAgentNativeSessionParams{
			AoSessionID: rec.AOSessionID, Harness: rec.Harness,
			ConfigDir: rec.ConfigDir, NativeSessionID: rec.NativeSessionID,
		})
		if err == nil {
			return agentNativeSessionFromGen(row), false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return domain.AgentNativeSession{}, false, fmt.Errorf("read conflicting native identity for %s: %w", rec.ID, err)
		}
	}

	return domain.AgentNativeSession{}, false, fmt.Errorf("create agent native session %s: conflict row was not found: %w", rec.ID, domain.ErrAgentNativeSessionConflict)
}

// GetAgentNativeSession returns one retained provider conversation by AO id.
func (s *Store) GetAgentNativeSession(ctx context.Context, id domain.AgentNativeSessionID) (domain.AgentNativeSession, bool, error) {
	row, err := s.qr.GetAgentNativeSession(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentNativeSession{}, false, nil
	}
	if err != nil {
		return domain.AgentNativeSession{}, false, fmt.Errorf("get agent native session %s: %w", id, err)
	}
	return agentNativeSessionFromGen(row), true, nil
}

// ListAgentNativeSessions returns every retained provider conversation for one
// AO session, newest use first.
func (s *Store) ListAgentNativeSessions(ctx context.Context, sessionID domain.SessionID) ([]domain.AgentNativeSession, error) {
	rows, err := s.qr.ListAgentNativeSessions(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list native sessions for AO session %s: %w", sessionID, err)
	}
	out := make([]domain.AgentNativeSession, 0, len(rows))
	for _, row := range rows {
		out = append(out, agentNativeSessionFromGen(row))
	}
	return out, nil
}

// UpdateAgentNativeSession replaces mutable resume facts only when the caller's
// observed generation still owns the record. rec.LastGenerationID may advance
// the fence to a newly launched invocation in the same atomic write.
func (s *Store) UpdateAgentNativeSession(ctx context.Context, rec domain.AgentNativeSession, expectedGenerationID domain.AgentGenerationID) (bool, error) {
	if err := validateAgentNativeSession(rec); err != nil {
		return false, err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.UpdateAgentNativeSession(ctx, gen.UpdateAgentNativeSessionParams{
		ConfigDir: rec.ConfigDir, NativeSessionID: rec.NativeSessionID,
		TranscriptPath:   rec.TranscriptPath,
		NextGenerationID: rec.LastGenerationID, LastUsedAt: rec.LastUsedAt,
		ID: rec.ID, AoSessionID: rec.AOSessionID,
		ExpectedGenerationID: expectedGenerationID,
	})
	if err != nil {
		if isSQLiteUnique(err) {
			return false, fmt.Errorf("update agent native session %s: %w", rec.ID, domain.ErrAgentNativeSessionConflict)
		}
		return false, fmt.Errorf("update agent native session %s: %w", rec.ID, err)
	}
	return n > 0, nil
}

// CreateAgentSwitch idempotently starts a durable switch saga. A retry with the
// same (session,idempotency_key) returns the first record. A different request
// for a session that already has a non-terminal saga returns the active record
// together with ErrAgentSwitchInProgress.
func (s *Store) CreateAgentSwitch(ctx context.Context, rec domain.AgentSwitch) (domain.AgentSwitch, bool, error) {
	if rec.SourceTranscriptStatus == "" {
		rec.SourceTranscriptStatus = domain.AgentSwitchSourceTranscriptNotAttempted
	}
	if err := validateAgentSwitch(rec, true); err != nil {
		return domain.AgentSwitch{}, false, err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.InsertAgentSwitch(ctx, agentSwitchToInsert(rec))
	if err != nil {
		return domain.AgentSwitch{}, false, fmt.Errorf("create agent switch %s: %w", rec.ID, err)
	}
	if n > 0 {
		return rec, true, nil
	}

	row, err := s.qw.GetAgentSwitchByIdempotencyKey(ctx, gen.GetAgentSwitchByIdempotencyKeyParams{
		SessionID: rec.SessionID, IdempotencyKey: rec.IdempotencyKey,
	})
	if err == nil {
		existing := agentSwitchFromGen(row)
		if existing.SessionID == rec.SessionID && existing.IdempotencyKey == rec.IdempotencyKey &&
			existing.RequestFingerprint == rec.RequestFingerprint {
			return existing, false, nil
		}
		return existing, false, fmt.Errorf("create agent switch %s: %w", rec.ID, domain.ErrAgentSwitchIdempotencyConflict)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.AgentSwitch{}, false, fmt.Errorf("read switch idempotency key for %s: %w", rec.ID, err)
	}

	row, err = s.qw.GetActiveAgentSwitch(ctx, rec.SessionID)
	if err == nil {
		existing := agentSwitchFromGen(row)
		return existing, false, fmt.Errorf("create agent switch %s while %s is active: %w", rec.ID, existing.ID, domain.ErrAgentSwitchInProgress)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.AgentSwitch{}, false, fmt.Errorf("read active agent switch for %s: %w", rec.SessionID, err)
	}

	// The only remaining unique conflict is a reused primary id.
	if row, err = s.qw.GetAgentSwitch(ctx, rec.ID); err == nil {
		return agentSwitchFromGen(row), false, fmt.Errorf("create agent switch %s: %w", rec.ID, domain.ErrAgentSwitchIdempotencyConflict)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return domain.AgentSwitch{}, false, fmt.Errorf("read conflicting agent switch %s: %w", rec.ID, err)
	}

	return domain.AgentSwitch{}, false, fmt.Errorf("create agent switch %s: conflict row was not found: %w", rec.ID, domain.ErrAgentSwitchIdempotencyConflict)
}

// GetAgentSwitch returns one durable switch saga by id.
func (s *Store) GetAgentSwitch(ctx context.Context, id domain.AgentSwitchID) (domain.AgentSwitch, bool, error) {
	row, err := s.qr.GetAgentSwitch(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentSwitch{}, false, nil
	}
	if err != nil {
		return domain.AgentSwitch{}, false, fmt.Errorf("get agent switch %s: %w", id, err)
	}
	return agentSwitchFromGen(row), true, nil
}

// GetAgentSwitchByIdempotencyKey resolves a retried switch request.
func (s *Store) GetAgentSwitchByIdempotencyKey(ctx context.Context, sessionID domain.SessionID, idempotencyKey string) (domain.AgentSwitch, bool, error) {
	row, err := s.qr.GetAgentSwitchByIdempotencyKey(ctx, gen.GetAgentSwitchByIdempotencyKeyParams{
		SessionID: sessionID, IdempotencyKey: idempotencyKey,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentSwitch{}, false, nil
	}
	if err != nil {
		return domain.AgentSwitch{}, false, fmt.Errorf("get agent switch idempotency key for %s: %w", sessionID, err)
	}
	return agentSwitchFromGen(row), true, nil
}

// GetActiveAgentSwitch returns the session's sole non-terminal saga.
func (s *Store) GetActiveAgentSwitch(ctx context.Context, sessionID domain.SessionID) (domain.AgentSwitch, bool, error) {
	row, err := s.qr.GetActiveAgentSwitch(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentSwitch{}, false, nil
	}
	if err != nil {
		return domain.AgentSwitch{}, false, fmt.Errorf("get active agent switch for %s: %w", sessionID, err)
	}
	return agentSwitchFromGen(row), true, nil
}

// ListActiveAgentSwitches returns every non-terminal switch saga.
func (s *Store) ListActiveAgentSwitches(ctx context.Context) ([]domain.AgentSwitch, error) {
	rows, err := s.qr.ListActiveAgentSwitches(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active agent switches: %w", err)
	}
	out := make([]domain.AgentSwitch, 0, len(rows))
	for _, row := range rows {
		out = append(out, agentSwitchFromGen(row))
	}
	return out, nil
}

// ListAgentSwitches returns a session's switch history, newest first.
func (s *Store) ListAgentSwitches(ctx context.Context, sessionID domain.SessionID) ([]domain.AgentSwitch, error) {
	rows, err := s.qr.ListAgentSwitches(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list agent switches for %s: %w", sessionID, err)
	}
	out := make([]domain.AgentSwitch, 0, len(rows))
	for _, row := range rows {
		out = append(out, agentSwitchFromGen(row))
	}
	return out, nil
}

// UpdateAgentSwitch atomically advances or amends a saga only when its state
// and both invocation-generation facts still match the caller's observation.
// This rejects stale source hooks and late target callbacks from abandoned
// starts without deriving liveness from their arrival.
func (s *Store) UpdateAgentSwitch(ctx context.Context, rec domain.AgentSwitch, expectedState domain.AgentSwitchState, expectedSourceGenerationID, expectedTargetGenerationID domain.AgentGenerationID) (bool, error) {
	if err := validateAgentSwitch(rec, false); err != nil {
		return false, err
	}
	if !expectedState.Valid() {
		return false, fmt.Errorf("update agent switch %s: invalid expected state %q", rec.ID, expectedState)
	}
	if !domain.ValidAgentSwitchTransition(expectedState, rec.State) {
		return false, fmt.Errorf("update agent switch %s: invalid state transition %q -> %q", rec.ID, expectedState, rec.State)
	}
	if expectedSourceGenerationID == "" || rec.SourceGenerationID != expectedSourceGenerationID {
		return false, fmt.Errorf("update agent switch %s: source generation does not match immutable switch provenance", rec.ID)
	}
	if expectedState == domain.AgentSwitchDelivering && rec.State == domain.AgentSwitchFailed {
		return false, fmt.Errorf("update agent switch %s: delivery failure requires FailAgentSwitchIfUnacknowledged", rec.ID)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := ensureNativeSessionRefBelongsTo(ctx, s.qw, rec.SessionID, rec.TargetHarness, rec.TargetNativeSessionRef, "target"); err != nil {
		return false, fmt.Errorf("update agent switch %s: %w", rec.ID, err)
	}
	n, err := s.qw.UpdateAgentSwitch(ctx, gen.UpdateAgentSwitchParams{
		TargetNativeSessionRef: rec.TargetNativeSessionRef,
		TargetStartMode:        rec.TargetStartMode, NextState: rec.State,
		NextTargetGenerationID:    rec.TargetGenerationID,
		NextTargetRuntimeHandleID: rec.TargetRuntimeHandleID,
		ErrorCode:                 string(rec.ErrorCode),
		UpdatedAt:                 rec.UpdatedAt,
		ID:                        rec.ID, SessionID: rec.SessionID, ExpectedState: expectedState,
		ExpectedSourceGenerationID: expectedSourceGenerationID,
		ExpectedTargetGenerationID: expectedTargetGenerationID,
	})
	if err != nil {
		if isSQLiteUnique(err) {
			return false, fmt.Errorf("update agent switch %s: %w", rec.ID, domain.ErrAgentSwitchInProgress)
		}
		return false, fmt.Errorf("update agent switch %s: %w", rec.ID, err)
	}
	return n > 0, nil
}

// FailAgentSwitchIfUnacknowledged closes an ambiguous delivery only if the
// exact target generation has not acknowledged it. The acknowledgement and
// failure updates share the store write lock and opposing SQL predicates, so
// exactly one outcome wins even when the target hook arrives at the deadline.
func (s *Store) FailAgentSwitchIfUnacknowledged(ctx context.Context, rec domain.AgentSwitch) (bool, error) {
	if err := validateAgentSwitch(rec, false); err != nil {
		return false, err
	}
	if rec.State != domain.AgentSwitchFailed || rec.TargetGenerationID == "" || rec.TargetAcknowledgedAt != nil {
		return false, fmt.Errorf("fail unacknowledged agent switch %s: failed state and unacknowledged target generation are required", rec.ID)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.FailAgentSwitchIfUnacknowledged(ctx, gen.FailAgentSwitchIfUnacknowledgedParams{
		ErrorCode: string(rec.ErrorCode), FailedAt: rec.UpdatedAt,
		ID: rec.ID, SessionID: rec.SessionID,
		ExpectedSourceGenerationID: rec.SourceGenerationID,
		ExpectedTargetGenerationID: rec.TargetGenerationID,
	})
	if err != nil {
		return false, fmt.Errorf("fail unacknowledged agent switch %s: %w", rec.ID, err)
	}
	return n > 0, nil
}

// RecordAgentHandoff records optional semantic enrichment for the source
// generation that requested it. Request/received/failed/rejected transitions
// remain limited to the collection phase; unavailable may also close a leaked
// not_attempted/requested lane during later crash recovery. SQL guards keep the
// outcome monotonic, and a terminal handoff outcome can never be overwritten.
// A false result is a stale, closed, or already-settled submission and leaves
// the switch row unchanged.
func (s *Store) RecordAgentHandoff(ctx context.Context, id domain.AgentSwitchID, sourceGenerationID domain.AgentGenerationID, status domain.AgentHandoffStatus, handoffPath, handoffHash string, updatedAt time.Time) (bool, error) {
	if !status.Valid() {
		return false, fmt.Errorf("record agent handoff for %s: invalid status %q", id, status)
	}
	if sourceGenerationID == "" {
		return false, fmt.Errorf("record agent handoff for %s: source generation is required", id)
	}
	if err := validateAgentHandoffReference(status, handoffPath, handoffHash); err != nil {
		return false, fmt.Errorf("record agent handoff for %s: %w", id, err)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var (
		n   int64
		err error
	)
	switch status {
	case domain.AgentHandoffRequested:
		n, err = s.qw.RequestAgentHandoff(ctx, gen.RequestAgentHandoffParams{
			UpdatedAt: updatedAt, ID: id, SourceGenerationID: sourceGenerationID,
		})
	case domain.AgentHandoffUnavailable:
		n, err = s.qw.MarkAgentHandoffUnavailable(ctx, gen.MarkAgentHandoffUnavailableParams{
			UpdatedAt: updatedAt, ID: id, SourceGenerationID: sourceGenerationID,
		})
	case domain.AgentHandoffReceived, domain.AgentHandoffTimedOut, domain.AgentHandoffFailed, domain.AgentHandoffRejected:
		n, err = s.qw.SettleAgentHandoff(ctx, gen.SettleAgentHandoffParams{
			NextAgentHandoffStatus: status, AgentHandoffPath: handoffPath, AgentHandoffHash: handoffHash,
			UpdatedAt: updatedAt, ID: id, SourceGenerationID: sourceGenerationID,
		})
	default:
		return false, fmt.Errorf("record agent handoff for %s: status %q cannot advance a handoff", id, status)
	}
	if err != nil {
		return false, fmt.Errorf("record agent handoff for %s: %w", id, err)
	}
	return n > 0, nil
}

// FinalizeAgentSwitchHandoff binds the exact AO-owned hidden continuation to
// the conclusive source-stop boundary. When a semantic report was received,
// its public provenance reference is moved to the same final artifact because
// that artifact embeds the validated report and becomes the sole retained file.
func (s *Store) FinalizeAgentSwitchHandoff(ctx context.Context, id domain.AgentSwitchID, sessionID domain.SessionID, sourceGenerationID, targetGenerationID domain.AgentGenerationID, handoffPath, handoffHash string, semanticIncluded bool, sourceTranscriptStatus domain.AgentSwitchSourceTranscriptStatus, updatedAt time.Time) (bool, error) {
	if id == "" || sessionID == "" || sourceGenerationID == "" || targetGenerationID == "" || updatedAt.IsZero() {
		return false, errors.New("finalize agent switch handoff: switch, session, generations, and timestamp are required")
	}
	if err := validateFinalHandoffReference(handoffPath, handoffHash); err != nil {
		return false, fmt.Errorf("finalize agent switch handoff %s: %w", id, err)
	}
	if !sourceTranscriptStatus.Captured() {
		return false, fmt.Errorf("finalize agent switch handoff %s: captured source transcript status is required", id)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.FinalizeAgentSwitchHandoff(ctx, gen.FinalizeAgentSwitchHandoffParams{
		FinalHandoffPath: handoffPath, FinalHandoffHash: handoffHash,
		SemanticIncluded:       semanticIncluded,
		SourceTranscriptStatus: sourceTranscriptStatus,
		UpdatedAt:              updatedAt, ID: id, SessionID: sessionID,
		SourceGenerationID: sourceGenerationID, TargetGenerationID: targetGenerationID,
	})
	if err != nil {
		return false, fmt.Errorf("finalize agent switch handoff %s: %w", id, err)
	}
	return n > 0, nil
}

// ConfirmAgentSwitchSourceStopped records the conclusive source-process
// boundary. It changes only session activity/updated_at, retaining the source
// owner and runtime generation until target activation. The session mutation
// and stopping_source -> source_stopped transition either both commit or both
// roll back, so a stale callback cannot leave the two durable projections at
// different phases.
func (s *Store) ConfirmAgentSwitchSourceStopped(ctx context.Context, confirmation domain.AgentSwitchSourceStopConfirmation) (bool, error) {
	if err := validateAgentSwitchSourceStopConfirmation(confirmation); err != nil {
		return false, err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin confirm source stopped for agent switch %s: %w", confirmation.SwitchID, err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.qw.WithTx(tx)

	row, err := q.GetAgentSwitch(ctx, confirmation.SwitchID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("confirm source stopped for agent switch %s: read switch: %w", confirmation.SwitchID, err)
	}
	sw := agentSwitchFromGen(row)
	if sw.SessionID != confirmation.SessionID || sw.State != domain.AgentSwitchStoppingSource ||
		sw.FromHarness != confirmation.SourceHarness || sw.SourceGenerationID != confirmation.SourceGenerationID ||
		sw.TargetGenerationID != confirmation.TargetGenerationID {
		return false, nil
	}
	if confirmation.StoppedAt.Before(sw.UpdatedAt) {
		return false, fmt.Errorf("confirm source stopped for agent switch %s: stopped timestamp precedes current switch update", confirmation.SwitchID)
	}

	n, err := q.MarkSessionAgentSwitchSourceStopped(ctx, gen.MarkSessionAgentSwitchSourceStoppedParams{
		StoppedAt: confirmation.StoppedAt, SessionID: confirmation.SessionID,
		ExpectedSourceHarness:         confirmation.SourceHarness,
		ExpectedSourceRuntimeLaunchID: confirmation.ExpectedSourceRuntimeLaunchID,
	})
	if err != nil {
		return false, fmt.Errorf("confirm source stopped for agent switch %s: mark session exited: %w", confirmation.SwitchID, err)
	}
	if n != 1 {
		return false, nil
	}
	n, err = q.MarkAgentSwitchSourceStopped(ctx, gen.MarkAgentSwitchSourceStoppedParams{
		StoppedAt: confirmation.StoppedAt, ID: confirmation.SwitchID,
		SessionID: confirmation.SessionID, ExpectedSourceHarness: confirmation.SourceHarness,
		ExpectedSourceGenerationID: confirmation.SourceGenerationID,
		ExpectedTargetGenerationID: confirmation.TargetGenerationID,
	})
	if err != nil {
		return false, fmt.Errorf("confirm source stopped for agent switch %s: advance switch: %w", confirmation.SwitchID, err)
	}
	if n != 1 {
		return false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("confirm source stopped for agent switch %s: commit: %w", confirmation.SwitchID, err)
	}
	return true, nil
}

// AcknowledgeAgentSwitchTarget records exactly one acknowledgement from the
// target generation while context delivery is open. The generation and state
// predicates reject a late callback from an abandoned target, and the
// acknowledgement timestamp is immutable once set.
func (s *Store) AcknowledgeAgentSwitchTarget(ctx context.Context, id domain.AgentSwitchID, sessionID domain.SessionID, targetGenerationID domain.AgentGenerationID, acknowledgedAt time.Time) (bool, error) {
	if id == "" || sessionID == "" || targetGenerationID == "" || acknowledgedAt.IsZero() {
		return false, fmt.Errorf("acknowledge agent switch target %s: switch, session, target generation, and timestamp are required", id)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.AcknowledgeAgentSwitchTarget(ctx, gen.AcknowledgeAgentSwitchTargetParams{
		TargetAcknowledgedAt: sql.NullTime{Time: acknowledgedAt, Valid: true},
		UpdatedAt:            acknowledgedAt, ID: id, SessionID: sessionID,
		TargetGenerationID: targetGenerationID,
	})
	if err != nil {
		return false, fmt.Errorf("acknowledge agent switch target %s: %w", id, err)
	}
	return n > 0, nil
}

// ActivateAgentSwitchTarget atomically transfers the sessions row to a
// validated native target and advances starting_target -> target_ready. Only
// owner/activity fields are updated; task, workspace, prompts, lifecycle
// policy, cleanup generation, and termination facts remain untouched. The
// is_terminated/source-owner predicates make a concurrent kill or replacement
// win instead of being resurrected by a stale activation.
func (s *Store) ActivateAgentSwitchTarget(ctx context.Context, activation domain.AgentSwitchTargetActivation) (bool, error) {
	if err := validateAgentSwitchTargetActivation(activation); err != nil {
		return false, err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin activate agent switch target %s: %w", activation.SwitchID, err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.qw.WithTx(tx)

	switchRow, err := q.GetAgentSwitch(ctx, activation.SwitchID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("activate agent switch target %s: read switch: %w", activation.SwitchID, err)
	}
	sw := agentSwitchFromGen(switchRow)
	if sw.SessionID != activation.SessionID || sw.State != domain.AgentSwitchStartingTarget ||
		sw.FromHarness != activation.SourceHarness || sw.TargetHarness != activation.TargetHarness ||
		sw.SourceGenerationID != activation.SourceGenerationID || sw.TargetGenerationID != activation.TargetGenerationID ||
		(sw.TargetRuntimeHandleID != "" && sw.TargetRuntimeHandleID != activation.RuntimeHandleID) ||
		sw.TargetNativeSessionRef == nil || *sw.TargetNativeSessionRef != activation.TargetNativeSessionRef ||
		sw.TargetAcknowledgedAt != nil {
		return false, nil
	}
	if activation.ActivatedAt.Before(sw.UpdatedAt) {
		return false, fmt.Errorf("activate agent switch target %s: activation timestamp precedes current switch update", activation.SwitchID)
	}

	nativeRow, err := q.GetAgentNativeSession(ctx, activation.TargetNativeSessionRef)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("activate agent switch target %s: target native session %s does not exist", activation.SwitchID, activation.TargetNativeSessionRef)
	}
	if err != nil {
		return false, fmt.Errorf("activate agent switch target %s: read target native session %s: %w", activation.SwitchID, activation.TargetNativeSessionRef, err)
	}
	targetNative := agentNativeSessionFromGen(nativeRow)
	if targetNative.AOSessionID != activation.SessionID || targetNative.Harness != activation.TargetHarness {
		return false, fmt.Errorf("activate agent switch target %s: target native session %s has mismatched AO session or harness", activation.SwitchID, activation.TargetNativeSessionRef)
	}
	if targetNative.LastGenerationID != activation.TargetGenerationID {
		return false, nil
	}
	if strings.TrimSpace(targetNative.NativeSessionID) == "" {
		return false, fmt.Errorf("activate agent switch target %s: target native session %s has no provider-native identity", activation.SwitchID, activation.TargetNativeSessionRef)
	}

	n, err := q.ActivateSessionAgentSwitchTarget(ctx, gen.ActivateSessionAgentSwitchTargetParams{
		TargetHarness: activation.TargetHarness, ActivatedAt: activation.ActivatedAt,
		RuntimeHandleID:            activation.RuntimeHandleID,
		TargetGenerationID:         string(activation.TargetGenerationID),
		TargetNativeSessionID:      targetNative.NativeSessionID,
		TargetNativeTranscriptPath: targetNative.TranscriptPath,
		SessionID:                  activation.SessionID, ExpectedSourceHarness: activation.SourceHarness,
		ExpectedSourceRuntimeLaunchID: activation.ExpectedSourceRuntimeLaunchID,
	})
	if err != nil {
		return false, fmt.Errorf("activate agent switch target %s: transfer session owner: %w", activation.SwitchID, err)
	}
	if n != 1 {
		return false, nil
	}
	if err := q.ResetConversationAgentOverridesForSession(ctx, gen.ResetConversationAgentOverridesForSessionParams{
		UpdatedAt: activation.ActivatedAt, CurrentSessionID: &activation.SessionID,
	}); err != nil {
		return false, fmt.Errorf("activate agent switch target %s: reset conversation agent overrides: %w", activation.SwitchID, err)
	}
	targetRef := activation.TargetNativeSessionRef
	n, err = q.MarkAgentSwitchTargetReady(ctx, gen.MarkAgentSwitchTargetReadyParams{
		ActivatedAt: activation.ActivatedAt, ID: activation.SwitchID,
		SessionID: activation.SessionID, ExpectedSourceHarness: activation.SourceHarness,
		ExpectedTargetHarness:          activation.TargetHarness,
		ExpectedSourceGenerationID:     activation.SourceGenerationID,
		ExpectedTargetGenerationID:     activation.TargetGenerationID,
		ExpectedTargetNativeSessionRef: &targetRef,
		ExpectedTargetRuntimeHandleID:  activation.RuntimeHandleID,
	})
	if err != nil {
		return false, fmt.Errorf("activate agent switch target %s: advance switch: %w", activation.SwitchID, err)
	}
	if n != 1 {
		return false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("activate agent switch target %s: commit: %w", activation.SwitchID, err)
	}
	return true, nil
}

func validateAgentNativeSession(rec domain.AgentNativeSession) error {
	if rec.ID == "" || rec.AOSessionID == "" {
		return errors.New("agent native session id and AO session id are required")
	}
	if !rec.Harness.IsKnown() {
		return fmt.Errorf("agent native session %s: unknown harness %q", rec.ID, rec.Harness)
	}
	if rec.CreatedAt.IsZero() || rec.LastUsedAt.IsZero() {
		return fmt.Errorf("agent native session %s: created and last-used timestamps are required", rec.ID)
	}
	if rec.LastUsedAt.Before(rec.CreatedAt) {
		return fmt.Errorf("agent native session %s: timestamps precede creation", rec.ID)
	}
	return nil
}

func validateAgentSwitch(rec domain.AgentSwitch, create bool) error {
	if rec.ID == "" || rec.SessionID == "" || strings.TrimSpace(rec.IdempotencyKey) == "" {
		return errors.New("agent switch id, session id, and idempotency key are required")
	}
	if !rec.RequestFingerprint.Valid() {
		return fmt.Errorf("agent switch %s: a valid request fingerprint is required", rec.ID)
	}
	if !rec.FromHarness.IsKnown() || !rec.TargetHarness.IsKnown() || rec.FromHarness == rec.TargetHarness {
		return fmt.Errorf("agent switch %s: source and distinct known target harnesses are required", rec.ID)
	}
	if !rec.State.Valid() || !rec.TargetStartMode.Valid() || !rec.AgentHandoffStatus.Valid() || !rec.SourceTranscriptStatus.Valid() {
		return fmt.Errorf("agent switch %s: invalid state, target start mode, handoff status, or transcript status", rec.ID)
	}
	if !rec.ErrorCode.Valid() {
		return fmt.Errorf("agent switch %s: invalid error code %q", rec.ID, rec.ErrorCode)
	}
	recoveryRequired := rec.RequiresRecovery()
	recoveryMarker := rec.ErrorCode == domain.AgentSwitchErrorTargetStartUnconfirmed ||
		rec.ErrorCode == domain.AgentSwitchErrorSourceRestoreUnconfirmed
	failureCode := rec.State == domain.AgentSwitchFailed && rec.ErrorCode != "" && !recoveryMarker
	if (rec.State == domain.AgentSwitchFailed) != failureCode || (rec.ErrorCode != "" && !failureCode && !recoveryRequired) {
		return fmt.Errorf("agent switch %s: error code must describe a terminal failure or an exact recovery condition", rec.ID)
	}
	if rec.SourceGenerationID == "" {
		return fmt.Errorf("agent switch %s: source generation is required", rec.ID)
	}
	if rec.TargetRuntimeHandleID != "" {
		if strings.TrimSpace(rec.TargetRuntimeHandleID) != rec.TargetRuntimeHandleID || rec.TargetGenerationID == "" {
			return fmt.Errorf("agent switch %s: target runtime handle requires a target generation and cannot have surrounding whitespace", rec.ID)
		}
	}
	if err := validateAgentHandoffReference(rec.AgentHandoffStatus, rec.AgentHandoffPath, rec.AgentHandoffHash); err != nil {
		return fmt.Errorf("agent switch %s: %w", rec.ID, err)
	}
	if rec.FinalHandoffPath != "" || rec.FinalHandoffHash != "" {
		if err := validateFinalHandoffReference(rec.FinalHandoffPath, rec.FinalHandoffHash); err != nil {
			return fmt.Errorf("agent switch %s: %w", rec.ID, err)
		}
	}
	if rec.SemanticHandoffIncluded &&
		(rec.AgentHandoffStatus != domain.AgentHandoffReceived || rec.FinalHandoffPath == "" ||
			rec.AgentHandoffPath != rec.FinalHandoffPath || rec.AgentHandoffHash != rec.FinalHandoffHash) {
		return fmt.Errorf("agent switch %s: included semantic handoff must be a received report bound to the finalized artifact", rec.ID)
	}
	if rec.RequestedAt.IsZero() || rec.UpdatedAt.IsZero() || rec.UpdatedAt.Before(rec.RequestedAt) {
		return fmt.Errorf("agent switch %s: invalid requested or updated timestamp", rec.ID)
	}
	if rec.TargetAcknowledgedAt != nil {
		if rec.TargetGenerationID == "" || rec.TargetAcknowledgedAt.Before(rec.RequestedAt) || rec.TargetAcknowledgedAt.After(rec.UpdatedAt) {
			return fmt.Errorf("agent switch %s: target acknowledgement requires a target generation and a timestamp within the switch lifetime", rec.ID)
		}
	}
	if create {
		if rec.State != domain.AgentSwitchPreparingHandoff || rec.TargetNativeSessionRef != nil ||
			rec.TargetStartMode != domain.AgentSwitchTargetStartPending || rec.TargetGenerationID != "" ||
			rec.TargetRuntimeHandleID != "" || rec.TargetAcknowledgedAt != nil ||
			rec.AgentHandoffStatus != domain.AgentHandoffNotAttempted ||
			rec.SemanticHandoffIncluded ||
			rec.AgentHandoffPath != "" || rec.AgentHandoffHash != "" ||
			rec.FinalHandoffPath != "" || rec.FinalHandoffHash != "" ||
			(rec.SourceTranscriptStatus != "" && rec.SourceTranscriptStatus != domain.AgentSwitchSourceTranscriptNotAttempted) {
			return fmt.Errorf("agent switch %s: a new saga must be preparing handoff without target launch or handoff facts", rec.ID)
		}
	}
	return nil
}

func validateAgentSwitchSourceStopConfirmation(confirmation domain.AgentSwitchSourceStopConfirmation) error {
	if confirmation.SwitchID == "" || confirmation.SessionID == "" ||
		confirmation.SourceGenerationID == "" || confirmation.TargetGenerationID == "" || confirmation.StoppedAt.IsZero() {
		return fmt.Errorf("confirm agent switch source stopped %s: switch, session, source/target generations, and timestamp are required", confirmation.SwitchID)
	}
	if !confirmation.SourceHarness.IsKnown() {
		return fmt.Errorf("confirm agent switch source stopped %s: unknown source harness %q", confirmation.SwitchID, confirmation.SourceHarness)
	}
	return nil
}

func validateAgentSwitchTargetActivation(activation domain.AgentSwitchTargetActivation) error {
	if activation.SwitchID == "" || activation.SessionID == "" || activation.SourceGenerationID == "" ||
		activation.TargetNativeSessionRef == "" || activation.TargetGenerationID == "" ||
		strings.TrimSpace(activation.RuntimeHandleID) == "" || activation.ActivatedAt.IsZero() {
		return fmt.Errorf("activate agent switch target %s: switch, session, source/target generations, native target, runtime handle, and timestamp are required", activation.SwitchID)
	}
	if !activation.SourceHarness.IsKnown() || !activation.TargetHarness.IsKnown() || activation.SourceHarness == activation.TargetHarness {
		return fmt.Errorf("activate agent switch target %s: source and distinct known target harnesses are required", activation.SwitchID)
	}
	return nil
}

func validateAgentHandoffReference(status domain.AgentHandoffStatus, path, hash string) error {
	if status == domain.AgentHandoffReceived {
		if strings.TrimSpace(path) == "" {
			return errors.New("received agent handoff requires a nonempty path")
		}
		if path != strings.TrimSpace(path) || hash != strings.TrimSpace(hash) {
			return errors.New("agent handoff path and hash cannot have surrounding whitespace")
		}
		if !validSHA256Hex(hash) {
			return errors.New("received agent handoff hash must be exactly 64 lowercase hexadecimal characters")
		}
		return nil
	}
	if path != "" || hash != "" {
		return errors.New("agent handoff path and hash require received status")
	}
	return nil
}

func validateFinalHandoffReference(path, hash string) error {
	if strings.TrimSpace(path) == "" || path != strings.TrimSpace(path) {
		return errors.New("finalized handoff requires a nonempty path without surrounding whitespace")
	}
	if hash != strings.TrimSpace(hash) || !validSHA256Hex(hash) {
		return errors.New("finalized handoff hash must be exactly 64 lowercase hexadecimal characters")
	}
	return nil
}

func validSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, ch := range value {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

func agentNativeSessionToInsert(rec domain.AgentNativeSession) gen.InsertAgentNativeSessionParams {
	return gen.InsertAgentNativeSessionParams{
		ID: rec.ID, AoSessionID: rec.AOSessionID, Harness: rec.Harness, ConfigDir: rec.ConfigDir,
		NativeSessionID: rec.NativeSessionID, TranscriptPath: rec.TranscriptPath,
		LastGenerationID: rec.LastGenerationID, CreatedAt: rec.CreatedAt,
		LastUsedAt: rec.LastUsedAt,
	}
}

func agentNativeSessionFromGen(row gen.AgentNativeSession) domain.AgentNativeSession {
	return domain.AgentNativeSession{
		ID: row.ID, AOSessionID: row.AoSessionID, Harness: row.Harness, ConfigDir: row.ConfigDir,
		NativeSessionID: row.NativeSessionID, TranscriptPath: row.TranscriptPath,
		LastGenerationID: row.LastGenerationID, CreatedAt: row.CreatedAt,
		LastUsedAt: row.LastUsedAt,
	}
}

func sameAgentNativeCreateIdentity(a, b domain.AgentNativeSession) bool {
	if a.ID != b.ID || a.AOSessionID != b.AOSessionID || a.Harness != b.Harness ||
		a.ConfigDir != b.ConfigDir {
		return false
	}
	return a.NativeSessionID == b.NativeSessionID || a.NativeSessionID == "" || b.NativeSessionID == ""
}

func agentSwitchToInsert(rec domain.AgentSwitch) gen.InsertAgentSwitchParams {
	return gen.InsertAgentSwitchParams{
		ID: rec.ID, SessionID: rec.SessionID, IdempotencyKey: rec.IdempotencyKey,
		RequestFingerprint: rec.RequestFingerprint,
		FromHarness:        rec.FromHarness, TargetHarness: rec.TargetHarness,
		TargetNativeSessionRef: rec.TargetNativeSessionRef,
		TargetStartMode:        rec.TargetStartMode, State: rec.State,
		AgentHandoffStatus:      rec.AgentHandoffStatus,
		SemanticHandoffIncluded: rec.SemanticHandoffIncluded,
		AgentHandoffPath:        rec.AgentHandoffPath,
		AgentHandoffHash:        rec.AgentHandoffHash,
		SourceTranscriptStatus:  rec.SourceTranscriptStatus,
		FinalHandoffPath:        rec.FinalHandoffPath,
		FinalHandoffHash:        rec.FinalHandoffHash,
		SourceGenerationID:      rec.SourceGenerationID,
		TargetGenerationID:      rec.TargetGenerationID,
		TargetRuntimeHandleID:   rec.TargetRuntimeHandleID,
		TargetAcknowledgedAt:    timePtrToNull(rec.TargetAcknowledgedAt),
		ErrorCode:               string(rec.ErrorCode),
		RequestedAt:             rec.RequestedAt, UpdatedAt: rec.UpdatedAt,
	}
}

func agentSwitchFromGen(row gen.AgentSwitch) domain.AgentSwitch {
	return domain.AgentSwitch{
		ID: row.ID, SessionID: row.SessionID, IdempotencyKey: row.IdempotencyKey,
		RequestFingerprint: row.RequestFingerprint,
		FromHarness:        row.FromHarness, TargetHarness: row.TargetHarness,
		TargetNativeSessionRef: cloneNativeSessionID(row.TargetNativeSessionRef),
		TargetStartMode:        row.TargetStartMode, State: row.State,
		AgentHandoffStatus:      row.AgentHandoffStatus,
		SemanticHandoffIncluded: row.SemanticHandoffIncluded,
		AgentHandoffPath:        row.AgentHandoffPath,
		AgentHandoffHash:        row.AgentHandoffHash,
		SourceTranscriptStatus:  row.SourceTranscriptStatus,
		FinalHandoffPath:        row.FinalHandoffPath,
		FinalHandoffHash:        row.FinalHandoffHash,
		SourceGenerationID:      row.SourceGenerationID,
		TargetGenerationID:      row.TargetGenerationID,
		TargetRuntimeHandleID:   row.TargetRuntimeHandleID,
		TargetAcknowledgedAt:    nullTimeToPtr(row.TargetAcknowledgedAt),
		ErrorCode:               domain.AgentSwitchErrorCode(row.ErrorCode),
		RequestedAt:             row.RequestedAt, UpdatedAt: row.UpdatedAt,
	}
}

func ensureNativeSessionRefBelongsTo(ctx context.Context, q *gen.Queries, sessionID domain.SessionID, harness domain.AgentHarness, ref *domain.AgentNativeSessionID, role string) error {
	if ref == nil {
		return nil
	}
	row, err := q.GetAgentNativeSession(ctx, *ref)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s native session %s does not exist", role, *ref)
	}
	if err != nil {
		return fmt.Errorf("read %s native session %s: %w", role, *ref, err)
	}
	if row.AoSessionID != sessionID {
		return fmt.Errorf("%s native session %s belongs to AO session %s, not %s", role, *ref, row.AoSessionID, sessionID)
	}
	if row.Harness != harness {
		return fmt.Errorf("%s native session %s belongs to harness %s, not %s", role, *ref, row.Harness, harness)
	}
	return nil
}

func cloneNativeSessionID(id *domain.AgentNativeSessionID) *domain.AgentNativeSessionID {
	if id == nil {
		return nil
	}
	copyID := *id
	return &copyID
}

func timePtrToNull(value *time.Time) sql.NullTime {
	if value == nil || value.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *value, Valid: true}
}

func nullTimeToPtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	copyTime := value.Time
	return &copyTime
}
