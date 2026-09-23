-- +goose Up
ALTER TABLE ao_sessions
    ADD COLUMN activity_blocked_tool_name TEXT NOT NULL DEFAULT ''
        CHECK (length(activity_blocked_tool_name) <= 256),
    ADD COLUMN activity_blocked_tool_use_id TEXT NOT NULL DEFAULT ''
        CHECK (length(activity_blocked_tool_use_id) <= 256);

-- +goose Down
ALTER TABLE ao_sessions
    DROP COLUMN activity_blocked_tool_use_id,
    DROP COLUMN activity_blocked_tool_name;
