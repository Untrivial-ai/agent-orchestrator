-- +goose Up
-- +goose StatementBegin

-- Clearing a notification hides it without discarding the open dedupe fact.
-- The row stays until its underlying condition resolves, so repeated SCM
-- observations cannot recreate a notification the user already dismissed.
ALTER TABLE notifications ADD COLUMN dismissed_at TIMESTAMP;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Older builds hard-delete cleared notifications. Match that behavior when
-- rolling back instead of making dismissed history visible again.
DELETE FROM notifications WHERE dismissed_at IS NOT NULL;
ALTER TABLE notifications DROP COLUMN dismissed_at;

-- +goose StatementEnd
