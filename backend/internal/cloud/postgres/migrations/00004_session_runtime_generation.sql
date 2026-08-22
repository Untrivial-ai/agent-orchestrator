-- +goose Up
ALTER TABLE ao_cloud_session_runtimes
    ADD COLUMN generation BIGINT NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE ao_cloud_session_runtimes
    DROP COLUMN generation;
