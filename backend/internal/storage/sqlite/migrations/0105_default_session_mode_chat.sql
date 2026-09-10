-- +goose Up
UPDATE app_settings
SET default_session_mode = 'chat', updated_at = CURRENT_TIMESTAMP
WHERE id = 1;

-- +goose Down
-- The previous preference cannot be reconstructed safely. Preserve it rather
-- than replacing a deliberate Chat choice during rollback.
SELECT 1;
