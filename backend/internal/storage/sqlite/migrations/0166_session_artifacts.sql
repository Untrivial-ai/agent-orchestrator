-- +goose Up
ALTER TABLE sessions ADD COLUMN artifact_dir TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN session_output_type TEXT NOT NULL DEFAULT 'none'
    CHECK (session_output_type IN ('none', 'pr', 'artifact'));

-- +goose Down
ALTER TABLE sessions DROP COLUMN session_output_type;
ALTER TABLE sessions DROP COLUMN artifact_dir;
