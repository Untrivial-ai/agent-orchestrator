-- +goose Up
-- The recovery migration already carries all of the current agent_switches
-- columns and indexes. Extend it in place rather than rebuilding the table:
-- this preserves its existing triggers and keeps startup migrations fast.
ALTER TABLE agent_switches
    ADD COLUMN failure_point TEXT NOT NULL DEFAULT '';

-- 0094 allowed this retained marker in a failed row. It represents an
-- unresolved source-stop outcome, but a legacy terminal row did not occupy
-- the one-active-switch gate. If a later active switch already owns that gate,
-- or several historical markers exist, retain the newest recoverable marker
-- only and make every older conflicting row terminal.
UPDATE agent_switches
SET error_code = 'failed_post_stop'
WHERE state = 'failed'
  AND error_code = 'source_stop_unconfirmed'
  AND (
      EXISTS (
          SELECT 1
          FROM agent_switches AS active
          WHERE active.session_id = agent_switches.session_id
            AND active.state NOT IN ('completed', 'failed')
      )
      OR EXISTS (
          SELECT 1
          FROM agent_switches AS newer
          WHERE newer.session_id = agent_switches.session_id
            AND newer.state = 'failed'
            AND newer.error_code = 'source_stop_unconfirmed'
            AND (
                newer.requested_at > agent_switches.requested_at
                OR (
                    newer.requested_at = agent_switches.requested_at
                    AND newer.id > agent_switches.id
                )
            )
      )
  );

-- Each remaining marker is the only candidate for its session, so promoting
-- it preserves the recovery gate without violating its unique index.
UPDATE agent_switches
SET state = 'stopping_source'
WHERE state = 'failed'
  AND error_code = 'source_stop_unconfirmed';

-- SQLite cannot amend an existing CHECK constraint. These triggers complete
-- the corrected invariant at the same database boundary for future writes.
-- +goose StatementBegin
CREATE TRIGGER agent_switches_failed_recovery_marker_insert
BEFORE INSERT ON agent_switches
WHEN NEW.state = 'failed'
    AND NEW.error_code IN (
        'source_stop_unconfirmed',
        'source_restore_unconfirmed',
        'target_start_unconfirmed'
    )
BEGIN
    SELECT RAISE(ABORT, 'agent switch recovery marker requires a nonterminal state');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER agent_switches_failed_recovery_marker_update
BEFORE UPDATE ON agent_switches
WHEN NEW.state = 'failed'
    AND NEW.error_code IN (
        'source_stop_unconfirmed',
        'source_restore_unconfirmed',
        'target_start_unconfirmed'
    )
BEGIN
    SELECT RAISE(ABORT, 'agent switch recovery marker requires a nonterminal state');
END;
-- +goose StatementEnd

CREATE TABLE agent_switch_failure_policy (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    consent_generation TEXT NOT NULL,
    destination_fingerprint TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL
);

INSERT INTO agent_switch_failure_policy (
    singleton, enabled, consent_generation, destination_fingerprint, updated_at
) VALUES (1, 0, '', '', CURRENT_TIMESTAMP);

CREATE TABLE agent_switch_failure_receipts (
    dedupe_key TEXT PRIMARY KEY,
    switch_id TEXT REFERENCES agent_switches(id) ON DELETE CASCADE,
    report_kind TEXT NOT NULL,
    durable_state_fingerprint TEXT NOT NULL,
    recorded_at TIMESTAMP NOT NULL,
    retain_until TIMESTAMP
);

CREATE TABLE agent_switch_failure_outbox (
    id TEXT PRIMARY KEY,
    schema_version INTEGER NOT NULL,
    envelope_encoding_version INTEGER NOT NULL,
    dedupe_key TEXT NOT NULL UNIQUE,
    destination_fingerprint TEXT NOT NULL,
    switch_id TEXT,
    report_kind TEXT NOT NULL,
    scope TEXT NOT NULL,
    failure_point TEXT NOT NULL,
    classifier_callsite TEXT NOT NULL,
    phase TEXT NOT NULL,
    error_code TEXT NOT NULL,
    fault_code TEXT NOT NULL,
    execution TEXT NOT NULL,
    execution_attempt_id TEXT NOT NULL,
    mode TEXT NOT NULL,
    from_harness TEXT NOT NULL,
    target_harness TEXT NOT NULL,
    target_start_mode TEXT NOT NULL,
    runtime_backend TEXT NOT NULL,
    call_outcome TEXT NOT NULL,
    ownership TEXT NOT NULL,
    compensation TEXT NOT NULL,
    user_impact TEXT NOT NULL,
    source_stop_confirmed TEXT NOT NULL,
    target_owner_committed TEXT NOT NULL,
    gate_retained TEXT NOT NULL,
    requested_at TIMESTAMP,
    occurred_at TIMESTAMP NOT NULL,
    sanitized_stack BLOB NOT NULL,
    stack_fingerprint TEXT NOT NULL,
    canonical_event_json BLOB NOT NULL CHECK (length(canonical_event_json) <= 61440),
    expires_at TIMESTAMP NOT NULL,
    available_at TIMESTAMP NOT NULL,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    last_attempt_at TIMESTAMP,
    lease_token TEXT,
    lease_consent_generation TEXT,
    lease_delivery_epoch INTEGER,
    lease_expires_at TIMESTAMP,
    delivered_at TIMESTAMP,
    discarded_at TIMESTAMP,
    last_delivery_error_class TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_agent_switch_failure_outbox_pending
    ON agent_switch_failure_outbox(available_at, occurred_at)
    WHERE delivered_at IS NULL AND discarded_at IS NULL;

CREATE TABLE agent_switch_failure_delivery_state (
    destination_fingerprint TEXT PRIMARY KEY,
    error_not_before TIMESTAMP,
    all_not_before TIMESTAMP
);

-- +goose Down
-- +goose StatementBegin
-- Rows may depend on the corrected retained-marker invariant and failure
-- payload tables, so a safe downgrade is intentionally unavailable.
SELECT 1;
-- +goose StatementEnd
