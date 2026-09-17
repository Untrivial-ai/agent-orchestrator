-- +goose Up

-- Archived projects remain in PostgreSQL for audit and session history, but
-- they must not reserve a repository forever. A repository can be registered
-- again once its previous project has been archived.
ALTER TABLE ao_projects
    DROP CONSTRAINT IF EXISTS ao_projects_org_id_repository_url_key;

CREATE UNIQUE INDEX ao_projects_org_active_repository_url_key
    ON ao_projects(org_id, repository_url)
    WHERE archived_at IS NULL;

-- +goose Down

DROP INDEX IF EXISTS ao_projects_org_active_repository_url_key;

ALTER TABLE ao_projects
    ADD CONSTRAINT ao_projects_org_id_repository_url_key
    UNIQUE (org_id, repository_url);
