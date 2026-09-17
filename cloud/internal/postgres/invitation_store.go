package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const invitationColumns = `
	invite.id, invite.org_id, invite.email, invite.invited_by_user_id,
	COALESCE(inviter.email, ''), COALESCE(inviter.display_name, ''),
	invite.role, invite.status, invite.expires_at, invite.accepted_at,
	invite.declined_at, invite.revoked_at, invite.created_at, invite.updated_at`

func scanInvitation(row pgx.Row) (domain.Invitation, error) {
	var invite domain.Invitation
	err := row.Scan(
		&invite.ID, &invite.OrgID, &invite.Email, &invite.InvitedByUserID,
		&invite.InvitedByEmail, &invite.InvitedByName,
		&invite.Role, &invite.Status, &invite.ExpiresAt, &invite.AcceptedAt,
		&invite.DeclinedAt, &invite.RevokedAt, &invite.CreatedAt, &invite.UpdatedAt,
	)
	if err != nil {
		return domain.Invitation{}, err
	}
	invite.Email = strings.ToLower(invite.Email)
	return invite, nil
}

// ListOrgMembers returns every active member of an organization, joined with
// their user profile.
func (s *Store) ListOrgMembers(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
) ([]domain.OrgMember, error) {
	var members []domain.OrgMember
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		rows, err := tx.Query(
			ctx,
			`SELECT users.id, users.email, users.display_name, membership.role, membership.created_at
			FROM ao_org_memberships membership
			JOIN ao_users users ON users.id = membership.user_id
			WHERE membership.org_id = $1 AND membership.status = 'active'
			ORDER BY membership.created_at`,
			orgID,
		)
		if err != nil {
			return fmt.Errorf("list org members: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var member domain.OrgMember
			if err := rows.Scan(
				&member.UserID, &member.Email, &member.DisplayName, &member.Role, &member.CreatedAt,
			); err != nil {
				return err
			}
			members = append(members, member)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return members, nil
}

// ListOrgInvitations returns every invitation raised for an organization,
// most recent first. Only owners and admins may see the list.
func (s *Store) ListOrgInvitations(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
) ([]domain.Invitation, error) {
	var invites []domain.Invitation
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		if err := requireOrgAdmin(ctx, tx, orgID, principal.UserID); err != nil {
			return err
		}
		rows, err := tx.Query(
			ctx,
			`SELECT `+invitationColumns+`
			FROM ao_org_invitations invite
			LEFT JOIN ao_users inviter ON inviter.id = invite.invited_by_user_id
			WHERE invite.org_id = $1
			ORDER BY invite.created_at DESC`,
			orgID,
		)
		if err != nil {
			return fmt.Errorf("list org invitations: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			invite, err := scanInvitation(rows)
			if err != nil {
				return err
			}
			invites = append(invites, invite)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return invites, nil
}

func (s *Store) ListMyInvitations(
	ctx context.Context,
	principal domain.Principal,
) ([]domain.Invitation, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('ao.user_id', $1, true)`, principal.UserID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx,
		`SELECT `+invitationColumns+`
		 FROM ao_org_invitations invite
		 LEFT JOIN ao_users inviter ON inviter.id = invite.invited_by_user_id
		 WHERE invite.invited_user_id = $1 AND invite.status = 'pending' AND invite.expires_at > now()
		 ORDER BY invite.created_at DESC`, principal.UserID,
	)
	if err != nil {
		return nil, fmt.Errorf("list my invitations: %w", err)
	}
	defer rows.Close()
	items := make([]domain.Invitation, 0)
	for rows.Next() {
		item, err := scanInvitation(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return items, nil
}

// CreateOrgInvitation records a pending invitation for an organization. Only
// owners and admins may invite; ErrConflict means the email already has a
// pending invitation.
func (s *Store) CreateOrgInvitation(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
	input domain.CreateInvitation,
) (domain.Invitation, error) {
	var invite domain.Invitation
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		if err := requireOrgAdmin(ctx, tx, orgID, principal.UserID); err != nil {
			return err
		}
		var invitationID string
		if err := tx.QueryRow(
			ctx,
			`INSERT INTO ao_org_invitations (
				org_id, email, invited_user_id, invited_by_user_id, role, expires_at
			)
			VALUES (
				$1,
				lower($2),
				(SELECT id FROM ao_users WHERE lower(email) = lower($2) LIMIT 1),
				$3,
				$4,
				now() + interval '14 days'
			)
			RETURNING id`,
			orgID, input.Email, principal.UserID, input.Role,
		).Scan(&invitationID); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.ConstraintName == "ao_org_invitations_pending_email_key" {
				return ErrConflict
			}
			return fmt.Errorf("create org invitation: %w", err)
		}
		record, err := scanInvitation(tx.QueryRow(
			ctx,
			`SELECT `+invitationColumns+`
			FROM ao_org_invitations invite
			LEFT JOIN ao_users inviter ON inviter.id = invite.invited_by_user_id
			WHERE invite.id = $1`,
			invitationID,
		))
		if err != nil {
			return fmt.Errorf("load created invitation: %w", err)
		}
		invite = record
		return nil
	})
	if err != nil {
		return domain.Invitation{}, err
	}
	return invite, nil
}

// RevokeOrgInvitation cancels a pending invitation. Only owners and admins
// may revoke.
func (s *Store) RevokeOrgInvitation(
	ctx context.Context,
	principal domain.Principal,
	orgID, invitationID string,
) error {
	return s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		if err := requireOrgAdmin(ctx, tx, orgID, principal.UserID); err != nil {
			return err
		}
		tag, err := tx.Exec(
			ctx,
			`UPDATE ao_org_invitations
			SET status = 'revoked', revoked_at = now(), updated_at = now()
			WHERE id = $1 AND org_id = $2 AND status = 'pending'`,
			invitationID, orgID,
		)
		if err != nil {
			return fmt.Errorf("revoke org invitation: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// GetOrgInvitation loads a pending, unexpired invitation addressed to the
// calling principal (by prior account link or by email). It does not require
// the caller to already be a member of orgID — accepting or declining an
// invitation is exactly how a non-member becomes one.
func (s *Store) GetOrgInvitation(
	ctx context.Context,
	principal domain.Principal,
	orgID, invitationID string,
) (domain.Invitation, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return domain.Invitation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('ao.org_id', $1, true)`, orgID); err != nil {
		return domain.Invitation{}, err
	}
	invite, err := scanInvitation(tx.QueryRow(
		ctx,
		`SELECT `+invitationColumns+`
		FROM ao_org_invitations invite
		LEFT JOIN ao_users inviter ON inviter.id = invite.invited_by_user_id
		WHERE invite.id = $1
		  AND invite.org_id = $2
		  AND invite.status = 'pending'
		  AND invite.expires_at > now()
		  AND (invite.invited_user_id = $3 OR lower(invite.email) = lower($4))`,
		invitationID, orgID, principal.UserID, principal.Email,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Invitation{}, ErrNotFound
	}
	if err != nil {
		return domain.Invitation{}, fmt.Errorf("get org invitation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Invitation{}, err
	}
	return invite, nil
}

// AcceptOrgInvitation accepts a pending invitation addressed to the calling
// principal and activates their membership in the invitation's organization,
// at the role the invitation specified.
func (s *Store) AcceptOrgInvitation(
	ctx context.Context,
	principal domain.Principal,
	orgID, invitationID string,
) (domain.Membership, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Membership{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(
		ctx,
		`SELECT set_config('ao.user_id', $1, true), set_config('ao.org_id', $2, true)`,
		principal.UserID, orgID,
	); err != nil {
		return domain.Membership{}, err
	}
	var role string
	err = tx.QueryRow(
		ctx,
		`UPDATE ao_org_invitations
		SET status = 'accepted', invited_user_id = $3, accepted_at = now(), updated_at = now()
		WHERE id = $1
		  AND org_id = $2
		  AND status = 'pending'
		  AND expires_at > now()
		  AND (invited_user_id = $3 OR lower(email) = lower($4))
		RETURNING role`,
		invitationID, orgID, principal.UserID, principal.Email,
	).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Membership{}, ErrNotFound
	}
	if err != nil {
		return domain.Membership{}, fmt.Errorf("accept org invitation: %w", err)
	}
	var membership domain.Membership
	membership.OrgID = orgID
	membership.Role = role
	if _, err := tx.Exec(
		ctx,
		`INSERT INTO ao_org_memberships (org_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (org_id, user_id) DO UPDATE
		SET role = EXCLUDED.role, status = 'active', updated_at = now()`,
		orgID, principal.UserID, role,
	); err != nil {
		return domain.Membership{}, fmt.Errorf("create org membership: %w", err)
	}
	if err := tx.QueryRow(
		ctx,
		`SELECT slug, display_name FROM ao_organizations WHERE id = $1`,
		orgID,
	).Scan(&membership.OrgSlug, &membership.DisplayName); err != nil {
		return domain.Membership{}, fmt.Errorf("load accepted organization: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Membership{}, fmt.Errorf("commit accept org invitation: %w", err)
	}
	return membership, nil
}

// DeclineOrgInvitation marks a pending invitation declined by its invitee.
func (s *Store) DeclineOrgInvitation(
	ctx context.Context,
	principal domain.Principal,
	orgID, invitationID string,
) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('ao.org_id', $1, true)`, orgID); err != nil {
		return err
	}
	tag, err := tx.Exec(
		ctx,
		`UPDATE ao_org_invitations
		SET status = 'declined', declined_at = now(), updated_at = now()
		WHERE id = $1
		  AND org_id = $2
		  AND status = 'pending'
		  AND (invited_user_id = $3 OR lower(email) = lower($4))`,
		invitationID, orgID, principal.UserID, principal.Email,
	)
	if err != nil {
		return fmt.Errorf("decline org invitation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}
