-- +goose Up
-- Track which TUI input optimistically marked a session active. A failed
-- delivery may clear only its own activity, regardless of unrelated edits to
-- the session row's updated_at timestamp.
ALTER TABLE ao_sessions
    ADD COLUMN activity_source_request_id UUID;

-- +goose Down
ALTER TABLE ao_sessions
    DROP COLUMN activity_source_request_id;
