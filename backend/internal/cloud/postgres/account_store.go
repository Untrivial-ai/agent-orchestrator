package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/cloud/domain"
)

// UpsertGoogleUser records a verified Google identity and creates its personal
// organization on first sign-in. The organization and owner membership are
// committed atomically.
func (s *Store) UpsertGoogleUser(ctx context.Context, principal domain.Principal) (domain.Principal, error) {
	principal.Provider = "google"
	principal.ExternalID = strings.TrimSpace(principal.ExternalID)
	principal.Email = strings.ToLower(strings.TrimSpace(principal.Email))
	principal.DisplayName = strings.TrimSpace(principal.DisplayName)
	if principal.ExternalID == "" || principal.Email == "" {
		return domain.Principal{}, ErrInvalid
	}
	if principal.DisplayName == "" {
		principal.DisplayName = principal.Email
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Principal{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := tx.QueryRow(
		ctx,
		`INSERT INTO ao_users (auth_provider, external_user_id, email, display_name)
		 VALUES ('google', $1, $2, $3)
		 ON CONFLICT (auth_provider, external_user_id)
		 DO UPDATE SET email = EXCLUDED.email,
		               display_name = EXCLUDED.display_name,
		               updated_at = now()
		 RETURNING id`,
		principal.ExternalID,
		principal.Email,
		principal.DisplayName,
	).Scan(&principal.UserID); err != nil {
		return domain.Principal{}, normalizeError(err)
	}
	if _, err := tx.Exec(
		ctx,
		`SELECT set_config('ao.user_id', $1, true),
		        pg_advisory_xact_lock(hashtextextended($1, 0))`,
		principal.UserID,
	); err != nil {
		return domain.Principal{}, err
	}
	var hasAnyMembership bool
	if err := tx.QueryRow(
		ctx,
		`SELECT EXISTS (
			SELECT 1 FROM ao_org_memberships
			WHERE user_id = $1
		)`,
		principal.UserID,
	).Scan(&hasAnyMembership); err != nil {
		return domain.Principal{}, err
	}
	if !hasAnyMembership {
		orgID := uuid.NewString()
		if _, err := tx.Exec(ctx, `SELECT set_config('ao.org_id', $1, true)`, orgID); err != nil {
			return domain.Principal{}, err
		}
		slug := "personal-" + strings.ReplaceAll(principal.UserID, "-", "")
		if _, err := tx.Exec(
			ctx,
			`INSERT INTO ao_organizations (
				id, slug, display_name, kind, owner_user_id, created_by_user_id
			) VALUES ($1, $2, $3, 'personal', $4, $4)`,
			orgID,
			slug,
			principal.DisplayName+"'s organization",
			principal.UserID,
		); err != nil {
			return domain.Principal{}, normalizeError(err)
		}
		if _, err := tx.Exec(
			ctx,
			`INSERT INTO ao_org_memberships (org_id, user_id, role)
			 VALUES ($1, $2, 'owner')`,
			orgID,
			principal.UserID,
		); err != nil {
			return domain.Principal{}, normalizeError(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Principal{}, err
	}
	return principal, nil
}

// PrincipalByID resolves the current user for an already verified AO access
// token. Memberships are loaded separately and never trusted from token claims.
func (s *Store) PrincipalByID(ctx context.Context, userID string) (domain.Principal, error) {
	var principal domain.Principal
	err := s.pool.QueryRow(
		ctx,
		`SELECT id, auth_provider, external_user_id, email, display_name
		 FROM ao_users WHERE id = $1`,
		strings.TrimSpace(userID),
	).Scan(
		&principal.UserID,
		&principal.Provider,
		&principal.ExternalID,
		&principal.Email,
		&principal.DisplayName,
	)
	if err != nil {
		return domain.Principal{}, normalizeError(err)
	}
	return principal, nil
}

// CreateRefreshSession persists only a refresh-token digest.
func (s *Store) CreateRefreshSession(ctx context.Context, userID string, tokenHash []byte, expiresAt time.Time) error {
	_, err := s.pool.Exec(
		ctx,
		`WITH expired AS (
			DELETE FROM ao_auth_sessions WHERE expires_at <= now()
		)
		INSERT INTO ao_auth_sessions (user_id, token_hash, expires_at)
		VALUES ($1, $2, $3)`,
		userID,
		tokenHash,
		expiresAt,
	)
	return normalizeError(err)
}

// RotateRefreshSession consumes an old refresh token and inserts its
// replacement in one transaction. The replacement retains the chain's
// original creation time and absolute expiry. Concurrent replay attempts
// cannot both win.
func (s *Store) RotateRefreshSession(
	ctx context.Context,
	oldHash, newHash []byte,
) (domain.Principal, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Principal{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var userID string
	var createdAt, expiresAt time.Time
	if err := tx.QueryRow(
		ctx,
		`DELETE FROM ao_auth_sessions
		 WHERE token_hash = $1 AND expires_at > now()
		 RETURNING user_id, created_at, expires_at`,
		oldHash,
	).Scan(&userID, &createdAt, &expiresAt); err != nil {
		return domain.Principal{}, normalizeError(err)
	}
	if _, err := tx.Exec(
		ctx,
		`INSERT INTO ao_auth_sessions (user_id, token_hash, created_at, expires_at)
		 VALUES ($1, $2, $3, $4)`,
		userID,
		newHash,
		createdAt,
		expiresAt,
	); err != nil {
		return domain.Principal{}, normalizeError(err)
	}
	var principal domain.Principal
	if err := tx.QueryRow(
		ctx,
		`SELECT id, auth_provider, external_user_id, email, display_name
		 FROM ao_users WHERE id = $1`,
		userID,
	).Scan(
		&principal.UserID,
		&principal.Provider,
		&principal.ExternalID,
		&principal.Email,
		&principal.DisplayName,
	); err != nil {
		return domain.Principal{}, normalizeError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Principal{}, err
	}
	return principal, nil
}

// RevokeRefreshSession removes one refresh-token digest. Revocation is
// idempotent so logout does not reveal whether a token existed.
func (s *Store) RevokeRefreshSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM ao_auth_sessions WHERE token_hash = $1`, tokenHash)
	return err
}

// ListMemberships returns all active AO organizations for the current user.
func (s *Store) ListMemberships(ctx context.Context, principal domain.Principal) ([]domain.Membership, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('ao.user_id', $1, true)`, principal.UserID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(
		ctx,
		`SELECT membership.org_id, organization.slug,
		        organization.display_name, membership.role
		 FROM ao_org_memberships membership
		 JOIN ao_organizations organization ON organization.id = membership.org_id
		 WHERE membership.user_id = $1
		   AND membership.status = 'active'
		   AND organization.status = 'active'
		 ORDER BY organization.created_at, organization.id`,
		principal.UserID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	memberships := make([]domain.Membership, 0)
	for rows.Next() {
		var membership domain.Membership
		if err := rows.Scan(
			&membership.OrgID,
			&membership.OrgSlug,
			&membership.DisplayName,
			&membership.Role,
		); err != nil {
			return nil, err
		}
		memberships = append(memberships, membership)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit membership read: %w", err)
	}
	return memberships, nil
}
