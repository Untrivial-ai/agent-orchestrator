-- Widen the token-usage source constraints for the file-backed agent
-- collectors. The tables otherwise remain byte-for-byte compatible, so follow
-- the repository's established writable_schema pattern for CHECK-only changes
-- instead of rebuilding append-only usage rows and their foreign keys.

-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
PRAGMA writable_schema = ON;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(
    sql,
    'CHECK (harness IN (''claude-code'', ''codex''))',
    'CHECK (harness IN (''claude-code'', ''codex'', ''copilot'', ''kimi'', ''pi'', ''qwen''))'
)
WHERE type = 'table' AND name = 'usage_bindings';
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(
    sql,
    'CHECK (kind IN (''claude_main'', ''claude_subagent'', ''codex_rollout''))',
    'CHECK (kind IN (''claude_main'', ''claude_subagent'', ''codex_rollout'', ''copilot_shutdown'', ''kimi_wire'', ''pi_session'', ''qwen_monthly''))'
)
WHERE type = 'table' AND name = 'usage_sources';
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = RESET;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
PRAGMA writable_schema = ON;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(
    sql,
    'CHECK (harness IN (''claude-code'', ''codex'', ''copilot'', ''kimi'', ''pi'', ''qwen''))',
    'CHECK (harness IN (''claude-code'', ''codex''))'
)
WHERE type = 'table' AND name = 'usage_bindings';
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(
    sql,
    'CHECK (kind IN (''claude_main'', ''claude_subagent'', ''codex_rollout'', ''copilot_shutdown'', ''kimi_wire'', ''pi_session'', ''qwen_monthly''))',
    'CHECK (kind IN (''claude_main'', ''claude_subagent'', ''codex_rollout''))'
)
WHERE type = 'table' AND name = 'usage_sources';
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = RESET;
-- +goose StatementEnd
