-- +goose Up
ALTER TABLE sessions ADD COLUMN claude_config_dir TEXT;

-- +goose Down
ALTER TABLE sessions DROP COLUMN claude_config_dir;
