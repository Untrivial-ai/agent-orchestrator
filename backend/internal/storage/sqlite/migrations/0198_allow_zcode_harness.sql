-- Widen the sessions.harness CHECK to allow the ZCode adapter (Z.ai's
-- official coding agent, github.com/zai-org/ZCode). SQLite cannot ALTER an
-- existing CHECK constraint, so this rewrites the sessions schema with the
-- same anchor-on-'fake' pattern used by the Codewhale migration (0193):
-- every shipped constraint variant ends with the retained 'fake' fixture
-- harness, so replacing "'fake'))" inserts 'zcode' directly before it
-- regardless of which earlier harness migrations the database took
-- (including the legacy QM-variant branch, which keeps its 'qm' entry).
-- The LIKE guards keep this a no-op on an already-widened schema, and a
-- database whose constraint is missing earlier harnesses is left for the
-- reconcileSchema repair, which also knows about zcode.

-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
PRAGMA writable_schema = ON;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(sql, '''fake''))', '''zcode'', ''fake''))')
WHERE type = 'table' AND name = 'sessions'
  AND sql LIKE '%CHECK (harness IN (%'
  AND sql LIKE '%''muse''%'
  AND sql LIKE '%''omp''%'
  AND sql LIKE '%''gemini''%'
  AND sql LIKE '%''unreal-agent''%'
  AND sql LIKE '%''mimo-code''%'
  AND sql LIKE '%''deepseek-harness''%'
  AND sql LIKE '%''opencode-v2''%'
  AND sql NOT LIKE '%''zcode''%';
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
SET sql = replace(sql, '''zcode'', ''fake''))', '''fake''))')
WHERE type = 'table' AND name = 'sessions';
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = RESET;
-- +goose StatementEnd
