-- +goose Up

-- Deleting a project archives its row (00013) so session history, events and the
-- audit trail survive it. The founding schema's UNIQUE (org_id, repository_url)
-- predates archiving and still counts archived rows, so a deleted project kept
-- its repository URL reserved forever: creating a new project for that
-- repository failed with a conflict that no visible project explained, and the
-- URL could never be used again in that organization.
--
-- Scope the rule to live projects, the way ao_org_invitations_pending_email_key
-- already scopes uniqueness to pending invitations. One active project per
-- repository per organization still holds; archived rows no longer reserve it.
ALTER TABLE ao_projects
    DROP CONSTRAINT IF EXISTS ao_projects_org_id_repository_url_key;

CREATE UNIQUE INDEX IF NOT EXISTS ao_projects_org_active_repository_url_key
    ON ao_projects(org_id, repository_url)
    WHERE archived_at IS NULL;

-- +goose Down

DROP INDEX IF EXISTS ao_projects_org_active_repository_url_key;

-- Restoring the original constraint requires that no organization holds both an
-- archived and a live project for the same repository, which this migration
-- deliberately allows. Resolve any such pair before rolling back.
ALTER TABLE ao_projects
    ADD CONSTRAINT ao_projects_org_id_repository_url_key UNIQUE (org_id, repository_url);
