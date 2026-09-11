-- +goose Up

-- A personal Coder connection belongs to the user, not to an organization.
-- Keep it separate from provider_connection_id, whose composite foreign key is
-- deliberately scoped to ao_provider_connections. A sandbox may use one kind
-- of connection or the other, never both.
ALTER TABLE ao_sandboxes
    ADD COLUMN user_provider_connection_id UUID
        REFERENCES ao_user_provider_connections(id) ON DELETE RESTRICT;

ALTER TABLE ao_sandboxes
    ADD CONSTRAINT ao_sandboxes_single_provider_connection_check
    CHECK (provider_connection_id IS NULL OR user_provider_connection_id IS NULL);

CREATE INDEX ao_sandboxes_user_provider_connection_idx
    ON ao_sandboxes(user_provider_connection_id)
    WHERE user_provider_connection_id IS NOT NULL;

-- Reconciliation has no end-user principal. Grant its narrowly identified
-- service context read access so it can unwrap only the connection referenced
-- by the claimed sandbox. Mutations remain owner-only.
CREATE POLICY ao_user_provider_connections_service_read_policy
    ON ao_user_provider_connections
    FOR SELECT
    USING (current_setting('ao.service', true) = 'control-plane');

-- +goose StatementBegin
CREATE FUNCTION ao_enforce_sandbox_user_provider_connection() RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    connection_provider TEXT;
    connection_user_id UUID;
    session_user_id UUID;
BEGIN
    IF NEW.user_provider_connection_id IS NULL THEN
        RETURN NEW;
    END IF;

    SELECT provider, user_id
    INTO connection_provider, connection_user_id
    FROM ao_user_provider_connections
    WHERE id = NEW.user_provider_connection_id;

    SELECT created_by_user_id
    INTO session_user_id
    FROM ao_sessions
    WHERE org_id = NEW.org_id AND id = NEW.session_id;

    IF connection_provider IS NULL OR connection_provider <> NEW.provider OR
       connection_user_id IS DISTINCT FROM session_user_id THEN
        RAISE EXCEPTION
            'sandbox personal provider connection does not match its provider and creator'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER ao_sandboxes_user_provider_connection_match
    BEFORE INSERT OR UPDATE OF provider, user_provider_connection_id
    ON ao_sandboxes
    FOR EACH ROW EXECUTE FUNCTION ao_enforce_sandbox_user_provider_connection();

-- +goose Down
DROP TRIGGER IF EXISTS ao_sandboxes_user_provider_connection_match ON ao_sandboxes;
DROP FUNCTION IF EXISTS ao_enforce_sandbox_user_provider_connection();
DROP POLICY IF EXISTS ao_user_provider_connections_service_read_policy ON ao_user_provider_connections;
DROP INDEX IF EXISTS ao_sandboxes_user_provider_connection_idx;
ALTER TABLE ao_sandboxes DROP CONSTRAINT IF EXISTS ao_sandboxes_single_provider_connection_check;
ALTER TABLE ao_sandboxes DROP COLUMN IF EXISTS user_provider_connection_id;
