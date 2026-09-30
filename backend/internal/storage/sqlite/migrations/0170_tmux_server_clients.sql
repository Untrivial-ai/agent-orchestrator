-- +goose Up
-- +goose StatementBegin
-- One row per AO-owned tmux socket, recording the exact client binary last
-- proven able to speak to that live server. The checksum detects a stable path
-- whose contents were replaced by a later bundled build. managed_retained
-- limits deletion to the private copy AO captured before an in-place re-stage.
CREATE TABLE tmux_server_clients (
    socket_name       TEXT PRIMARY KEY,
    binary_path       TEXT NOT NULL,
    binary_sha256     TEXT NOT NULL CHECK (length(binary_sha256) = 64),
    managed_retained  BOOLEAN NOT NULL DEFAULT FALSE,
    confirmed_at      TIMESTAMP NOT NULL
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS tmux_server_clients;
-- +goose StatementEnd
