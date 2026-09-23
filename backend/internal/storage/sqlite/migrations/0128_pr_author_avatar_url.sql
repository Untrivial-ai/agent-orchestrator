-- Summary: persist the optional provider-hosted profile image for a pull request author.
-- +goose Up
-- +goose StatementBegin
ALTER TABLE pr ADD COLUMN author_avatar_url TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE pr DROP COLUMN author_avatar_url;
-- +goose StatementEnd
