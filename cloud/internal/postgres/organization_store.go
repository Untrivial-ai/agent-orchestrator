package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Store) CreateOrganization(
	ctx context.Context,
	principal domain.Principal,
	displayName string,
) (domain.Membership, error) {
	orgID := uuid.NewString()
	slug := "workspace-" + strings.ReplaceAll(orgID, "-", "")
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Membership{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`SELECT set_config('ao.user_id', $1, true), set_config('ao.org_id', $2, true)`,
		principal.UserID, orgID,
	); err != nil {
		return domain.Membership{}, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO ao_organizations (
			id, auth_provider, slug, display_name, kind, owner_user_id, created_by_user_id
		) VALUES ($1, $2, $3, $4, 'team', $5, $5)`,
		orgID, principal.Provider, slug, displayName, principal.UserID,
	); err != nil {
		return domain.Membership{}, normalizeConstraintError(err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO ao_org_memberships (org_id, user_id, role) VALUES ($1, $2, 'owner')`,
		orgID, principal.UserID,
	); err != nil {
		return domain.Membership{}, normalizeConstraintError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Membership{}, err
	}
	return domain.Membership{OrgID: orgID, OrgSlug: slug, DisplayName: displayName, Role: "owner"}, nil
}

func (s *Store) UpdateOrgMemberRole(
	ctx context.Context,
	principal domain.Principal,
	orgID, userID, role string,
) (domain.OrgMember, error) {
	var member domain.OrgMember
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		if err := requireOrgAdmin(ctx, tx, orgID, principal.UserID); err != nil {
			return err
		}
		var currentRole string
		if err := tx.QueryRow(ctx,
			`SELECT role FROM ao_org_memberships
			 WHERE org_id = $1 AND user_id = $2 AND status = 'active' FOR UPDATE`,
			orgID, userID,
		).Scan(&currentRole); err != nil {
			if err == pgx.ErrNoRows {
				return ErrNotFound
			}
			return err
		}
		if currentRole == "owner" && role != "owner" {
			var owners int
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM ao_org_memberships
				 WHERE org_id = $1 AND role = 'owner' AND status = 'active'`, orgID,
			).Scan(&owners); err != nil {
				return err
			}
			if owners <= 1 {
				return ErrConflict
			}
		}
		return tx.QueryRow(ctx,
			`UPDATE ao_org_memberships membership SET role = $3, updated_at = now()
			 FROM ao_users users
			 WHERE membership.org_id = $1 AND membership.user_id = $2
			   AND membership.user_id = users.id AND membership.status = 'active'
			 RETURNING users.id, users.email, users.display_name, membership.role, membership.created_at`,
			orgID, userID, role,
		).Scan(&member.UserID, &member.Email, &member.DisplayName, &member.Role, &member.CreatedAt)
	})
	if err != nil {
		return domain.OrgMember{}, fmt.Errorf("update organization member role: %w", err)
	}
	return member, nil
}
