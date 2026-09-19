# Headless AO daemon image: CLI + daemon only (no Electron / browser supervisor).
#
# Build from the repo root:
#   docker build -f docker/Dockerfile -t ao-daemon .
#
# See docs/docker.md for the run command, Connect Mobile LAN access, and how to
# add agent CLIs and credentials. The desktop web renderer (dev:web) is not a
# packaged, daemon-served UI — remote access uses the authenticated LAN listener.

# ---- build ----
FROM golang:1.25-bookworm AS build
WORKDIR /src

COPY backend/go.mod backend/go.sum ./backend/
RUN cd backend && go mod download

COPY backend ./backend
# Pure-Go SQLite (modernc) builds with CGO disabled → a static binary.
RUN cd backend && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ao ./cmd/ao

# ---- runtime ----
FROM debian:bookworm-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      git \
      jq \
      tmux \
 && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/ao /usr/local/bin/ao
COPY docker/entrypoint.sh /usr/local/bin/ao-docker-entrypoint.sh
RUN chmod +x /usr/local/bin/ao /usr/local/bin/ao-docker-entrypoint.sh \
 && useradd --create-home --shell /bin/bash --uid 10001 ao \
 && mkdir -p /ao/data /workspace \
 && chown -R ao:ao /ao /workspace

USER ao
ENV HOME=/home/ao \
    AO_DATA_DIR=/ao/data \
    AO_RUN_FILE=/ao/data/running.json
WORKDIR /workspace

# Loopback API (in-container CLI / health). Publish only when you intentionally
# forward it; prefer the authenticated LAN listener on 3011 for off-host access.
EXPOSE 3001
# Connect Mobile LAN listener (opt-in via AO_MOBILE_ENABLE / AO_MOBILE_PASSWORD).
EXPOSE 3011

ENTRYPOINT ["/usr/local/bin/ao-docker-entrypoint.sh"]
