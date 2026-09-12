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
	if rec.SourceKind == "" {
		rec.SourceKind = domain.CodexAccountSwitchSourceManaged
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var n int64
	err := s.inTx(ctx, "create Codex account switch", func(q *gen.Queries) error {
		var insertErr error
		n, insertErr = q.InsertCodexAccountSwitch(ctx, gen.InsertCodexAccountSwitchParams{
			ID: rec.ID, SourceKind: string(rec.SourceKind), SourceAccountID: rec.SourceAccountID, TargetAccountID: rec.TargetAccountID,
			IdempotencyKey: rec.IdempotencyKey, RequestFingerprint: rec.RequestFingerprint,
			ExpectedAccountRevision: rec.ExpectedAccountRevision, RestartRunningSessions: rec.RestartIdleSessions, Phase: string(rec.Phase),
			CreatedAt: rec.CreatedAt.UTC(), UpdatedAt: rec.UpdatedAt.UTC(),
		})
		return insertErr
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

func codexAccountSwitchFromGen(row gen.CodexAccountSwitch) domain.CodexAccountSwitch {
	return domain.CodexAccountSwitch{
		ID: row.ID, SourceKind: domain.CodexAccountSwitchSourceKind(row.SourceKind), SourceAccountID: row.SourceAccountID, TargetAccountID: row.TargetAccountID,
		RestartIdleSessions: row.RestartRunningSessions, Phase: domain.CodexAccountSwitchPhase(row.Phase), FailureCode: row.FailureCode,
		CredentialsCommittedAt: nullTimeToPtr(row.CredentialsCommittedAt),
		CreatedAt:              row.CreatedAt, UpdatedAt: row.UpdatedAt, CompletedAt: nullTimeToPtr(row.CompletedAt),
		IdempotencyKey: row.IdempotencyKey, RequestFingerprint: row.RequestFingerprint,
		ExpectedAccountRevision: row.ExpectedAccountRevision,
	}
}
