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
	if n == 0 && rec.State != domain.TaskDelegationCompleted {
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
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}
	if row.WorkerID != nil {
		rec.WorkerID = *row.WorkerID
	}
	return rec
}
