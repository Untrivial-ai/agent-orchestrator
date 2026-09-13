package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// CreateSessionInterfaceTransition claims the one active transition slot for a
// session. created=false returns the winner of a concurrent request rather than
// leaking a SQLite constraint as an internal error.
func (s *Store) CreateSessionInterfaceTransition(
	ctx context.Context,
	rec domain.SessionInterfaceTransition,
) (domain.SessionInterfaceTransition, bool, error) {
	if !rec.HistoryPolicy.Valid() {
		return domain.SessionInterfaceTransition{}, false,
			fmt.Errorf("create interface transition: history policy %q is invalid", rec.HistoryPolicy)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	row, err := s.qw.InsertSessionInterfaceTransition(ctx, gen.InsertSessionInterfaceTransitionParams{
		ID:                   rec.ID,
		SessionID:            rec.SessionID,
		SourceMode:           rec.SourceMode,
		TargetMode:           rec.TargetMode,
		Policy:               rec.Policy,
		HistoryPolicy:        rec.HistoryPolicy,
		Phase:                rec.Phase,
		NativeConversationID: rec.NativeConversationID,
		CreatedAt:            rec.CreatedAt,
		UpdatedAt:            rec.UpdatedAt,
	})
	if err == nil {
		return interfaceTransitionToDomain(row), true, nil
	}
	if !isSQLiteUnique(err) {
		return domain.SessionInterfaceTransition{}, false, fmt.Errorf("create interface transition for %s: %w", rec.SessionID, err)
	}
	existing, lookupErr := s.qw.GetActiveSessionInterfaceTransition(ctx, rec.SessionID)
	if lookupErr != nil {
		return domain.SessionInterfaceTransition{}, false, fmt.Errorf("read winning interface transition for %s: %w", rec.SessionID, lookupErr)
	}
	return interfaceTransitionToDomain(existing), false, nil
}

// CreateSessionInterfaceTransitionAfter claims a provider-history recovery
// only while expectedPredecessorID is still the exact latest saga. The read and
// insert share the writer transaction and lock, closing the consent TOCTOU gap.
func (s *Store) CreateSessionInterfaceTransitionAfter(
	ctx context.Context,
	rec domain.SessionInterfaceTransition,
	expectedPredecessorID string,
) (transition domain.SessionInterfaceTransition, created, predecessorMatched bool, err error) {
	if !rec.HistoryPolicy.Valid() {
		return domain.SessionInterfaceTransition{}, false, false,
			fmt.Errorf("create interface transition: history policy %q is invalid", rec.HistoryPolicy)
	}
	if expectedPredecessorID == "" {
		return domain.SessionInterfaceTransition{}, false, false,
			fmt.Errorf("create interface transition: expected predecessor is empty")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	err = s.inTx(ctx, "create interface transition after predecessor", func(q *gen.Queries) error {
		latest, lookupErr := q.GetLatestSessionInterfaceTransition(ctx, rec.SessionID)
		if errors.Is(lookupErr, sql.ErrNoRows) {
			return nil
		}
		if lookupErr != nil {
			return fmt.Errorf("read latest interface transition for %s: %w", rec.SessionID, lookupErr)
		}
		if latest.ID != expectedPredecessorID {
			return nil
		}
		predecessorMatched = true
		row, insertErr := q.InsertSessionInterfaceTransition(ctx, gen.InsertSessionInterfaceTransitionParams{
			ID: rec.ID, SessionID: rec.SessionID, SourceMode: rec.SourceMode,
			TargetMode: rec.TargetMode, Policy: rec.Policy, HistoryPolicy: rec.HistoryPolicy,
			Phase: rec.Phase, NativeConversationID: rec.NativeConversationID,
			CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt,
		})
		if insertErr == nil {
			transition, created = interfaceTransitionToDomain(row), true
			return nil
		}
		if !isSQLiteUnique(insertErr) {
			return fmt.Errorf("create interface transition for %s: %w", rec.SessionID, insertErr)
		}
		existing, activeErr := q.GetActiveSessionInterfaceTransition(ctx, rec.SessionID)
		if activeErr != nil {
			return fmt.Errorf("read winning interface transition for %s: %w", rec.SessionID, activeErr)
		}
		transition = interfaceTransitionToDomain(existing)
		return nil
	})
	return transition, created, predecessorMatched, err
}

// GetSessionInterfaceTransition reads one interface transition by id.
func (s *Store) GetSessionInterfaceTransition(
	ctx context.Context,
	id string,
) (domain.SessionInterfaceTransition, bool, error) {
	row, err := s.qr.GetSessionInterfaceTransition(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SessionInterfaceTransition{}, false, nil
	}
	if err != nil {
		return domain.SessionInterfaceTransition{}, false, fmt.Errorf("get interface transition %s: %w", id, err)
	}
	return interfaceTransitionToDomain(row), true, nil
}

// GetActiveSessionInterfaceTransition reads the active transition for a session.
func (s *Store) GetActiveSessionInterfaceTransition(
	ctx context.Context,
	id domain.SessionID,
) (domain.SessionInterfaceTransition, bool, error) {
	row, err := s.qr.GetActiveSessionInterfaceTransition(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SessionInterfaceTransition{}, false, nil
	}
	if err != nil {
		return domain.SessionInterfaceTransition{}, false, fmt.Errorf("get active interface transition for %s: %w", id, err)
	}
	return interfaceTransitionToDomain(row), true, nil
}

// GetLatestSessionInterfaceTransition reads the newest transition for a session.
func (s *Store) GetLatestSessionInterfaceTransition(
	ctx context.Context,
	id domain.SessionID,
) (domain.SessionInterfaceTransition, bool, error) {
	row, err := s.qr.GetLatestSessionInterfaceTransition(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SessionInterfaceTransition{}, false, nil
	}
	if err != nil {
		return domain.SessionInterfaceTransition{}, false, fmt.Errorf("get latest interface transition for %s: %w", id, err)
	}
	return interfaceTransitionToDomain(row), true, nil
}

// ListActiveSessionInterfaceTransitions lists transitions that have not reached a terminal phase.
func (s *Store) ListActiveSessionInterfaceTransitions(ctx context.Context) ([]domain.SessionInterfaceTransition, error) {
	rows, err := s.qr.ListActiveSessionInterfaceTransitions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active interface transitions: %w", err)
	}
	out := make([]domain.SessionInterfaceTransition, 0, len(rows))
	for _, row := range rows {
		out = append(out, interfaceTransitionToDomain(row))
	}
	return out, nil
}

// ListDeliverableSessionInterfaceTransitions returns terminal handoffs whose
// outbox still owns at least one message. Terminal rows are deliberately kept:
// they are the durable retry anchor after rollback, cancellation, or restart.
func (s *Store) ListDeliverableSessionInterfaceTransitions(ctx context.Context) ([]domain.SessionInterfaceTransition, error) {
	rows, err := s.qr.ListDeliverableSessionInterfaceTransitions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list deliverable interface transitions: %w", err)
	}
	out := make([]domain.SessionInterfaceTransition, 0, len(rows))
	for _, row := range rows {
		out = append(out, interfaceTransitionToDomain(row))
	}
	return out, nil
}

// AdvanceSessionInterfaceTransition compare-and-swaps one phase. A false result
// means another request (usually cancellation) won the race.
func (s *Store) AdvanceSessionInterfaceTransition(
	ctx context.Context,
	id string,
	expected, next domain.SessionInterfaceTransitionPhase,
	nativeID, errorCode, errorDetail string,
	now time.Time,
) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var completed sql.NullTime
	if next.Terminal() {
		completed = sql.NullTime{Time: now, Valid: true}
	}
	rows, err := s.qw.AdvanceSessionInterfaceTransition(ctx, gen.AdvanceSessionInterfaceTransitionParams{
		Phase:                next,
		NativeConversationID: nativeID,
		ErrorCode:            errorCode,
		ErrorDetail:          errorDetail,
		UpdatedAt:            now,
		CompletedAt:          completed,
		ID:                   id,
		Phase_2:              expected,
	})
	if err != nil {
		return false, fmt.Errorf("advance interface transition %s: %w", id, err)
	}
	return rows > 0, nil
}

// AcknowledgeSessionInterfaceTransitionNotice records that a user has seen a
// failed or recovered transition notice. The transition remains intact as an
// audit record, and COALESCE makes retries return the original timestamp.
func (s *Store) AcknowledgeSessionInterfaceTransitionNotice(
	ctx context.Context,
	sessionID domain.SessionID,
	transitionID string,
	now time.Time,
) (domain.SessionInterfaceTransition, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	row, err := s.qw.AcknowledgeSessionInterfaceTransitionNotice(
		ctx,
		gen.AcknowledgeSessionInterfaceTransitionNoticeParams{
			NoticeAcknowledgedAt: sql.NullTime{Time: now, Valid: true},
			ID:                   transitionID,
			SessionID:            sessionID,
		},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SessionInterfaceTransition{}, false, nil
	}
	if err != nil {
		return domain.SessionInterfaceTransition{}, false,
			fmt.Errorf("acknowledge interface transition notice %s: %w", transitionID, err)
	}
	return interfaceTransitionToDomain(row), true, nil
}

// CommitSessionControllerEpoch is the atomic persistence primitive used only by
// Lifecycle Manager. Keeping the CAS here prevents stale controller handoffs;
// keeping the decision in Lifecycle Manager preserves its ownership of session
// activity and controller facts.
func (s *Store) CommitSessionControllerEpoch(
	ctx context.Context,
	id domain.SessionID,
	source, target domain.SessionMode,
	nativeID string,
	now time.Time,
) (bool, error) {
	return s.commitSessionControllerEpoch(ctx, id, source, target, nativeID, false, now)
}

// RestoreSessionControllerEpoch atomically restores the source interface after
// a failed or interrupted handoff. Unlike an accepted Chat-to-TUI handoff, a
// rollback must retain the checkpoint that caused target replay to fail so an
// ordinary retry cannot silently bypass it.
func (s *Store) RestoreSessionControllerEpoch(
	ctx context.Context,
	id domain.SessionID,
	source, target domain.SessionMode,
	nativeID string,
	now time.Time,
) (bool, error) {
	return s.commitSessionControllerEpoch(ctx, id, source, target, nativeID, true, now)
}

func (s *Store) commitSessionControllerEpoch(
	ctx context.Context,
	id domain.SessionID,
	source, target domain.SessionMode,
	nativeID string,
	restore bool,
	now time.Time,
) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin controller epoch for %s: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.qw.WithTx(tx)
	var rows int64
	if restore {
		rows, err = q.RestoreSessionControllerEpoch(ctx, gen.RestoreSessionControllerEpochParams{
			TargetMode:             target,
			AgentSessionID:         nativeID,
			ProviderConversationID: nativeID,
			ActivityLastAt:         now,
			UpdatedAt:              now,
			ID:                     id,
			SourceMode:             source,
		})
	} else {
		rows, err = q.CommitSessionControllerEpoch(ctx, gen.CommitSessionControllerEpochParams{
			TargetMode:             target,
			AgentSessionID:         nativeID,
			ProviderConversationID: nativeID,
			ActivityLastAt:         now,
			UpdatedAt:              now,
			ID:                     id,
			SourceMode:             source,
		})
	}
	if err != nil {
		return false, fmt.Errorf("commit controller epoch for %s: %w", id, err)
	}
	if rows == 0 {
		return false, nil
	}
	if source == domain.SessionModeChat && target == domain.SessionModeTUI && nativeID == "" {
		// A fresh TUI will choose its own native identity. Release the unused
		// Chat reservation and event namespace in the same transaction, so a
		// later return can bind that identity without inventing inherited history.
		released, err := q.ReleaseUntouchedConversationProvider(ctx, gen.ReleaseUntouchedConversationProviderParams{
			SessionID:       sql.NullString{String: string(id), Valid: true},
			ProviderScopeID: uuid.NewString(),
		})
		if err != nil {
			return false, fmt.Errorf("release untouched Chat provider for %s: %w", id, err)
		}
		if released != 1 {
			return false, fmt.Errorf("release untouched Chat provider for %s: empty root proof changed", id)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit controller epoch transaction for %s: %w", id, err)
	}
	return true, nil
}

// EnqueueSessionInterfaceTransitionMessage queues a user message while an interface transition is active.
func (s *Store) EnqueueSessionInterfaceTransitionMessage(
	ctx context.Context,
	transitionID, clientMessageID, message string,
	now time.Time,
) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.qw.EnqueueSessionInterfaceTransitionMessage(ctx, gen.EnqueueSessionInterfaceTransitionMessageParams{
		TransitionID:    transitionID,
		ClientMessageID: clientMessageID,
		Message:         message,
		CreatedAt:       now,
	}); err != nil {
		return fmt.Errorf("queue interface transition message: %w", err)
	}
	return nil
}

// ListPendingSessionInterfaceTransitionMessages lists queued transition messages awaiting delivery.
func (s *Store) ListPendingSessionInterfaceTransitionMessages(
	ctx context.Context,
	transitionID string,
) ([]domain.SessionInterfaceTransitionMessage, error) {
	rows, err := s.qr.ListPendingSessionInterfaceTransitionMessages(ctx, transitionID)
	if err != nil {
		return nil, fmt.Errorf("list interface transition messages: %w", err)
	}
	out := make([]domain.SessionInterfaceTransitionMessage, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.SessionInterfaceTransitionMessage{
			ID: row.ID, TransitionID: row.TransitionID,
			ClientMessageID: row.ClientMessageID, Message: row.Message,
			CreatedAt: row.CreatedAt, DeliveredAt: nullTimeToTime(row.DeliveredAt),
		})
	}
	return out, nil
}

// MarkSessionInterfaceTransitionMessageDelivered records successful delivery of a queued transition message.
func (s *Store) MarkSessionInterfaceTransitionMessageDelivered(
	ctx context.Context,
	id int64,
	now time.Time,
) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.qw.MarkSessionInterfaceTransitionMessageDelivered(ctx, gen.MarkSessionInterfaceTransitionMessageDeliveredParams{
		DeliveredAt: sql.NullTime{Time: now, Valid: true},
		ID:          id,
	})
	if err != nil {
		return fmt.Errorf("mark interface transition message delivered: %w", err)
	}
	return nil
}

func interfaceTransitionToDomain(row gen.SessionInterfaceTransition) domain.SessionInterfaceTransition {
	return domain.SessionInterfaceTransition{
		ID: row.ID, SessionID: row.SessionID,
		SourceMode: row.SourceMode, TargetMode: row.TargetMode,
		Policy: row.Policy, HistoryPolicy: row.HistoryPolicy, Phase: row.Phase,
		NativeConversationID: row.NativeConversationID,
		ErrorCode:            row.ErrorCode, ErrorDetail: row.ErrorDetail,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		CompletedAt:          nullTimeToTime(row.CompletedAt),
		NoticeAcknowledgedAt: nullTimeToTime(row.NoticeAcknowledgedAt),
	}
}
