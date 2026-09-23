-- +goose Up

-- last_user_message_at is distinct from updated_at: updated_at is bumped by
-- every session event (agent turns included), so it cannot tell a quiet user
-- apart from a busy agent. This column is written only where a user message is
-- appended, so the control plane's idle-pause scan can measure true user
-- silence. Backfilled from updated_at so already-idle sessions become eligible
-- for pause as soon as this ships rather than waiting a full idle window past
-- their real last activity.
ALTER TABLE ao_sessions ADD COLUMN last_user_message_at TIMESTAMPTZ;
UPDATE ao_sessions SET last_user_message_at = updated_at;

-- +goose Down
ALTER TABLE ao_sessions DROP COLUMN last_user_message_at;
