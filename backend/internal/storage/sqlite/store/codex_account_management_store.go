package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

var _ ports.CodexAccountSwitchStore = (*Store)(nil)

// GetCodexActiveAccount reads the singleton active-account pointer.
func (s *Store) GetCodexActiveAccount(ctx context.Context) (domain.CodexActiveAccount, bool, error) {
	row, err := s.qr.GetCodexActiveAccount(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CodexActiveAccount{}, false, nil
	}
	if err != nil {
		return domain.CodexActiveAccount{}, false, fmt.Errorf("get active Codex account: %w", err)
	}
	return domain.CodexActiveAccount{
		AccountID: row.AccountID, Revision: row.Revision,
		ActivatedAt: row.ActivatedAt, UpdatedAt: row.UpdatedAt,
	}, true, nil
}

// SetCodexActiveAccount atomically advances the active-account revision.
func (s *Store) SetCodexActiveAccount(ctx context.Context, accountID string, expectedRevision int64, at time.Time) (domain.CodexActiveAccount, error) {
	if expectedRevision < 0 || (accountID == "" && expectedRevision == 0) {
		return domain.CodexActiveAccount{}, ports.ErrCodexAccountRevisionConflict
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	var active domain.CodexActiveAccount
	err := s.inTx(ctx, "set active Codex account", func(q *gen.Queries) error {
		var (
			changed int64
			err     error
		)
		if expectedRevision == 0 {
			changed, err = q.InsertCodexActiveAccount(ctx, gen.InsertCodexActiveAccountParams{
				AccountID: accountID, ActivatedAt: at.UTC(), UpdatedAt: at.UTC(),
			})
		} else {
			changed, err = q.UpdateCodexActiveAccount(ctx, gen.UpdateCodexActiveAccountParams{
				AccountID: accountID, ActivatedAt: at.UTC(), UpdatedAt: at.UTC(), ExpectedRevision: expectedRevision,
			})
		}
		if err != nil {
			return err
		}
		if changed == 0 {
			return ports.ErrCodexAccountRevisionConflict
		}
		row, err := q.GetCodexActiveAccount(ctx)
		if err != nil {
			return fmt.Errorf("read activated Codex account: %w", err)
		}
		active = domain.CodexActiveAccount{
			AccountID: row.AccountID, Revision: row.Revision,
			ActivatedAt: row.ActivatedAt, UpdatedAt: row.UpdatedAt,
		}
		return nil
	})
	if err != nil {
		return domain.CodexActiveAccount{}, err
	}
	return active, nil
}

// CreateCodexAccountSwitch inserts or returns an idempotent global switch.
func (s *Store) CreateCodexAccountSwitch(ctx context.Context, rec domain.CodexAccountSwitch) (domain.CodexAccountSwitch, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var n int64
	err := s.inTxDB(ctx, "create Codex account switch snapshot", func(q *gen.Queries, db gen.DBTX) error {
		var insertErr error
		n, insertErr = q.InsertCodexAccountSwitch(ctx, gen.InsertCodexAccountSwitchParams{
			ID: rec.ID, SourceAccountID: rec.SourceAccountID, TargetAccountID: rec.TargetAccountID,
			IdempotencyKey: rec.IdempotencyKey, RequestFingerprint: rec.RequestFingerprint,
			ExpectedAccountRevision: rec.ExpectedAccountRevision, Phase: string(rec.Phase),
			CreatedAt: rec.CreatedAt.UTC(), UpdatedAt: rec.UpdatedAt.UTC(),
		})
		if insertErr != nil || n == 0 {
			return insertErr
		}
		for _, item := range rec.Sessions {
			if err := insertCodexAccountSwitchSession(ctx, db, rec.ID, item); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return domain.CodexAccountSwitch{}, false, fmt.Errorf("create Codex account switch %s: %w", rec.ID, err)
	}
	if n > 0 {
		return rec, true, nil
	}
	if row, readErr := s.qw.GetCodexAccountSwitchByIdempotency(ctx, rec.IdempotencyKey); readErr == nil {
		existing := codexAccountSwitchFromGen(row)
		if existing.RequestFingerprint == rec.RequestFingerprint {
			return existing, false, nil
		}
		return existing, false, ports.ErrCodexAccountSwitchIdempotencyConflict
	} else if !errors.Is(readErr, sql.ErrNoRows) {
		return domain.CodexAccountSwitch{}, false, readErr
	}
	if row, readErr := s.qw.GetActiveCodexAccountSwitch(ctx); readErr == nil {
		return codexAccountSwitchFromGen(row), false, ports.ErrCodexAccountSwitchInProgress
	} else if !errors.Is(readErr, sql.ErrNoRows) {
		return domain.CodexAccountSwitch{}, false, readErr
	}
	return domain.CodexAccountSwitch{}, false, ports.ErrCodexAccountSwitchIdempotencyConflict
}

// GetCodexAccountSwitch reads one switch by ID.
func (s *Store) GetCodexAccountSwitch(ctx context.Context, id string) (domain.CodexAccountSwitch, bool, error) {
	row, err := s.qr.GetCodexAccountSwitch(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CodexAccountSwitch{}, false, nil
	}
	if err != nil {
		return domain.CodexAccountSwitch{}, false, fmt.Errorf("get Codex account switch %s: %w", id, err)
	}
	return codexAccountSwitchFromGen(row), true, nil
}

// GetCodexAccountSwitchByIdempotency reads one switch by idempotency key.
func (s *Store) GetCodexAccountSwitchByIdempotency(ctx context.Context, key string) (domain.CodexAccountSwitch, bool, error) {
	row, err := s.qr.GetCodexAccountSwitchByIdempotency(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CodexAccountSwitch{}, false, nil
	}
	if err != nil {
		return domain.CodexAccountSwitch{}, false, fmt.Errorf("get Codex account switch by idempotency key: %w", err)
	}
	return codexAccountSwitchFromGen(row), true, nil
}

// GetActiveCodexAccountSwitch reads the sole nonterminal switch.
func (s *Store) GetActiveCodexAccountSwitch(ctx context.Context) (domain.CodexAccountSwitch, bool, error) {
	row, err := s.qr.GetActiveCodexAccountSwitch(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CodexAccountSwitch{}, false, nil
	}
	if err != nil {
		return domain.CodexAccountSwitch{}, false, fmt.Errorf("get active Codex account switch: %w", err)
	}
	return codexAccountSwitchFromGen(row), true, nil
}

// UpdateCodexAccountSwitch applies a compare-and-swap phase transition.
func (s *Store) UpdateCodexAccountSwitch(ctx context.Context, rec domain.CodexAccountSwitch, expected domain.CodexAccountSwitchPhase) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.UpdateCodexAccountSwitchPhase(ctx, gen.UpdateCodexAccountSwitchPhaseParams{
		NextPhase: string(rec.Phase), FailureCode: rec.FailureCode,
		CredentialsCommittedAt: timePtrToNull(rec.CredentialsCommittedAt),
		UpdatedAt:              rec.UpdatedAt.UTC(), CompletedAt: timePtrToNull(rec.CompletedAt),
		ID: rec.ID, ExpectedPhase: string(expected),
	})
	if err != nil {
		return false, fmt.Errorf("update Codex account switch %s: %w", rec.ID, err)
	}
	return n > 0, nil
}

// InsertCodexAccountSwitchSession records one affected session.
func (s *Store) InsertCodexAccountSwitchSession(ctx context.Context, switchID string, rec domain.CodexAccountSwitchSession) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return insertCodexAccountSwitchSession(ctx, s.writeDB, switchID, rec)
}

func insertCodexAccountSwitchSession(ctx context.Context, db gen.DBTX, switchID string, rec domain.CodexAccountSwitchSession) error {
	result, err := db.ExecContext(ctx, `INSERT INTO codex_account_switch_sessions (
		switch_id, session_id, native_session_id, interface_mode,
		source_handle_id, source_generation, was_running, stop_state, restart_state,
		reviewer_was_running, reviewer_source_handle_id, reviewer_native_session_id,
		reviewer_stop_state, reviewer_restart_state
	) VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?, ?)
	ON CONFLICT DO NOTHING`, switchID, string(rec.SessionID), rec.NativeSessionID, string(rec.InterfaceMode),
		rec.SourceHandleID, rec.SourceGeneration, rec.WasRunning, rec.RestartState,
		rec.ReviewerWasRunning, rec.ReviewerSourceHandleID, rec.ReviewerNativeSessionID,
		rec.ReviewerStopState, rec.ReviewerRestartState)
	if err != nil {
		return fmt.Errorf("insert Codex account switch session %s: %w", rec.SessionID, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("insert Codex account switch session %s result: %w", rec.SessionID, err)
	}
	if n == 0 {
		return fmt.Errorf("insert Codex account switch session %s: duplicate", rec.SessionID)
	}
	return nil
}

// ListCodexAccountSwitchSessions reads affected sessions in stable order.
func (s *Store) ListCodexAccountSwitchSessions(ctx context.Context, switchID string) ([]domain.CodexAccountSwitchSession, error) {
	rows, err := s.readDB.QueryContext(ctx, `SELECT session_id, native_session_id, interface_mode,
		source_handle_id, source_generation, was_running, stop_state, restart_state,
		reviewer_was_running, reviewer_source_handle_id, reviewer_native_session_id,
		reviewer_stop_state, reviewer_restart_state, error_code, stopped_at, restarted_at
		FROM codex_account_switch_sessions WHERE switch_id = ? ORDER BY session_id`, switchID)
	if err != nil {
		return nil, fmt.Errorf("list Codex account switch sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]domain.CodexAccountSwitchSession, 0)
	for rows.Next() {
		var item domain.CodexAccountSwitchSession
		var mode string
		var stoppedAt, restartedAt sql.NullTime
		if err := rows.Scan(&item.SessionID, &item.NativeSessionID, &mode,
			&item.SourceHandleID, &item.SourceGeneration, &item.WasRunning, &item.StopState, &item.RestartState,
			&item.ReviewerWasRunning, &item.ReviewerSourceHandleID, &item.ReviewerNativeSessionID,
			&item.ReviewerStopState, &item.ReviewerRestartState, &item.ErrorCode, &stoppedAt, &restartedAt); err != nil {
			return nil, fmt.Errorf("scan Codex account switch session: %w", err)
		}
		item.InterfaceMode = domain.SessionMode(mode)
		item.StoppedAt = nullTimeToPtr(stoppedAt)
		item.RestartedAt = nullTimeToPtr(restartedAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

// UpdateCodexAccountSwitchSession applies compare-and-swap progress for one session.
func (s *Store) UpdateCodexAccountSwitchSession(ctx context.Context, switchID string, rec domain.CodexAccountSwitchSession, expectedStop, expectedRestart string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.UpdateCodexAccountSwitchSession(ctx, gen.UpdateCodexAccountSwitchSessionParams{
		StopState: rec.StopState, RestartState: rec.RestartState,
		ErrorCode: rec.ErrorCode, ReviewerStopState: rec.ReviewerStopState,
		ReviewerRestartState: rec.ReviewerRestartState, StoppedAt: timePtrToNull(rec.StoppedAt),
		RestartedAt: timePtrToNull(rec.RestartedAt), SwitchID: switchID, SessionID: string(rec.SessionID),
		ExpectedStopState: expectedStop, ExpectedRestartState: expectedRestart,
	})
	if err != nil {
		return false, fmt.Errorf("update Codex account switch session %s: %w", rec.SessionID, err)
	}
	return n > 0, nil
}

func codexAccountSwitchFromGen(row gen.CodexAccountSwitch) domain.CodexAccountSwitch {
	return domain.CodexAccountSwitch{
		ID: row.ID, SourceAccountID: row.SourceAccountID, TargetAccountID: row.TargetAccountID,
		Phase: domain.CodexAccountSwitchPhase(row.Phase), FailureCode: row.FailureCode,
		CredentialsCommittedAt: nullTimeToPtr(row.CredentialsCommittedAt),
		CreatedAt:              row.CreatedAt, UpdatedAt: row.UpdatedAt, CompletedAt: nullTimeToPtr(row.CompletedAt),
		IdempotencyKey: row.IdempotencyKey, RequestFingerprint: row.RequestFingerprint,
		ExpectedAccountRevision: row.ExpectedAccountRevision,
	}
}
