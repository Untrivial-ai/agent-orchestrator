-- +goose Up
ALTER TABLE sessions ADD COLUMN revision INTEGER NOT NULL DEFAULT 0;

-- Database-owned so all writers, including projection triggers, participate.
-- The guard also prevents recursion when recursive_triggers is enabled.
-- Read revisions with SELECT; UPDATE RETURNING runs before this AFTER trigger.
-- +goose StatementBegin
CREATE TRIGGER sessions_revision_update
AFTER UPDATE ON sessions
WHEN NEW.revision = OLD.revision
BEGIN
    UPDATE sessions SET revision = OLD.revision + 1 WHERE id = NEW.id;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER sessions_revision_update;
ALTER TABLE sessions DROP COLUMN revision;
