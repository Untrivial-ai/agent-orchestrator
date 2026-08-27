-- +goose Up
-- +goose StatementBegin
ALTER TABLE review ADD COLUMN reviewer_activity_state TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE review DROP COLUMN reviewer_activity_state;
-- +goose StatementEnd
