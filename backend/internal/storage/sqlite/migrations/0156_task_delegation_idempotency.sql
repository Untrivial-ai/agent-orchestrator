-- +goose Up
CREATE TABLE task_delegations (
    idempotency_key     TEXT PRIMARY KEY
        CHECK (length(idempotency_key) > 0 AND length(idempotency_key) <= 128),
    request_fingerprint TEXT NOT NULL
        CHECK (
            length(request_fingerprint) = 67
            AND substr(request_fingerprint, 1, 3) = 'v1:'
            AND substr(request_fingerprint, 4) NOT GLOB '*[^0-9a-f]*'
        ),
    worker_id           TEXT REFERENCES sessions (id) ON DELETE CASCADE,
    state               TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'completed')),
    created_at          TIMESTAMP NOT NULL,
    updated_at          TIMESTAMP NOT NULL,
    CHECK (updated_at >= created_at),
    CHECK (
        (state = 'pending' AND worker_id IS NULL)
        OR (state = 'completed' AND worker_id IS NOT NULL)
    )
);

-- +goose Down
DROP TABLE task_delegations;
