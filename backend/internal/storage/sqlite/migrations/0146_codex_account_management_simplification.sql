-- Summary: keep Codex account switching local and derive the active account from auth.json.
-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
PRAGMA foreign_keys=OFF;

-- Pre-credential legacy phases can be cancelled. Phases that may have
-- installed a credential are retained for local startup settlement.
UPDATE codex_account_switches
SET phase = 'failed',
    failure_code = 'legacy_session_switch_retired',
    updated_at = CURRENT_TIMESTAMP,
    completed_at = COALESCE(completed_at, CURRENT_TIMESTAMP)
WHERE phase IN ('stopping_sessions', 'sessions_stopped')
   OR (phase = 'recovery_required' AND failure_code = 'stop_unconfirmed');

UPDATE codex_account_switches
SET phase = 'recovery_required',
    failure_code = 'legacy_switch_recovery',
    updated_at = CURRENT_TIMESTAMP,
    completed_at = NULL
WHERE phase IN ('restarting_sessions', 'verifying_target', 'rollback_required');

DROP TRIGGER IF EXISTS codex_account_switch_sessions_cdc_insert;
DROP TRIGGER IF EXISTS codex_account_switch_sessions_cdc;
DROP TRIGGER IF EXISTS codex_account_switches_cdc_update;
DROP TRIGGER IF EXISTS codex_account_switches_cdc_insert;
DROP TABLE IF EXISTS codex_account_switch_sessions;
DROP INDEX IF EXISTS idx_codex_account_switches_one_active;

CREATE TABLE codex_account_switches_next (
    id TEXT PRIMARY KEY,
    source_account_id TEXT NOT NULL,
    target_account_id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL UNIQUE,
    phase TEXT NOT NULL CHECK (phase IN (
        'requested', 'checkpointing_source', 'activating_target',
        'recovery_required', 'completed', 'failed'
    )),
    failure_code TEXT NOT NULL DEFAULT '',
    credentials_committed_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    completed_at TIMESTAMP,
    source_kind TEXT NOT NULL DEFAULT 'managed'
        CHECK (source_kind IN ('managed', 'device', 'none'))
);

INSERT INTO codex_account_switches_next (
    id, source_account_id, target_account_id, idempotency_key,
    phase, failure_code, credentials_committed_at,
    created_at, updated_at, completed_at, source_kind
)
SELECT
    id, source_account_id, target_account_id, idempotency_key,
    phase, failure_code, credentials_committed_at,
    created_at, updated_at, completed_at, 'managed'
FROM codex_account_switches;

DROP TABLE codex_account_switches;
ALTER TABLE codex_account_switches_next RENAME TO codex_account_switches;

CREATE UNIQUE INDEX idx_codex_account_switches_one_active
ON codex_account_switches((1))
WHERE phase NOT IN ('completed', 'failed');

DROP TRIGGER IF EXISTS codex_active_account_cdc_insert;
DROP TRIGGER IF EXISTS codex_active_account_cdc_update;
DROP TRIGGER IF EXISTS codex_active_account_cdc_delete;
DROP TABLE IF EXISTS codex_active_account;

PRAGMA foreign_keys=ON;
PRAGMA foreign_key_check;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Removed active-pointer and session-restart state is intentionally not
-- restored. Device auth.json remains the active-account authority.
SELECT 1;
-- +goose StatementEnd
