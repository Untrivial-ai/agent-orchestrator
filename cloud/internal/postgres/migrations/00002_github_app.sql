-- +goose Up

ALTER TABLE ao_github_install_attempts
    ADD CONSTRAINT ao_github_install_attempts_org_id_id_key UNIQUE (org_id, id),
    ADD COLUMN phase TEXT NOT NULL DEFAULT 'install'
        CHECK (phase IN ('install', 'oauth')),
    ADD COLUMN oauth_verifier_ciphertext BYTEA,
    ADD COLUMN oauth_verifier_nonce BYTEA
        CHECK (oauth_verifier_nonce IS NULL OR octet_length(oauth_verifier_nonce) = 12),
    ADD COLUMN last_error TEXT NOT NULL DEFAULT '',
    ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD CONSTRAINT ao_github_install_attempts_oauth_verifier_pair_check CHECK (
        (oauth_verifier_ciphertext IS NULL) = (oauth_verifier_nonce IS NULL)
    );

ALTER TABLE ao_github_installations
    ADD COLUMN sync_status TEXT NOT NULL DEFAULT 'pending'
        CHECK (sync_status IN ('pending', 'syncing', 'ready', 'retry', 'failed')),
    ADD COLUMN sync_generation BIGINT NOT NULL DEFAULT 0
        CHECK (sync_generation >= 0),
    ADD COLUMN last_synced_at TIMESTAMPTZ,
    ADD COLUMN last_error TEXT NOT NULL DEFAULT '';

CREATE TABLE ao_github_callback_routes (
    state_hash BYTEA PRIMARY KEY CHECK (octet_length(state_hash) = 32),
    attempt_id UUID NOT NULL,
    org_id UUID NOT NULL,
    user_id UUID NOT NULL,
    phase TEXT NOT NULL CHECK (phase IN ('install', 'oauth')),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ao_github_callback_routes_attempt_fk
        FOREIGN KEY (org_id, attempt_id)
        REFERENCES ao_github_install_attempts(org_id, id)
        ON DELETE CASCADE
);

CREATE TABLE ao_github_installation_routes (
    github_installation_id BIGINT PRIMARY KEY CHECK (github_installation_id > 0),
    org_id UUID NOT NULL,
    installation_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ao_github_installation_routes_installation_fk
        FOREIGN KEY (org_id, installation_id)
        REFERENCES ao_github_installations(org_id, id)
        ON DELETE CASCADE
);

ALTER TABLE ao_github_webhook_deliveries
    ADD COLUMN lease_owner TEXT NOT NULL DEFAULT '',
    ADD COLUMN lease_until TIMESTAMPTZ;

UPDATE ao_github_webhook_deliveries
SET status = 'retry',
    next_attempt_at = now(),
    processing_started_at = NULL,
    updated_at = now()
WHERE status = 'processing';

-- +goose StatementBegin
CREATE FUNCTION ao_enforce_active_github_project_grant()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.github_repository_id IS NULL AND NEW.github_repository_grant_id IS NULL THEN
        RETURN NEW;
    END IF;
    IF NEW.github_repository_id IS NULL OR NEW.github_repository_grant_id IS NULL THEN
        RAISE EXCEPTION 'GitHub repository and grant must be set together'
            USING ERRCODE = '23514';
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM ao_github_repository_grants grant_row
        WHERE grant_row.org_id = NEW.org_id
          AND grant_row.id = NEW.github_repository_grant_id
          AND grant_row.github_repository_id = NEW.github_repository_id
          AND grant_row.revoked_at IS NULL
    ) THEN
        RAISE EXCEPTION 'GitHub repository grant is not active'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER ao_projects_active_github_grant
    BEFORE INSERT OR UPDATE OF github_repository_id, github_repository_grant_id
    ON ao_projects
    FOR EACH ROW
    EXECUTE FUNCTION ao_enforce_active_github_project_grant();

-- +goose Down

DROP TRIGGER IF EXISTS ao_projects_active_github_grant ON ao_projects;
DROP FUNCTION IF EXISTS ao_enforce_active_github_project_grant();
ALTER TABLE ao_github_webhook_deliveries
    DROP COLUMN IF EXISTS lease_until,
    DROP COLUMN IF EXISTS lease_owner;
DROP TABLE IF EXISTS ao_github_installation_routes;
DROP TABLE IF EXISTS ao_github_callback_routes;
ALTER TABLE ao_github_installations
    DROP COLUMN IF EXISTS last_error,
    DROP COLUMN IF EXISTS last_synced_at,
    DROP COLUMN IF EXISTS sync_generation,
    DROP COLUMN IF EXISTS sync_status;
ALTER TABLE ao_github_install_attempts
    DROP CONSTRAINT IF EXISTS ao_github_install_attempts_oauth_verifier_pair_check,
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS last_error,
    DROP COLUMN IF EXISTS oauth_verifier_nonce,
    DROP COLUMN IF EXISTS oauth_verifier_ciphertext,
    DROP COLUMN IF EXISTS phase,
    DROP CONSTRAINT IF EXISTS ao_github_install_attempts_org_id_id_key;
