-- +goose Up

ALTER TABLE ao_projects
    ADD COLUMN archived_at TIMESTAMPTZ;

CREATE INDEX ao_projects_org_active_created_idx
    ON ao_projects(org_id, created_at DESC, id DESC)
    WHERE archived_at IS NULL;

-- +goose Down

DROP INDEX IF EXISTS ao_projects_org_active_created_idx;
ALTER TABLE ao_projects DROP COLUMN IF EXISTS archived_at;
