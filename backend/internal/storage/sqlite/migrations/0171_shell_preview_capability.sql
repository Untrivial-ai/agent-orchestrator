-- +goose Up
-- +goose StatementBegin
ALTER TABLE shell_terminals ADD COLUMN preview_capability_verifier TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE shell_terminals DROP COLUMN preview_capability_verifier;
-- +goose StatementEnd
