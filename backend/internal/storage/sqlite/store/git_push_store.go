package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const pushApprovalColumns = `id, project_id, session_id, repository, remote, remote_url, branch,
expected_head_sha, created_at, approved_at, approved_by, expires_at, consumed_at, status, result, error_message`

type rowScanner interface{ Scan(...any) error }

func scanPushApproval(row rowScanner) (domain.PushApproval, error) {
	var rec domain.PushApproval
	var approvedAt, consumedAt sql.NullTime
	if err := row.Scan(
		&rec.ID, &rec.ProjectID, &rec.SessionID, &rec.Repository, &rec.Remote, &rec.RemoteURL,
		&rec.Branch, &rec.ExpectedHeadSHA, &rec.CreatedAt, &approvedAt, &rec.ApprovedBy,
		&rec.ExpiresAt, &consumedAt, &rec.Status, &rec.Result, &rec.ErrorMessage,
	); err != nil {
		return domain.PushApproval{}, err
	}
	if approvedAt.Valid {
		rec.ApprovedAt = &approvedAt.Time
	}
	if consumedAt.Valid {
		rec.ConsumedAt = &consumedAt.Time
	}
	return rec, nil
}

func (s *Store) CreatePushApproval(ctx context.Context, rec domain.PushApproval) (domain.PushApproval, error) {
	if err := rec.ValidateForCreate(); err != nil {
		return domain.PushApproval{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	row := s.writeDB.QueryRowContext(ctx, `INSERT INTO push_approvals (
id, project_id, session_id, repository, remote, remote_url, branch, expected_head_sha,
created_at, expires_at, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'PENDING') RETURNING `+pushApprovalColumns,
		rec.ID, rec.ProjectID, rec.SessionID, rec.Repository, rec.Remote, rec.RemoteURL,
		rec.Branch, rec.ExpectedHeadSHA, rec.CreatedAt, rec.ExpiresAt)
	created, err := scanPushApproval(row)
	if err != nil {
		return domain.PushApproval{}, fmt.Errorf("create push approval %s: %w", rec.ID, err)
	}
	return created, nil
}

func (s *Store) GetPushApproval(ctx context.Context, id string) (domain.PushApproval, bool, error) {
	rec, err := scanPushApproval(s.readDB.QueryRowContext(ctx,
		`SELECT `+pushApprovalColumns+` FROM push_approvals WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.PushApproval{}, false, nil
	}
	if err != nil {
		return domain.PushApproval{}, false, fmt.Errorf("get push approval %s: %w", id, err)
	}
	return rec, true, nil
}

func (s *Store) ApprovePushApproval(ctx context.Context, id, approvedBy string, at time.Time) (domain.PushApproval, bool, error) {
	return s.updatePushApproval(ctx, "approve", `UPDATE push_approvals
SET status = 'APPROVED', approved_at = ?, approved_by = ?
WHERE id = ? AND status = 'PENDING' AND expires_at > ? RETURNING `+pushApprovalColumns,
		at, approvedBy, id, at)
}

// ClaimPushApproval is the one-time consumption boundary. SQLite executes the
// conditional UPDATE atomically on the single writer connection, so concurrent
// callers cannot both transition APPROVED to CONSUMED.
func (s *Store) ClaimPushApproval(ctx context.Context, id string, at time.Time) (domain.PushApproval, bool, error) {
	return s.updatePushApproval(ctx, "claim", `UPDATE push_approvals
SET status = 'CONSUMED', consumed_at = ?
WHERE id = ? AND status = 'APPROVED' AND expires_at > ? RETURNING `+pushApprovalColumns,
		at, id, at)
}

func (s *Store) RevokePushApproval(ctx context.Context, id string) (domain.PushApproval, bool, error) {
	return s.updatePushApproval(ctx, "revoke", `UPDATE push_approvals
SET status = 'REVOKED', result = 'revoked'
WHERE id = ? AND status IN ('PENDING', 'APPROVED') RETURNING `+pushApprovalColumns, id)
}

func (s *Store) ExpirePushApproval(ctx context.Context, id string, at time.Time) (domain.PushApproval, bool, error) {
	return s.updatePushApproval(ctx, "expire", `UPDATE push_approvals
SET status = 'EXPIRED', result = 'expired'
WHERE id = ? AND status IN ('PENDING', 'APPROVED') AND expires_at <= ? RETURNING `+pushApprovalColumns,
		id, at)
}

func (s *Store) CompletePushApproval(ctx context.Context, id, result string) (domain.PushApproval, bool, error) {
	return s.updatePushApproval(ctx, "complete", `UPDATE push_approvals
SET result = ?, error_message = ''
WHERE id = ? AND status = 'CONSUMED' RETURNING `+pushApprovalColumns, result, id)
}

func (s *Store) FailPushApproval(ctx context.Context, id, result, message string) (domain.PushApproval, bool, error) {
	return s.updatePushApproval(ctx, "fail", `UPDATE push_approvals
SET status = 'FAILED', result = ?, error_message = ?
WHERE id = ? AND status IN ('PENDING', 'APPROVED', 'CONSUMED') RETURNING `+pushApprovalColumns,
		result, message, id)
}

func (s *Store) updatePushApproval(ctx context.Context, action, statement string, args ...any) (domain.PushApproval, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rec, err := scanPushApproval(s.writeDB.QueryRowContext(ctx, statement, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.PushApproval{}, false, nil
	}
	if err != nil {
		return domain.PushApproval{}, false, fmt.Errorf("%s push approval: %w", action, err)
	}
	return rec, true, nil
}

func (s *Store) CreateGitActionAudit(ctx context.Context, rec domain.GitActionAudit) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var finished any
	if rec.FinishedAt != nil {
		finished = *rec.FinishedAt
	}
	var approvalID any
	if rec.ApprovalID != "" {
		approvalID = rec.ApprovalID
	}
	_, err := s.writeDB.ExecContext(ctx, `INSERT INTO git_action_audits (
id, project_id, session_id, action, repository, remote, branch, head_sha, approval_id, requested_by,
executed_by, started_at, finished_at, result, error_message)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.ProjectID, rec.SessionID, rec.Action, rec.Repository, rec.Remote, rec.Branch, rec.HeadSHA, approvalID,
		rec.RequestedBy, rec.ExecutedBy, rec.StartedAt, finished, rec.Result, rec.ErrorMessage)
	if err != nil {
		return fmt.Errorf("create git action audit %s: %w", rec.ID, err)
	}
	return nil
}

func (s *Store) ListGitActionAuditsByApproval(ctx context.Context, approvalID string) ([]domain.GitActionAudit, error) {
	rows, err := s.readDB.QueryContext(ctx, `SELECT id, project_id, session_id, action, repository, remote, branch, head_sha,
approval_id, requested_by, executed_by, started_at, finished_at, result, error_message
FROM git_action_audits WHERE approval_id = ? ORDER BY started_at ASC, id ASC`, approvalID)
	if err != nil {
		return nil, fmt.Errorf("list git action audits: %w", err)
	}
	defer rows.Close()
	result := make([]domain.GitActionAudit, 0)
	for rows.Next() {
		var rec domain.GitActionAudit
		var approval sql.NullString
		var finished sql.NullTime
		if err := rows.Scan(&rec.ID, &rec.ProjectID, &rec.SessionID, &rec.Action, &rec.Repository, &rec.Remote, &rec.Branch,
			&rec.HeadSHA, &approval, &rec.RequestedBy, &rec.ExecutedBy, &rec.StartedAt,
			&finished, &rec.Result, &rec.ErrorMessage); err != nil {
			return nil, err
		}
		if approval.Valid {
			rec.ApprovalID = approval.String
		}
		if finished.Valid {
			rec.FinishedAt = &finished.Time
		}
		result = append(result, rec)
	}
	return result, rows.Err()
}
