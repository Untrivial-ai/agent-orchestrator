-- +goose Up
-- Provider quotas are account-level facts. They deliberately do not reference
-- sessions: any number of conversations may share one subscription account.
CREATE TABLE quota_accounts (
    provider                 TEXT NOT NULL,
    account_id               TEXT NOT NULL,
    account_label            TEXT NOT NULL DEFAULT '',
    plan_type                TEXT NOT NULL DEFAULT '',
    auth_mode                TEXT NOT NULL DEFAULT '',
    supports_read            INTEGER NOT NULL DEFAULT 0,
    supports_subscribe       INTEGER NOT NULL DEFAULT 0,
    supports_history         INTEGER NOT NULL DEFAULT 0,
    supports_credits         INTEGER NOT NULL DEFAULT 0,
    supports_spend_limits    INTEGER NOT NULL DEFAULT 0,
    completeness             TEXT NOT NULL CHECK (completeness IN ('complete', 'partial')),
    observed_at              TIMESTAMP NOT NULL,
	last_refresh_error        TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (provider, account_id)
);

CREATE TABLE quota_limits (
    provider                 TEXT NOT NULL,
    account_id               TEXT NOT NULL,
    limit_id                 TEXT NOT NULL,
    window_type              TEXT NOT NULL DEFAULT '',
    category                 TEXT NOT NULL,
    scope                    TEXT NOT NULL,
    scope_id                 TEXT NOT NULL DEFAULT '',
    limit_name               TEXT NOT NULL DEFAULT '',
    used_percent             REAL,
    remaining_value          REAL,
    total_value              REAL,
    unit                     TEXT NOT NULL DEFAULT '',
    window_duration_seconds  INTEGER,
    resets_at                TIMESTAMP,
    reached                  INTEGER,
    reached_reason           TEXT NOT NULL DEFAULT '',
    observed_at              TIMESTAMP NOT NULL,
    PRIMARY KEY (provider, account_id, limit_id, window_type, scope, scope_id),
    FOREIGN KEY (provider, account_id) REFERENCES quota_accounts(provider, account_id) ON DELETE CASCADE
);

CREATE TABLE quota_balances (
    provider     TEXT NOT NULL,
    account_id   TEXT NOT NULL,
    balance_id   TEXT NOT NULL,
    balance_name TEXT NOT NULL DEFAULT '',
    value        TEXT NOT NULL DEFAULT '',
    currency     TEXT NOT NULL DEFAULT '',
    unlimited    INTEGER NOT NULL DEFAULT 0,
    observed_at  TIMESTAMP NOT NULL,
    PRIMARY KEY (provider, account_id, balance_id),
    FOREIGN KEY (provider, account_id) REFERENCES quota_accounts(provider, account_id) ON DELETE CASCADE
);

CREATE TABLE quota_history (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    provider      TEXT NOT NULL,
    account_id    TEXT NOT NULL,
    limit_id      TEXT NOT NULL,
    window_type   TEXT NOT NULL DEFAULT '',
    scope         TEXT NOT NULL,
    scope_id      TEXT NOT NULL DEFAULT '',
    used_percent  REAL,
    resets_at     TIMESTAMP,
    reached       INTEGER,
    observed_at   TIMESTAMP NOT NULL,
    FOREIGN KEY (provider, account_id) REFERENCES quota_accounts(provider, account_id) ON DELETE CASCADE
);

CREATE INDEX idx_quota_history_lookup
ON quota_history(provider, account_id, limit_id, window_type, observed_at DESC);

CREATE TABLE quota_alerts (
    id          TEXT PRIMARY KEY,
    provider    TEXT NOT NULL,
    account_id  TEXT NOT NULL,
    limit_id    TEXT NOT NULL DEFAULT '',
    kind        TEXT NOT NULL,
    severity    TEXT NOT NULL,
    title       TEXT NOT NULL,
    body        TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMP NOT NULL,
    FOREIGN KEY (provider, account_id) REFERENCES quota_accounts(provider, account_id) ON DELETE CASCADE
);

CREATE INDEX idx_quota_alerts_created
ON quota_alerts(created_at DESC, id DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_quota_history_lookup;
DROP INDEX IF EXISTS idx_quota_alerts_created;
DROP TABLE IF EXISTS quota_alerts;
DROP TABLE IF EXISTS quota_history;
DROP TABLE IF EXISTS quota_balances;
DROP TABLE IF EXISTS quota_limits;
DROP TABLE IF EXISTS quota_accounts;
