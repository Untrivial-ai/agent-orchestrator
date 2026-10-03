-- +goose Up
CREATE INDEX ao_github_webhook_opened_repo_idx
    ON ao_github_webhook_deliveries (github_repository_id, received_at DESC)
    WHERE event = 'pull_request' AND action = 'opened';

-- +goose Down
DROP INDEX ao_github_webhook_opened_repo_idx;
