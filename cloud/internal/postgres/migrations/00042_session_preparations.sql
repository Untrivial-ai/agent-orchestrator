-- +goose Up

ALTER TABLE ao_sessions
    ADD COLUMN is_preparation BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN preparation_expires_at TIMESTAMPTZ;

ALTER TABLE ao_sessions
    ADD CONSTRAINT ao_sessions_preparation_expiry_check CHECK (
        (is_preparation AND preparation_expires_at IS NOT NULL)
        OR (NOT is_preparation AND preparation_expires_at IS NULL)
    );

ALTER TABLE ao_sandboxes
    ADD COLUMN preparation_expires_at TIMESTAMPTZ;

CREATE INDEX ao_sandboxes_preparation_expiry_idx
    ON ao_sandboxes(preparation_expires_at)
    WHERE preparation_expires_at IS NOT NULL AND desired_state <> 'deleted';

-- +goose Down

DROP INDEX IF EXISTS ao_sandboxes_preparation_expiry_idx;
ALTER TABLE ao_sandboxes
    DROP COLUMN IF EXISTS preparation_expires_at;
ALTER TABLE ao_sessions
    DROP CONSTRAINT IF EXISTS ao_sessions_preparation_expiry_check,
    DROP COLUMN IF EXISTS preparation_expires_at,
    DROP COLUMN IF EXISTS is_preparation;
