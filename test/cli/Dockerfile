# End-to-end CLI smoke test, modelling "install ao on a fresh machine, then use it".
#
# Build context is the REPO ROOT:
#   docker build -f test/cli/Dockerfile -t ao-cli-smoke .
#   docker run --rm --init ao-cli-smoke
#
# `ao start` is now the desktop-app launcher (it no longer runs a daemon); the
# fresh-box check just proves the binary is sane and start reaches the fetch
# path. --init keeps process accounting clean for any short-lived child.

# ---- stage 1: build the binary (the "release" a user would download) ----

FROM golang:1.27.1-bookworm AS build
WORKDIR /src

# Cache modules first.
COPY backend/go.mod backend/go.sum ./backend/
RUN cd backend && go mod download

COPY backend ./backend
# Pure-Go SQLite (modernc) builds fine with CGO disabled -> a static binary.
# Build against a release repo with NO published assets so `ao start` reaches the
# fetch path and fails cleanly (404) on a fresh box. The real AgentWrapper repo
# now publishes a linux-x64 AppImage, so an unpinned binary would download it and
# exit 0, defeating the fresh-install assertion below. See install-check.sh.
RUN cd backend && CGO_ENABLED=0 go build -trimpath \
      -ldflags "-X github.com/aoagents/agent-orchestrator/backend/internal/cli.releaseRepo=AgentWrapper/ao-fresh-install-fixture" \
      -o /out/ao ./cmd/ao

# ---- stage 2: a clean machine with NO Go toolchain, just like an end user ----
FROM debian:bookworm-slim AS run

# Runtime deps a fresh user would need: git is required by `ao doctor`; curl
# drives the HTTP-level guard checks; ca-certificates for good measure. Zellij is
# optional for this smoke test, so doctor reports a WARN if it is absent.
RUN apt-get update \
 && apt-get install -y --no-install-recommends git curl ca-certificates \
 && rm -rf /var/lib/apt/lists/*

# "Install" the CLI the way a user would: drop the binary on PATH.
COPY --from=build /out/ao /usr/local/bin/ao
COPY test/cli/install-check.sh /usr/local/bin/ao-install-check.sh
RUN chmod +x /usr/local/bin/ao /usr/local/bin/ao-install-check.sh

# Run as an unprivileged user with a real HOME, like a normal install.
RUN useradd --create-home --shell /bin/bash ao
USER ao
WORKDIR /home/ao

# Sanity: prove the install resolved before the check runs.
RUN ao version

ENTRYPOINT ["/usr/local/bin/ao-install-check.sh"]
