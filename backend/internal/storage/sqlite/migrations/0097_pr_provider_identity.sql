-- Persist provider-native PR identity separately from mutable repository URLs.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE pr ADD COLUMN provider_id TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX idx_pr_provider_identity
    ON pr(provider, host, provider_id)
    WHERE provider_id != '';

CREATE TABLE pr_url_alias (
    alias_url     TEXT PRIMARY KEY,
    canonical_url TEXT NOT NULL REFERENCES pr(url) ON DELETE CASCADE,
    CHECK (alias_url != canonical_url)
);
CREATE INDEX idx_pr_url_alias_canonical ON pr_url_alias(canonical_url);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_pr_url_alias_canonical;
DROP TABLE IF EXISTS pr_url_alias;
DROP INDEX IF EXISTS idx_pr_provider_identity;
ALTER TABLE pr DROP COLUMN provider_id;
-- +goose StatementEnd
