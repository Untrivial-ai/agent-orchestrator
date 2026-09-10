-- +goose Up
-- +goose NO TRANSACTION
PRAGMA auto_vacuum = INCREMENTAL;
VACUUM;

-- +goose Down
-- +goose NO TRANSACTION
PRAGMA auto_vacuum = NONE;
VACUUM;
