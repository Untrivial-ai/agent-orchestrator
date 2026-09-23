-- +goose Up
-- +goose StatementBegin

-- Clearing a notification hides it without discarding the open dedupe fact.
-- The row stays until its underlying condition resolves, so repeated SCM
-- observations cannot recreate a notification the user already dismissed.
ALTER TABLE notifications ADD COLUMN dismissed_at TIMESTAMP;

-- Visible history is the hot path. Keep page and count queries on the live
-- subset even when many dismissed dedupe rows are waiting for resolution.
CREATE INDEX idx_notifications_live_history
    ON notifications(created_at DESC, id DESC)
    WHERE dismissed_at IS NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Older builds hard-delete cleared notifications. Match that behavior when
-- rolling back instead of making dismissed history visible again.
DELETE FROM notifications WHERE dismissed_at IS NOT NULL;
DROP INDEX IF EXISTS idx_notifications_live_history;
ALTER TABLE notifications DROP COLUMN dismissed_at;

-- +goose StatementEnd
