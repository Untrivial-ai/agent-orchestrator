-- +goose Up
-- +goose StatementBegin
ALTER TABLE sessions ADD COLUMN model TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE sessions DROP COLUMN model;
-- +goose StatementEnd
