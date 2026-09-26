-- +goose Up

ALTER TABLE ao_sessions
	ADD COLUMN auto_review_enabled BOOLEAN NOT NULL DEFAULT false;

-- +goose Down

ALTER TABLE ao_sessions DROP COLUMN auto_review_enabled;
