-- Widen the sessions.harness CHECK to allow the Codewhale adapter.
-- SQLite cannot ALTER an existing CHECK constraint, so this applies the same
-- surgical sqlite_master rewrite used by the earlier harness migrations.

-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
PRAGMA writable_schema = ON;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(
    sql,
    'CHECK (harness IN ('''', ''claude-code'', ''codex'', ''aider'', ''opencode'', ''grok'', ''droid'', ''amp'', ''agy'', ''crush'', ''cursor'', ''qwen'', ''copilot'', ''goose'', ''auggie'', ''continue'', ''devin'', ''cline'', ''kimi'', ''muse'', ''kiro'', ''kilocode'', ''vibe'', ''pi'', ''kimchi'', ''prime-agent'', ''autohand'', ''omp'', ''fake''))',
    'CHECK (harness IN ('''', ''claude-code'', ''codex'', ''aider'', ''opencode'', ''grok'', ''droid'', ''amp'', ''agy'', ''crush'', ''cursor'', ''qwen'', ''copilot'', ''goose'', ''auggie'', ''continue'', ''devin'', ''cline'', ''kimi'', ''muse'', ''kiro'', ''kilocode'', ''vibe'', ''pi'', ''kimchi'', ''prime-agent'', ''autohand'', ''omp'', ''codewhale'', ''fake''))'
)
WHERE type = 'table' AND name = 'sessions';
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(
    sql,
    'CHECK (harness IN ('''', ''claude-code'', ''codex'', ''aider'', ''opencode'', ''grok'', ''droid'', ''amp'', ''agy'', ''crush'', ''cursor'', ''qwen'', ''copilot'', ''goose'', ''auggie'', ''continue'', ''devin'', ''cline'', ''kimi'', ''muse'', ''kiro'', ''kilocode'', ''vibe'', ''pi'', ''kimchi'', ''prime-agent'', ''autohand'', ''omp'', ''qm'', ''fake''))',
    'CHECK (harness IN ('''', ''claude-code'', ''codex'', ''aider'', ''opencode'', ''grok'', ''droid'', ''amp'', ''agy'', ''crush'', ''cursor'', ''qwen'', ''copilot'', ''goose'', ''auggie'', ''continue'', ''devin'', ''cline'', ''kimi'', ''muse'', ''kiro'', ''kilocode'', ''vibe'', ''pi'', ''kimchi'', ''prime-agent'', ''autohand'', ''omp'', ''codewhale'', ''qm'', ''fake''))'
)
WHERE type = 'table' AND name = 'sessions';
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
    'CHECK (harness IN ('''', ''claude-code'', ''codex'', ''aider'', ''opencode'', ''grok'', ''droid'', ''amp'', ''agy'', ''crush'', ''cursor'', ''qwen'', ''copilot'', ''goose'', ''auggie'', ''continue'', ''devin'', ''cline'', ''kimi'', ''muse'', ''kiro'', ''kilocode'', ''vibe'', ''pi'', ''kimchi'', ''prime-agent'', ''autohand'', ''omp'', ''codewhale'', ''fake''))',
    'CHECK (harness IN ('''', ''claude-code'', ''codex'', ''aider'', ''opencode'', ''grok'', ''droid'', ''amp'', ''agy'', ''crush'', ''cursor'', ''qwen'', ''copilot'', ''goose'', ''auggie'', ''continue'', ''devin'', ''cline'', ''kimi'', ''muse'', ''kiro'', ''kilocode'', ''vibe'', ''pi'', ''kimchi'', ''prime-agent'', ''autohand'', ''omp'', ''fake''))'
)
WHERE type = 'table' AND name = 'sessions';
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(
    sql,
    'CHECK (harness IN ('''', ''claude-code'', ''codex'', ''aider'', ''opencode'', ''grok'', ''droid'', ''amp'', ''agy'', ''crush'', ''cursor'', ''qwen'', ''copilot'', ''goose'', ''auggie'', ''continue'', ''devin'', ''cline'', ''kimi'', ''muse'', ''kiro'', ''kilocode'', ''vibe'', ''pi'', ''kimchi'', ''prime-agent'', ''autohand'', ''omp'', ''codewhale'', ''qm'', ''fake''))',
    'CHECK (harness IN ('''', ''claude-code'', ''codex'', ''aider'', ''opencode'', ''grok'', ''droid'', ''amp'', ''agy'', ''crush'', ''cursor'', ''qwen'', ''copilot'', ''goose'', ''auggie'', ''continue'', ''devin'', ''cline'', ''kimi'', ''muse'', ''kiro'', ''kilocode'', ''vibe'', ''pi'', ''kimchi'', ''prime-agent'', ''autohand'', ''omp'', ''qm'', ''fake''))'
)
WHERE type = 'table' AND name = 'sessions';
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = RESET;
-- +goose StatementEnd
