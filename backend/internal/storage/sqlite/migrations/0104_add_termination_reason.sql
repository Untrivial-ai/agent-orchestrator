-- +goose Up
-- +goose StatementBegin

ALTER TABLE sessions ADD COLUMN termination_reason TEXT NOT NULL DEFAULT '';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE sessions DROP COLUMN termination_reason;

-- +goose StatementEnd
