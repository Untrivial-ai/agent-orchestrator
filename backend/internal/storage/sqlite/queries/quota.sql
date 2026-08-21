-- name: UpsertQuotaAccount :exec
INSERT INTO quota_accounts (
    provider, account_id, account_label, plan_type, auth_mode,
    supports_read, supports_subscribe, supports_history, supports_credits,
    supports_spend_limits, completeness, observed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (provider, account_id) DO UPDATE SET
    account_label = CASE WHEN excluded.account_label <> '' THEN excluded.account_label ELSE quota_accounts.account_label END,
    plan_type = CASE WHEN excluded.plan_type <> '' THEN excluded.plan_type ELSE quota_accounts.plan_type END,
    auth_mode = CASE WHEN excluded.auth_mode <> '' THEN excluded.auth_mode ELSE quota_accounts.auth_mode END,
    supports_read = MAX(quota_accounts.supports_read, excluded.supports_read),
    supports_subscribe = MAX(quota_accounts.supports_subscribe, excluded.supports_subscribe),
    supports_history = MAX(quota_accounts.supports_history, excluded.supports_history),
    supports_credits = MAX(quota_accounts.supports_credits, excluded.supports_credits),
    supports_spend_limits = MAX(quota_accounts.supports_spend_limits, excluded.supports_spend_limits),
    completeness = excluded.completeness,
    observed_at = excluded.observed_at,
    last_refresh_error = ''
WHERE excluded.observed_at >= quota_accounts.observed_at;

-- name: RecordQuotaRefreshFailure :exec
UPDATE quota_accounts
SET last_refresh_error = ?
WHERE provider = ? AND account_id = ?;

-- name: InsertQuotaAlert :execrows
INSERT OR IGNORE INTO quota_alerts (
    id, provider, account_id, limit_id, kind, severity, title, body, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListQuotaAlerts :many
SELECT * FROM quota_alerts
WHERE created_at > sqlc.arg(since)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit);

-- name: DeleteQuotaLimitsForAccount :exec
DELETE FROM quota_limits WHERE provider = ? AND account_id = ?;

-- name: UpsertQuotaLimit :exec
INSERT INTO quota_limits (
    provider, account_id, limit_id, window_type, category, scope, scope_id,
    limit_name, used_percent, remaining_value, total_value, unit,
    window_duration_seconds, resets_at, reached, reached_reason, observed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (provider, account_id, limit_id, window_type, scope, scope_id) DO UPDATE SET
    category = excluded.category,
    limit_name = CASE WHEN excluded.limit_name <> '' THEN excluded.limit_name ELSE quota_limits.limit_name END,
    used_percent = COALESCE(excluded.used_percent, quota_limits.used_percent),
    remaining_value = COALESCE(excluded.remaining_value, quota_limits.remaining_value),
    total_value = COALESCE(excluded.total_value, quota_limits.total_value),
    unit = CASE WHEN excluded.unit <> '' THEN excluded.unit ELSE quota_limits.unit END,
    window_duration_seconds = COALESCE(excluded.window_duration_seconds, quota_limits.window_duration_seconds),
    resets_at = COALESCE(excluded.resets_at, quota_limits.resets_at),
    reached = COALESCE(excluded.reached, quota_limits.reached),
    reached_reason = CASE WHEN excluded.reached_reason <> '' THEN excluded.reached_reason ELSE quota_limits.reached_reason END,
    observed_at = excluded.observed_at
WHERE excluded.observed_at >= quota_limits.observed_at;

-- name: DeleteQuotaBalancesForAccount :exec
DELETE FROM quota_balances WHERE provider = ? AND account_id = ?;

-- name: UpsertQuotaBalance :exec
INSERT INTO quota_balances (
    provider, account_id, balance_id, balance_name, value, currency, unlimited, observed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (provider, account_id, balance_id) DO UPDATE SET
    balance_name = excluded.balance_name,
    value = excluded.value,
    currency = excluded.currency,
    unlimited = excluded.unlimited,
    observed_at = excluded.observed_at
WHERE excluded.observed_at >= quota_balances.observed_at;

-- name: InsertQuotaHistoryIfChanged :execrows
INSERT INTO quota_history (
    provider, account_id, limit_id, window_type, scope, scope_id,
    used_percent, resets_at, reached, observed_at
)
SELECT
    sqlc.arg(input_provider), sqlc.arg(input_account_id),
    sqlc.arg(input_limit_id), sqlc.arg(input_window_type),
    sqlc.arg(input_scope), sqlc.arg(input_scope_id),
    sqlc.narg(input_used_percent), sqlc.narg(input_resets_at),
    sqlc.narg(input_reached), sqlc.arg(input_observed_at)
WHERE NOT EXISTS (
    SELECT 1
    FROM (
        SELECT used_percent, resets_at, reached
        FROM quota_history
        WHERE provider = sqlc.arg(input_provider)
          AND account_id = sqlc.arg(input_account_id)
          AND limit_id = sqlc.arg(input_limit_id)
          AND window_type = sqlc.arg(input_window_type)
          AND scope = sqlc.arg(input_scope)
          AND scope_id = sqlc.arg(input_scope_id)
        ORDER BY observed_at DESC, id DESC
        LIMIT 1
    ) AS latest
    WHERE (
        (latest.used_percent IS NULL AND sqlc.narg(input_used_percent) IS NULL)
        OR ABS(COALESCE(latest.used_percent, -1000) - COALESCE(sqlc.narg(input_used_percent), -1000)) < 1
    )
      AND COALESCE(latest.resets_at, '') = COALESCE(sqlc.narg(input_resets_at), '')
      AND COALESCE(latest.reached, -1) = COALESCE(sqlc.narg(input_reached), -1)
);

-- name: ListQuotaAccounts :many
SELECT * FROM quota_accounts ORDER BY provider, account_id;

-- name: GetQuotaAccount :one
SELECT * FROM quota_accounts WHERE provider = ? AND account_id = ?;

-- name: ListQuotaLimitsForAccount :many
SELECT * FROM quota_limits
WHERE provider = ? AND account_id = ?
ORDER BY category, scope, scope_id, limit_name, limit_id, window_type;

-- name: ListQuotaBalancesForAccount :many
SELECT * FROM quota_balances
WHERE provider = ? AND account_id = ?
ORDER BY balance_name, balance_id;

-- name: ListQuotaHistoryForAccount :many
SELECT * FROM quota_history
WHERE provider = ? AND account_id = ?
  AND observed_at >= ?
ORDER BY observed_at DESC
LIMIT ?;

-- name: DeleteQuotaHistoryBefore :execrows
DELETE FROM quota_history WHERE observed_at < ?;

-- name: CompactQuotaHistoryDaily :execrows
DELETE FROM quota_history WHERE id IN (
    SELECT id FROM (
        SELECT history.id, ROW_NUMBER() OVER (
            PARTITION BY
                history.provider, history.account_id, history.limit_id,
                history.window_type, history.scope, history.scope_id,
                CAST(strftime('%s', history.observed_at) AS INTEGER) / 86400
            ORDER BY history.observed_at DESC, history.id DESC
        ) AS history_rank
        FROM quota_history AS history
        WHERE history.observed_at >= sqlc.arg(range_start)
          AND history.observed_at < sqlc.arg(range_end)
    ) AS ranked_history
    WHERE history_rank > 1
);

-- name: CompactQuotaHistoryHourly :execrows
DELETE FROM quota_history WHERE id IN (
    SELECT id FROM (
        SELECT history.id, ROW_NUMBER() OVER (
            PARTITION BY
                history.provider, history.account_id, history.limit_id,
                history.window_type, history.scope, history.scope_id,
                CAST(strftime('%s', history.observed_at) AS INTEGER) / 3600
            ORDER BY history.observed_at DESC, history.id DESC
        ) AS history_rank
        FROM quota_history AS history
        WHERE history.observed_at >= sqlc.arg(range_start)
          AND history.observed_at < sqlc.arg(range_end)
    ) AS ranked_history
    WHERE history_rank > 1
);

-- name: CompactQuotaHistoryMinute :execrows
DELETE FROM quota_history WHERE id IN (
    SELECT id FROM (
        SELECT history.id, ROW_NUMBER() OVER (
            PARTITION BY
                history.provider, history.account_id, history.limit_id,
                history.window_type, history.scope, history.scope_id,
                CAST(strftime('%s', history.observed_at) AS INTEGER) / 60
            ORDER BY history.observed_at DESC, history.id DESC
        ) AS history_rank
        FROM quota_history AS history
        WHERE history.observed_at >= sqlc.arg(range_start)
          AND history.observed_at < sqlc.arg(range_end)
    ) AS ranked_history
    WHERE history_rank > 1
);
