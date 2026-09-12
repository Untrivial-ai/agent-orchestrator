-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION ao_enforce_sandbox_provider_connection() RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    connection_provider TEXT;
BEGIN
    IF NEW.provider_connection_id IS NULL THEN
        RETURN NEW;
    END IF;

    SELECT provider
    INTO connection_provider
    FROM ao_provider_connections
    WHERE org_id = NEW.org_id
      AND id = NEW.provider_connection_id;

    IF connection_provider IS NOT NULL AND connection_provider <> NEW.provider THEN
        RAISE EXCEPTION
            'sandbox provider % does not match provider connection %',
            NEW.provider,
            connection_provider
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER ao_sandboxes_provider_connection_match
    BEFORE INSERT OR UPDATE OF provider, provider_connection_id
    ON ao_sandboxes
    FOR EACH ROW EXECUTE FUNCTION ao_enforce_sandbox_provider_connection();

-- +goose Down
DROP TRIGGER IF EXISTS ao_sandboxes_provider_connection_match ON ao_sandboxes;
DROP FUNCTION IF EXISTS ao_enforce_sandbox_provider_connection();
