-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
-- SQLite has no ALTER COLUMN syntax. Rebuilding these parent tables would
-- temporarily invalidate the many CDC triggers that read them, so widen only
-- the four project_id declarations in sqlite_schema and force a schema reload.
-- The replacement is deliberately exact and guarded by a postcondition.
PRAGMA writable_schema=ON;

UPDATE sqlite_schema
SET sql = replace(
    replace(
        replace(
            sql,
            'project_id              TEXT NOT NULL',
            'project_id              TEXT'
        ),
        'project_id      TEXT NOT NULL',
        'project_id      TEXT'
    ),
    'project_id TEXT NOT NULL',
    'project_id TEXT'
)
WHERE type = 'table'
  AND name IN ('sessions', 'change_log', 'notifications', 'conversations');

-- RESET disables writable_schema and forces this connection to discard its
-- cached schema before goose records the migration and the store starts.
PRAGMA writable_schema=RESET;

CREATE TEMP TABLE standalone_schema_guard (
    nullable_project_columns INTEGER CHECK (nullable_project_columns = 4)
);
INSERT INTO standalone_schema_guard
SELECT
    (SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'project_id' AND "notnull" = 0) +
    (SELECT COUNT(*) FROM pragma_table_info('change_log') WHERE name = 'project_id' AND "notnull" = 0) +
    (SELECT COUNT(*) FROM pragma_table_info('notifications') WHERE name = 'project_id' AND "notnull" = 0) +
    (SELECT COUNT(*) FROM pragma_table_info('conversations') WHERE name = 'project_id' AND "notnull" = 0);
DROP TABLE standalone_schema_guard;

-- Existing first-run Scratch sessions become genuine projectless sessions.
-- Their workspace paths remain unchanged so live/restorable work is preserved.
UPDATE sessions SET project_id = NULL WHERE project_id = 'scratch';
UPDATE change_log SET project_id = NULL WHERE project_id = 'scratch';
UPDATE notifications SET project_id = NULL WHERE project_id = 'scratch';
UPDATE conversations
SET project_id = NULL
WHERE scope = 'session' AND project_id = 'scratch';

UPDATE projects
SET archived_at = COALESCE(archived_at, datetime('now'))
WHERE id = 'scratch';

PRAGMA foreign_key_check;
-- +goose StatementEnd

-- +goose Down
-- Standalone sessions cannot be losslessly reattached to a repository. This
-- migration is intentionally forward-only.
