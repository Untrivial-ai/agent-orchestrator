-- +goose Up

-- consecutive_failures backs exponential backoff on sandbox provisioning
-- retries: without it, a persistently broken provider integration retries
-- at the same flat interval forever, indistinguishable in cost from a
-- transient blip. See RecordSandboxFailure and UpdateSandboxObservation in
-- internal/postgres/sandbox_store.go.
ALTER TABLE ao_sandboxes
    ADD COLUMN consecutive_failures INTEGER NOT NULL DEFAULT 0
        CHECK (consecutive_failures >= 0);

-- +goose Down
ALTER TABLE ao_sandboxes DROP COLUMN consecutive_failures;
