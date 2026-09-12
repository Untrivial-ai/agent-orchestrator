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

-- Only the built-in Scratch project has both this reserved id and kind. A
-- user-registered repository may legitimately have the id "scratch" and must
-- remain untouched. Preserve Scratch workers as standalone sessions, but end
-- the legacy orchestrator: projectless orchestrators are not a supported state
-- and startup reconciliation would otherwise relaunch it after migration.
CREATE TEMP TABLE legacy_scratch_workers (
    session_id TEXT PRIMARY KEY
);
INSERT INTO legacy_scratch_workers (session_id)
SELECT sessions.id
FROM sessions
JOIN projects ON projects.id = sessions.project_id
WHERE projects.id = 'scratch'
  AND projects.kind = 'scratch'
  AND sessions.kind = 'worker';

UPDATE sessions
SET is_terminated = TRUE,
    activity_state = 'exited',
    updated_at = datetime('now')
WHERE project_id = 'scratch'
  AND kind = 'orchestrator'
  AND EXISTS (
      SELECT 1 FROM projects
      WHERE id = 'scratch' AND kind = 'scratch'
  );

UPDATE sessions
SET project_id = NULL
WHERE id IN (SELECT session_id FROM legacy_scratch_workers);
UPDATE change_log
SET project_id = NULL
WHERE session_id IN (SELECT session_id FROM legacy_scratch_workers);
UPDATE notifications
SET project_id = NULL
WHERE session_id IN (SELECT session_id FROM legacy_scratch_workers);
UPDATE conversations
SET project_id = NULL
WHERE scope = 'session'
  AND session_id IN (SELECT session_id FROM legacy_scratch_workers);

UPDATE projects
SET archived_at = COALESCE(archived_at, datetime('now'))
WHERE id = 'scratch' AND kind = 'scratch';

DROP TABLE legacy_scratch_workers;

PRAGMA foreign_key_check;
-- +goose StatementEnd

-- +goose Down
-- Standalone sessions cannot be losslessly reattached to a repository. This
-- migration is intentionally forward-only.
