-- +goose Up
-- +goose StatementBegin
-- A stop time is a lifecycle fact. Existing terminated rows have no proven stop
-- time, so they stay NULL and cannot enter age-based retention automatically.
ALTER TABLE sessions ADD COLUMN stopped_at TIMESTAMP;
ALTER TABLE sessions ADD COLUMN history_sort_epoch INTEGER NOT NULL DEFAULT 0;
-- modernc/sqlite stores Go time.Time with a timezone suffix SQLite's date
-- parser does not accept. The leading YYYY-MM-DD HH:MM:SS is stable UTC.
UPDATE sessions SET history_sort_epoch = COALESCE(CAST(strftime('%s', substr(created_at, 1, 19)) AS INTEGER), 0);

CREATE INDEX idx_sessions_history ON sessions (is_terminated, history_sort_epoch DESC, id DESC);
CREATE INDEX idx_sessions_project_history ON sessions (is_terminated, project_id, history_sort_epoch DESC, id DESC);

CREATE TRIGGER sessions_history_stop AFTER UPDATE OF is_terminated ON sessions
WHEN OLD.is_terminated = 0 AND NEW.is_terminated = 1
BEGIN
    UPDATE sessions SET stopped_at = NEW.updated_at,
        history_sort_epoch = COALESCE(CAST(strftime('%s', substr(NEW.updated_at, 1, 19)) AS INTEGER), 0)
    WHERE id = NEW.id;
END;

CREATE TRIGGER sessions_history_restore AFTER UPDATE OF is_terminated ON sessions
WHEN OLD.is_terminated = 1 AND NEW.is_terminated = 0
BEGIN
    UPDATE sessions SET stopped_at = NULL,
        history_sort_epoch = COALESCE(CAST(strftime('%s', substr(NEW.created_at, 1, 19)) AS INTEGER), 0)
    WHERE id = NEW.id;
END;

CREATE TRIGGER sessions_history_initial_stop AFTER INSERT ON sessions
WHEN NEW.is_terminated = 1
BEGIN
    UPDATE sessions SET stopped_at = NEW.updated_at,
        history_sort_epoch = COALESCE(CAST(strftime('%s', substr(NEW.updated_at, 1, 19)) AS INTEGER), 0)
    WHERE id = NEW.id;
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER sessions_history_initial_stop;
DROP TRIGGER sessions_history_restore;
DROP TRIGGER sessions_history_stop;
DROP INDEX idx_sessions_project_history;
DROP INDEX idx_sessions_history;
ALTER TABLE sessions DROP COLUMN history_sort_epoch;
ALTER TABLE sessions DROP COLUMN stopped_at;
-- +goose StatementEnd
