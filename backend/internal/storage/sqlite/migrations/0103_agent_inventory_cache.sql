-- +goose Up
CREATE TABLE agent_inventory_cache (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    inventory_json TEXT NOT NULL,
    observed_at TIMESTAMP NOT NULL
);

-- +goose Down
DROP TABLE agent_inventory_cache;
