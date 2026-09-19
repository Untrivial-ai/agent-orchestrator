-- +goose Up
-- +goose StatementBegin
-- The named skills the provider will let this conversation invoke.
--
-- Persisted because the catalog is push-only. ACP has no request that asks for it:
-- the agent sends available_commands_update on session/new and again on
-- commands_changed, and nothing re-sends it when AO reattaches to a surviving
-- provider. A restart therefore left the driver's in-memory copy empty for the rest
-- of the session, and an empty catalog is not renderable as "not known yet" -- the
-- composer reads it as "this agent has no skills" and `/` stops opening a menu.
--
-- The whole list is stored, not a delta, for the reason plan_json is: every push is
-- a complete replacement of the catalog, so the latest payload is the entire
-- answer. An empty list is stored as an empty array rather than NULL, matching
-- mcp_servers_json: "the provider says there are none" and "the provider has never
-- said" are different facts and only the first should suppress the menu.
ALTER TABLE conversations ADD COLUMN skills_json TEXT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE conversations DROP COLUMN skills_json;
-- +goose StatementEnd
