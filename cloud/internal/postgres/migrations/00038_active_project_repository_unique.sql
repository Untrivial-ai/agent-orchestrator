-- +goose Up

-- Deleting a project archives it (archived_at) rather than removing the row, so
-- its history survives. The founding schema's blanket UNIQUE (org_id,
-- repository_url) then wrongly blocks re-registering the same repository after
-- its project is archived. Scope uniqueness to live projects so an archived
-- project no longer reserves its repository URL.
ALTER TABLE ao_projects
    DROP CONSTRAINT IF EXISTS ao_projects_org_id_repository_url_key;
CREATE UNIQUE INDEX ao_projects_org_active_repository_url_key
    ON ao_projects(org_id, repository_url)
    WHERE archived_at IS NULL;

-- +goose Down

DROP INDEX IF EXISTS ao_projects_org_active_repository_url_key;
ALTER TABLE ao_projects
    ADD CONSTRAINT ao_projects_org_id_repository_url_key UNIQUE (org_id, repository_url);
