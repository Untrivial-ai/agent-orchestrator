package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// ReserveTaskDelegation creates the durable fence for one local delegation.
// A retry with the same fingerprint receives the existing record.
func (s *Store) ReserveTaskDelegation(ctx context.Context, rec domain.TaskDelegation) (domain.TaskDelegation, bool, error) {
	if err := validateTaskDelegationReservation(rec); err != nil {
		return domain.TaskDelegation{}, false, err
	}
	if err := s.writeMu.LockContext(ctx); err != nil {
		return domain.TaskDelegation{}, false, err
	}
	defer s.writeMu.Unlock()

	n, err := s.qw.InsertTaskDelegation(ctx, gen.InsertTaskDelegationParams{
		IdempotencyKey:     rec.IdempotencyKey,
		RequestFingerprint: string(rec.RequestFingerprint),
		CreatedAt:          rec.CreatedAt,
		UpdatedAt:          rec.UpdatedAt,
	})
	if err != nil {
		return domain.TaskDelegation{}, false, fmt.Errorf("reserve task delegation: %w", err)
	}
	if n > 0 {
		rec.StartupState = domain.TaskDelegationStartupSeeded
		return rec, true, nil
	}

	row, err := s.qw.GetTaskDelegation(ctx, rec.IdempotencyKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.TaskDelegation{}, false, fmt.Errorf("reserve task delegation: conflict row was not found")
		}
		return domain.TaskDelegation{}, false, fmt.Errorf("read task delegation: %w", err)
	}
	existing := taskDelegationFromGen(row)
	if existing.RequestFingerprint != rec.RequestFingerprint {
		return existing, false, fmt.Errorf("reserve task delegation: %w", domain.ErrTaskDelegationIdempotencyConflict)
	}
	if row.State == string(domain.TaskDelegationPending) && row.Recoverable == 0 {
		return existing, false, domain.ErrTaskDelegationRecoveryRequired
	}
	return existing, false, nil
}

// CompleteTaskDelegation records the worker created by the reserved request.
func (s *Store) CompleteTaskDelegation(
	ctx context.Context,
	idempotencyKey string,
	fingerprint domain.TaskDelegationRequestFingerprint,
	workerID domain.SessionID,
	updatedAt time.Time,
) (domain.TaskDelegation, error) {
	if strings.TrimSpace(idempotencyKey) == "" || !fingerprint.Valid() || workerID == "" {
		return domain.TaskDelegation{}, fmt.Errorf("complete task delegation: invalid identity")
	}
	if err := s.writeMu.LockContext(ctx); err != nil {
		return domain.TaskDelegation{}, err
	}
	defer s.writeMu.Unlock()

	n, err := s.qw.CompleteTaskDelegation(ctx, gen.CompleteTaskDelegationParams{
		WorkerID:           &workerID,
		UpdatedAt:          updatedAt,
		IdempotencyKey:     idempotencyKey,
		RequestFingerprint: string(fingerprint),
	})
	if err != nil {
		return domain.TaskDelegation{}, fmt.Errorf("complete task delegation: %w", err)
	}
	row, err := s.qw.GetTaskDelegation(ctx, idempotencyKey)
	if err != nil {
		return domain.TaskDelegation{}, fmt.Errorf("read completed task delegation: %w", err)
	}
	rec := taskDelegationFromGen(row)
	if rec.RequestFingerprint != fingerprint || (rec.State == domain.TaskDelegationCompleted && rec.WorkerID != workerID) {
		return rec, fmt.Errorf("complete task delegation: %w", domain.ErrTaskDelegationIdempotencyConflict)
	}
	if n == 0 && !rec.Ready() {
		return rec, fmt.Errorf("complete task delegation: %w", domain.ErrTaskDelegationInProgress)
	}
	return rec, nil
}

func validateTaskDelegationReservation(rec domain.TaskDelegation) error {
	if strings.TrimSpace(rec.IdempotencyKey) == "" || len(rec.IdempotencyKey) > 128 {
		return fmt.Errorf("reserve task delegation: invalid idempotency key")
	}
	if !rec.RequestFingerprint.Valid() {
		return fmt.Errorf("reserve task delegation: invalid request fingerprint")
	}
	if rec.State != domain.TaskDelegationPending || rec.WorkerID != "" {
		return fmt.Errorf("reserve task delegation: invalid initial state")
	}
	if rec.CreatedAt.IsZero() || rec.UpdatedAt.Before(rec.CreatedAt) {
		return fmt.Errorf("reserve task delegation: invalid timestamps")
	}
	return nil
}

func taskDelegationFromGen(row gen.TaskDelegation) domain.TaskDelegation {
	rec := domain.TaskDelegation{
		IdempotencyKey:     row.IdempotencyKey,
		RequestFingerprint: domain.TaskDelegationRequestFingerprint(row.RequestFingerprint),
		State:              domain.TaskDelegationState(row.State),
		StartupState:       domain.TaskDelegationStartupState(row.StartupState),
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}
	if row.WorkerID != nil {
		rec.WorkerID = *row.WorkerID
	}
	return rec
}

// CreateTaskDelegationSession commits the spawn identity before runtime side effects.
func (s *Store) CreateTaskDelegationSession(ctx context.Context, seed domain.SessionRecord, key string, fingerprint domain.TaskDelegationRequestFingerprint) (domain.SessionRecord, bool, error) {
	if err := s.writeMu.LockContext(ctx); err != nil {
		return domain.SessionRecord{}, false, err
	}
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return domain.SessionRecord{}, false, err
	}
	defer tx.Rollback()
	q := s.qw.WithTx(tx)
	reservation, err := q.GetTaskDelegation(ctx, key)
	if err != nil {
		return domain.SessionRecord{}, false, fmt.Errorf("load spawn reservation: %w", err)
	}
	if reservation.RequestFingerprint != string(fingerprint) {
		return domain.SessionRecord{}, false, domain.ErrTaskDelegationIdempotencyConflict
	}
	if reservation.WorkerID != nil {
		row, err := q.GetSession(ctx, *reservation.WorkerID)
		if err != nil {
			return domain.SessionRecord{}, false, fmt.Errorf("load reserved worker: %w", err)
		}
		return rowToRecord(row), false, nil
	}
	if reservation.Recoverable == 0 {
		return domain.SessionRecord{}, false, domain.ErrTaskDelegationRecoveryRequired
	}
	rec, err := createSession(ctx, q, seed)
	if err != nil {
		return domain.SessionRecord{}, false, err
	}
	count, err := q.BindTaskDelegationWorker(ctx, gen.BindTaskDelegationWorkerParams{
		WorkerID: &rec.ID, UpdatedAt: rec.UpdatedAt, IdempotencyKey: key, RequestFingerprint: string(fingerprint),
	})
	if err != nil {
		return domain.SessionRecord{}, false, fmt.Errorf("bind reserved worker: %w", err)
	}
	if count != 1 {
		return domain.SessionRecord{}, false, domain.ErrTaskDelegationInProgress
	}
	if err := tx.Commit(); err != nil {
		return domain.SessionRecord{}, false, fmt.Errorf("commit reserved worker: %w", err)
	}
	return rec, true, nil
}

func (s *Store) ClaimTaskDelegationStartup(ctx context.Context, key string, fingerprint domain.TaskDelegationRequestFingerprint, workerID domain.SessionID) (bool, error) {
	if err := s.writeMu.LockContext(ctx); err != nil {
		return false, err
	}
	defer s.writeMu.Unlock()
	n, err := s.qw.ClaimTaskDelegationStartup(ctx, gen.ClaimTaskDelegationStartupParams{
		IdempotencyKey: key, RequestFingerprint: string(fingerprint), WorkerID: &workerID,
	})
	if err != nil || n == 1 {
		return n == 1, err
	}
	row, err := s.qw.GetTaskDelegation(ctx, key)
	if err != nil {
		return false, fmt.Errorf("read startup claim: %w", err)
	}
	rec := taskDelegationFromGen(row)
	if rec.RequestFingerprint != fingerprint || rec.WorkerID != workerID {
		return false, domain.ErrTaskDelegationIdempotencyConflict
	}
	if rec.Ready() {
		return false, nil
	}
	return false, domain.ErrTaskDelegationRecoveryRequired
}

func (s *Store) TaskDelegationStartupForWorker(ctx context.Context, id domain.SessionID) (domain.TaskDelegationStartupState, error) {
	state, err := s.qr.TaskDelegationStartupForWorker(ctx, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read worker startup checkpoint: %w", err)
	}
	return domain.TaskDelegationStartupState(state), nil
}
