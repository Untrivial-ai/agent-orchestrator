-- +goose Up
DROP TABLE IF EXISTS agent_inventory_cache;

-- +goose Down
CREATE TABLE agent_inventory_cache (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    inventory_json TEXT NOT NULL,
    observed_at TIMESTAMP NOT NULL
);
