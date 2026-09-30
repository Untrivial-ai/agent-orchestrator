package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
)

// UpdateProjectSettings merges under a row lock so concurrent partial writes
// cannot overwrite settings they did not supply.
func (s *Store) UpdateProjectSettings(ctx context.Context, principal domain.Principal, orgID, projectID string, input domain.ProjectSettingsPatch) (domain.Project, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return domain.Project{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	input, err = domain.ParseProjectSettingsPatch(raw)
	if err != nil {
		return domain.Project{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	var project domain.Project
	err = s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		if err := scanProject(tx.QueryRow(ctx,
			`SELECT id, org_id, display_name, repository_url, default_branch,
				github_repository_id, config, created_at, updated_at
			FROM ao_projects WHERE org_id = $1 AND id = $2 AND archived_at IS NULL FOR UPDATE`,
			orgID, projectID), &project); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		config, err := domain.MergeProjectSettingsConfig(project.Config, input.Config)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		if input.DisplayName != nil {
			project.DisplayName = *input.DisplayName
		}
		if input.DefaultBranch != nil {
			project.DefaultBranch = *input.DefaultBranch
		}
		if err := scanProject(tx.QueryRow(ctx,
			`UPDATE ao_projects SET display_name = $3, default_branch = $4, config = $5, updated_at = now()
			WHERE org_id = $1 AND id = $2
			RETURNING id, org_id, display_name, repository_url, default_branch,
				github_repository_id, config, created_at, updated_at`,
			orgID, projectID, project.DisplayName, project.DefaultBranch, config), &project); err != nil {
			return normalizeConstraintError(err)
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO ao_audit_events (org_id, actor_user_id, action, resource_type, resource_id)
			VALUES ($1, $2, 'project.settings.updated', 'project', $3)`, orgID, principal.UserID, projectID)
		return err
	})
	return project, err
}
