-- +goose Up

ALTER TABLE ao_sessions
    ADD COLUMN parent_session_id UUID,
    ADD CONSTRAINT ao_sessions_parent_not_self
        CHECK (parent_session_id IS NULL OR parent_session_id <> id),
    ADD CONSTRAINT ao_sessions_parent_project_fk
        FOREIGN KEY (org_id, project_id, parent_session_id)
        REFERENCES ao_sessions(org_id, project_id, id)
        ON DELETE SET NULL (parent_session_id);

CREATE INDEX ao_sessions_parent_updated_idx
    ON ao_sessions(org_id, parent_session_id, updated_at DESC, id DESC)
    WHERE parent_session_id IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS ao_sessions_parent_updated_idx;

ALTER TABLE ao_sessions
    DROP CONSTRAINT IF EXISTS ao_sessions_parent_project_fk,
    DROP CONSTRAINT IF EXISTS ao_sessions_parent_not_self,
    DROP COLUMN IF EXISTS parent_session_id;
