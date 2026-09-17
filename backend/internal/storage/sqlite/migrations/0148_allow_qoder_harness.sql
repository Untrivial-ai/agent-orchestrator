-- Widen sessions.harness to allow the Qoder CLI adapter. The replacement is
-- deliberately anchored at the final fake value so it also converges legacy
-- development schemas whose optional qm value differs.

-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
PRAGMA writable_schema = ON;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(sql, '''omp'', ''fake''))', '''omp'', ''qoder'', ''fake''))')
WHERE type = 'table' AND name = 'sessions' AND instr(sql, '''qoder''') = 0;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(sql, '''omp'', ''qm'', ''fake''))', '''omp'', ''qoder'', ''qm'', ''fake''))')
WHERE type = 'table' AND name = 'sessions' AND instr(sql, '''qoder''') = 0;
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
SET sql = replace(sql, '''omp'', ''qoder'', ''fake''))', '''omp'', ''fake''))')
WHERE type = 'table' AND name = 'sessions';
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(sql, '''omp'', ''qoder'', ''qm'', ''fake''))', '''omp'', ''qm'', ''fake''))')
WHERE type = 'table' AND name = 'sessions';
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = RESET;
-- +goose StatementEnd
