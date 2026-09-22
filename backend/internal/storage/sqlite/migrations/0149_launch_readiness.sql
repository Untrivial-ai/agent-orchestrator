-- Summary: persist readiness evidence for the current launch and conversation.
-- +goose Up
-- +goose StatementBegin
ALTER TABLE sessions ADD COLUMN launch_readiness_state TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN launch_readiness_launch_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN launch_readiness_conversation_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN launch_readiness_cause TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN launch_readiness_resume BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE sessions ADD COLUMN launch_readiness_updated_at DATETIME;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE sessions DROP COLUMN launch_readiness_updated_at;
ALTER TABLE sessions DROP COLUMN launch_readiness_resume;
ALTER TABLE sessions DROP COLUMN launch_readiness_cause;
ALTER TABLE sessions DROP COLUMN launch_readiness_conversation_id;
ALTER TABLE sessions DROP COLUMN launch_readiness_launch_id;
ALTER TABLE sessions DROP COLUMN launch_readiness_state;
-- +goose StatementEnd
