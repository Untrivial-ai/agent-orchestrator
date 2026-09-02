-- +goose Up
-- +goose StatementBegin
ALTER TABLE review ADD COLUMN reviewer_launch_id TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE review DROP COLUMN reviewer_launch_id;
-- +goose StatementEnd
