-- Widen the sessions.harness CHECK to allow the Gemini CLI adapter.
-- Migration 0163 adds fx first; the Unreal Agent anchor below remains present
-- in both current and legacy qm schema variants, preserving fx on rewrite.
-- SQLite cannot ALTER an existing CHECK constraint, so this applies the same
-- surgical sqlite_master rewrite used by the earlier harness migrations.
-- writable_schema changes run outside a transaction; RESET forces SQLite to
-- reparse the schema.

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
    'CHECK (harness IN ('''', ''claude-code'', ''codex'', ''aider'', ''opencode'', ''grok'', ''droid'', ''amp'', ''agy'', ''crush'', ''cursor'', ''qwen'', ''copilot'', ''goose'', ''auggie'', ''continue'', ''devin'', ''cline'', ''kimi'', ''muse'', ''kiro'', ''kilocode'', ''vibe'', ''pi'', ''kimchi'', ''prime-agent'', ''autohand'', ''gemini'', ''omp'', ''fake''))'
)
WHERE type = 'table' AND name = 'sessions';
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(
    sql,
    'CHECK (harness IN ('''', ''claude-code'', ''codex'', ''aider'', ''opencode'', ''grok'', ''droid'', ''amp'', ''agy'', ''crush'', ''cursor'', ''qwen'', ''copilot'', ''goose'', ''auggie'', ''continue'', ''devin'', ''cline'', ''kimi'', ''muse'', ''kiro'', ''kilocode'', ''vibe'', ''pi'', ''kimchi'', ''prime-agent'', ''autohand'', ''omp'', ''qm'', ''fake''))',
    'CHECK (harness IN ('''', ''claude-code'', ''codex'', ''aider'', ''opencode'', ''grok'', ''droid'', ''amp'', ''agy'', ''crush'', ''cursor'', ''qwen'', ''copilot'', ''goose'', ''auggie'', ''continue'', ''devin'', ''cline'', ''kimi'', ''muse'', ''kiro'', ''kilocode'', ''vibe'', ''pi'', ''kimchi'', ''prime-agent'', ''autohand'', ''gemini'', ''omp'', ''qm'', ''fake''))'
)
WHERE type = 'table' AND name = 'sessions';
-- +goose StatementEnd
-- Migration 0155 has already added Unreal Agent on current installations.
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(sql, '''omp'', ''unreal-agent''', '''gemini'', ''omp'', ''unreal-agent''')
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
    'CHECK (harness IN ('''', ''claude-code'', ''codex'', ''aider'', ''opencode'', ''grok'', ''droid'', ''amp'', ''agy'', ''crush'', ''cursor'', ''qwen'', ''copilot'', ''goose'', ''auggie'', ''continue'', ''devin'', ''cline'', ''kimi'', ''muse'', ''kiro'', ''kilocode'', ''vibe'', ''pi'', ''kimchi'', ''prime-agent'', ''autohand'', ''gemini'', ''omp'', ''fake''))',
    'CHECK (harness IN ('''', ''claude-code'', ''codex'', ''aider'', ''opencode'', ''grok'', ''droid'', ''amp'', ''agy'', ''crush'', ''cursor'', ''qwen'', ''copilot'', ''goose'', ''auggie'', ''continue'', ''devin'', ''cline'', ''kimi'', ''muse'', ''kiro'', ''kilocode'', ''vibe'', ''pi'', ''kimchi'', ''prime-agent'', ''autohand'', ''omp'', ''fake''))'
)
WHERE type = 'table' AND name = 'sessions';
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(
    sql,
    'CHECK (harness IN ('''', ''claude-code'', ''codex'', ''aider'', ''opencode'', ''grok'', ''droid'', ''amp'', ''agy'', ''crush'', ''cursor'', ''qwen'', ''copilot'', ''goose'', ''auggie'', ''continue'', ''devin'', ''cline'', ''kimi'', ''muse'', ''kiro'', ''kilocode'', ''vibe'', ''pi'', ''kimchi'', ''prime-agent'', ''autohand'', ''gemini'', ''omp'', ''qm'', ''fake''))',
    'CHECK (harness IN ('''', ''claude-code'', ''codex'', ''aider'', ''opencode'', ''grok'', ''droid'', ''amp'', ''agy'', ''crush'', ''cursor'', ''qwen'', ''copilot'', ''goose'', ''auggie'', ''continue'', ''devin'', ''cline'', ''kimi'', ''muse'', ''kiro'', ''kilocode'', ''vibe'', ''pi'', ''kimchi'', ''prime-agent'', ''autohand'', ''omp'', ''qm'', ''fake''))'
)
WHERE type = 'table' AND name = 'sessions';
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(sql, '''gemini'', ''omp'', ''unreal-agent''', '''omp'', ''unreal-agent''')
WHERE type = 'table' AND name = 'sessions';
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = RESET;
-- +goose StatementEnd
