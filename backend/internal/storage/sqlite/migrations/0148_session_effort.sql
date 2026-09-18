-- +goose Up
-- +goose StatementBegin
ALTER TABLE sessions ADD COLUMN effort TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE sessions DROP COLUMN effort;
-- +goose StatementEnd
