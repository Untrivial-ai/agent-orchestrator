-- +goose Up
-- Legacy rows have no reliable command/shell discriminator. Preserve them rather
-- than risk destroying user work; new trusted command terminals are transient.
ALTER TABLE shell_terminals ADD COLUMN transient BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE shell_terminals DROP COLUMN transient;
