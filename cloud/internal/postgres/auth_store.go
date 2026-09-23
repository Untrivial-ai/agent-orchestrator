package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (s *Store) UpsertWorkOSUser(
	ctx context.Context,
	principal domain.Principal,
) (domain.Principal, error) {
	err := s.pool.QueryRow(
		ctx,
		`INSERT INTO ao_users (
			auth_provider, external_user_id, email, display_name
		) VALUES ('workos', $1, $2, $3)
		ON CONFLICT (auth_provider, external_user_id)
		DO UPDATE SET
			email = EXCLUDED.email,
			display_name = EXCLUDED.display_name,
			updated_at = now()
		RETURNING id`,
		principal.ExternalID,
		principal.Email,
		principal.DisplayName,
	).Scan(&principal.UserID)
	if err != nil {
		return domain.Principal{}, err
	}
	if err := s.ensureWorkOSOrganization(ctx, principal); err != nil {
		return domain.Principal{}, err
	}
	return principal, nil
}

func (s *Store) ensureWorkOSOrganization(
	ctx context.Context,
	principal domain.Principal,
) error {
	if principal.ExternalOrgID == "" {
		return s.ensurePersonalOrganization(ctx, principal)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(
		ctx,
		`SELECT set_config('ao.user_id', $1, true),
			set_config('ao.external_org_id', $2, true)`,
		principal.UserID,
		principal.ExternalOrgID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		principal.ExternalOrgID,
	); err != nil {
		return err
	}
	orgName := strings.TrimSpace(principal.OrgName)
	if orgName == "" {
		orgName = "WorkOS organization"
	}
	role := principal.OrgRole
	if role != "owner" && role != "admin" && role != "member" {
		role = "member"
	}
	var ownerUserID *string
	if role == "owner" {
		ownerUserID = &principal.UserID
	}
	digest := sha256.Sum256([]byte(principal.ExternalOrgID))
	slug := "workos-" + hex.EncodeToString(digest[:8])
	var orgID string
	if err := tx.QueryRow(
		ctx,
		`INSERT INTO ao_organizations (
			auth_provider, external_org_id, slug, display_name, kind,
			owner_user_id, created_by_user_id
		) VALUES ('workos', $1, $2, $3, 'team', $4, $5)
		ON CONFLICT (auth_provider, external_org_id)
		DO UPDATE SET
			display_name = EXCLUDED.display_name,
			owner_user_id = COALESCE(ao_organizations.owner_user_id, EXCLUDED.owner_user_id),
			updated_at = now()
		RETURNING id`,
		principal.ExternalOrgID,
		slug,
		orgName,
		ownerUserID,
		principal.UserID,
	).Scan(&orgID); err != nil {
		return err
	}
	if _, err := tx.Exec(
		ctx,
		`SELECT set_config('ao.org_id', $1, true)`,
		orgID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		ctx,
		`INSERT INTO ao_org_memberships (org_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (org_id, user_id)
		DO UPDATE SET role = EXCLUDED.role, updated_at = now()`,
		orgID,
		principal.UserID,
		role,
	); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ensurePersonalOrganization(
	ctx context.Context,
	principal domain.Principal,
) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(
		ctx,
		`SELECT set_config('ao.user_id', $1, true)`,
		principal.UserID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		principal.UserID,
	); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(
		ctx,
		`SELECT EXISTS (
			SELECT 1 FROM ao_org_memberships
			WHERE user_id = $1 AND status = 'active'
		)`,
		principal.UserID,
	).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return tx.Commit(ctx)
	}

	var orgID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()`).Scan(&orgID); err != nil {
		return err
	}
	if _, err := tx.Exec(
		ctx,
		`SELECT set_config('ao.org_id', $1, true)`,
		orgID,
	); err != nil {
		return err
	}
	slug := "personal-" + strings.ReplaceAll(principal.UserID, "-", "")
	displayName := principal.DisplayName + "'s organization"
	if _, err := tx.Exec(
		ctx,
		`INSERT INTO ao_organizations (
			id, auth_provider, slug, display_name, kind, owner_user_id, created_by_user_id
		) VALUES ($1, 'workos', $2, $3, 'personal', $4, $4)`,
		orgID,
		slug,
		displayName,
		principal.UserID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		ctx,
		`INSERT INTO ao_org_memberships (org_id, user_id, role)
		VALUES ($1, $2, 'owner')`,
		orgID,
		principal.UserID,
	); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) RegisterLocal(
	ctx context.Context,
	registration domain.LocalRegistration,
	tokenHash []byte,
	expiresAt time.Time,
) (domain.Principal, string, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Principal{}, "", err
	}
	defer tx.Rollback(ctx)

	principal := domain.Principal{
		Provider:    "local",
		ExternalID:  strings.ToLower(strings.TrimSpace(registration.Email)),
		Email:       strings.ToLower(strings.TrimSpace(registration.Email)),
		DisplayName: strings.TrimSpace(registration.DisplayName),
	}
	err = tx.QueryRow(
		ctx,
		`INSERT INTO ao_users (
			auth_provider, external_user_id, email, display_name, password_hash
		) VALUES ('local', $1, $2, $3, $4)
		RETURNING id`,
		principal.ExternalID,
		principal.Email,
		principal.DisplayName,
		registration.PasswordHash,
	).Scan(&principal.UserID)
	if err != nil {
		return domain.Principal{}, "", normalizeConstraintError(err)
	}

	var orgID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()`).Scan(&orgID); err != nil {
		return domain.Principal{}, "", err
	}
	if _, err := tx.Exec(
		ctx,
		`SELECT set_config('ao.user_id', $1, true), set_config('ao.org_id', $2, true)`,
		principal.UserID,
		orgID,
	); err != nil {
		return domain.Principal{}, "", err
	}
	if _, err := tx.Exec(
		ctx,
		`DELETE FROM ao_auth_sessions WHERE expires_at <= now()`,
	); err != nil {
		return domain.Principal{}, "", err
	}
	if _, err := tx.Exec(
		ctx,
		`INSERT INTO ao_organizations (
			id, auth_provider, slug, display_name, kind, owner_user_id, created_by_user_id
		) VALUES ($1, 'local', $2, $3, 'personal', $4, $4)`,
		orgID,
		registration.OrgSlug,
		registration.OrgName,
		principal.UserID,
	); err != nil {
		return domain.Principal{}, "", normalizeConstraintError(err)
	}
	if _, err := tx.Exec(
		ctx,
		`INSERT INTO ao_org_memberships (org_id, user_id, role)
		VALUES ($1, $2, 'owner')`,
		orgID,
		principal.UserID,
	); err != nil {
		return domain.Principal{}, "", err
	}
	if _, err := tx.Exec(
		ctx,
		`INSERT INTO ao_auth_sessions (user_id, token_hash, expires_at)
		VALUES ($1, $2, $3)`,
		principal.UserID,
		tokenHash,
		expiresAt,
	); err != nil {
		return domain.Principal{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Principal{}, "", err
	}
	return principal, orgID, nil
}

func (s *Store) LocalUserByEmail(
	ctx context.Context,
	email string,
) (domain.Principal, string, error) {
	var principal domain.Principal
	var passwordHash string
	err := s.pool.QueryRow(
		ctx,
		`SELECT id, external_user_id, email, display_name, password_hash
		FROM ao_users
		WHERE auth_provider = 'local' AND lower(email) = lower($1)`,
		strings.TrimSpace(email),
	).Scan(
		&principal.UserID,
		&principal.ExternalID,
		&principal.Email,
		&principal.DisplayName,
		&passwordHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Principal{}, "", ErrNotFound
	}
	if err != nil {
		return domain.Principal{}, "", err
	}
	principal.Provider = "local"
	return principal, passwordHash, nil
}

func (s *Store) CreateLocalSession(
	ctx context.Context,
	userID string,
	tokenHash []byte,
	expiresAt time.Time,
) error {
	if _, err := s.pool.Exec(
		ctx,
		`DELETE FROM ao_auth_sessions WHERE expires_at <= now()`,
	); err != nil {
		return err
	}
	_, err := s.pool.Exec(
		ctx,
		`INSERT INTO ao_auth_sessions (user_id, token_hash, expires_at)
		VALUES ($1, $2, $3)`,
		userID,
		tokenHash,
		expiresAt,
	)
	return err
}

func (s *Store) PrincipalFromLocalToken(
	ctx context.Context,
	tokenHash []byte,
) (domain.Principal, error) {
	var principal domain.Principal
	err := s.pool.QueryRow(
		ctx,
		`SELECT users.id, users.external_user_id, users.email, users.display_name
		FROM ao_auth_sessions sessions
		JOIN ao_users users ON users.id = sessions.user_id
		WHERE sessions.token_hash = $1
		  AND sessions.expires_at > now()
		  AND users.auth_provider = 'local'`,
		tokenHash,
	).Scan(
		&principal.UserID,
		&principal.ExternalID,
		&principal.Email,
		&principal.DisplayName,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Principal{}, ErrNotFound
	}
	if err != nil {
		return domain.Principal{}, err
	}
	principal.Provider = "local"
	return principal, nil
}

func (s *Store) RevokeLocalSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.pool.Exec(
		ctx,
		`DELETE FROM ao_auth_sessions WHERE token_hash = $1`,
		tokenHash,
	)
	return err
}

func (s *Store) ListMemberships(
	ctx context.Context,
	principal domain.Principal,
) ([]domain.Membership, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(
		ctx,
		`SELECT set_config('ao.user_id', $1, true)`,
		principal.UserID,
	); err != nil {
		return nil, err
	}
	rows, err := tx.Query(
		ctx,
		`SELECT membership.org_id, organization.slug, organization.display_name, membership.role
		FROM ao_org_memberships membership
		JOIN ao_organizations organization ON organization.id = membership.org_id
		WHERE membership.user_id = $1
		  AND membership.status = 'active'
		  AND organization.status = 'active'
		  AND (
			organization.external_org_id IS NULL
			OR $2 <> 'workos'
			OR (
				$3 <> ''
				AND organization.auth_provider = 'workos'
				AND organization.external_org_id = $3
			)
			OR (
				$3 = ''
				AND organization.external_org_id IS NULL
				AND organization.owner_user_id = $1
			)
		  )
		ORDER BY organization.created_at, organization.id`,
		principal.UserID,
		principal.Provider,
		principal.ExternalOrgID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var memberships []domain.Membership
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
		return nil, err
	}
	return memberships, nil
}

func normalizeConstraintError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23503":
			return ErrNotFound
		case "23505":
			return ErrConflict
		case "22P02", "23514":
			return ErrInvalid
		}
	}
	return err
}
