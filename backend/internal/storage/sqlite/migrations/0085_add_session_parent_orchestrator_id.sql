-- Persist which orchestrator session (if any) spawned a given session, so the
-- desktop UI can link a worker's "Orchestrator" button to its actual parent
-- instead of always guessing "the newest active orchestrator in the project"
-- (issue #1211). Set once at spawn time by the session service; never updated
-- afterwards, mirroring session_mode's immutable-after-spawn treatment below.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE sessions
    ADD COLUMN parent_orchestrator_id TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE sessions DROP COLUMN parent_orchestrator_id;
-- +goose StatementEnd
