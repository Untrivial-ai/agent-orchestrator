# syntax=docker/dockerfile:1

ARG BUILDPLATFORM
ARG TARGETOS=linux
ARG TARGETARCH

FROM --platform=${BUILDPLATFORM} golang:1.26.5-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/ao-cloud ./cmd/ao-cloud && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/ao-cloud-migrate ./cmd/ao-cloud-migrate && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/ao-cloud-healthcheck ./cmd/ao-cloud-healthcheck && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/ao-worker ./cmd/ao-worker && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/ao ./cmd/ao-cloud-agent

FROM node:22-trixie-slim AS worker
ARG TARGETARCH
ARG CLAUDE_CODE_VERSION=2.1.228
ARG CODEX_VERSION=0.147.0
ARG CURSOR_AGENT_VERSION=2026.08.11-e8db854
ARG GH_VERSION=2.97.0
ARG AGENT_BROWSER_VERSION=0.33.1
RUN apt-get update && \
    apt-get upgrade --yes && \
    apt-get install --yes --no-install-recommends \
        bash \
        ca-certificates \
        chromium \
        curl \
        git \
        jq \
        openssh-client \
        procps \
        tar && \
    case "${TARGETARCH}" in \
        amd64|arm64) gh_arch="${TARGETARCH}" ;; \
        *) echo "unsupported GitHub CLI architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac && \
    curl --fail --location --silent --show-error \
        "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${gh_arch}.tar.gz" \
        | tar --strip-components=2 -xzf - -C /usr/local/bin \
            "gh_${GH_VERSION}_linux_${gh_arch}/bin/gh" && \
    rm -rf /var/lib/apt/lists/* && \
    npm install --global \
        "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
        "@openai/codex@${CODEX_VERSION}" && \
    claude --version && \
    codex --version && \
    gh --version && \
    groupadd --gid 10001 ao-worker && \
    useradd --uid 10001 --gid ao-worker --home-dir /workspace/.ao/home --shell /bin/bash ao-worker && \
    mkdir -p /workspace/repository /workspace/.ao/home && \
    chown -R ao-worker:ao-worker /workspace
RUN case "${TARGETARCH}" in \
        amd64) cursor_arch=x64 ;; \
        arm64) cursor_arch=arm64 ;; \
        *) echo "unsupported Cursor Agent architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac && \
    mkdir -p "/opt/cursor-agent/${CURSOR_AGENT_VERSION}" && \
    curl --fail --location --silent --show-error \
        "https://downloads.cursor.com/lab/${CURSOR_AGENT_VERSION}/linux/${cursor_arch}/agent-cli-package.tar.gz" \
        | tar --strip-components=1 -xzf - -C "/opt/cursor-agent/${CURSOR_AGENT_VERSION}" && \
    ln -s "/opt/cursor-agent/${CURSOR_AGENT_VERSION}/cursor-agent" /usr/local/bin/cursor-agent && \
    cursor-agent --version
# The pinned agent-browser engine for the in-VM session browser (same pins as
# frontend/scripts/prepare-agent-browser.mjs; keep the two tables in sync).
RUN set -eu; \
    case "${TARGETARCH}" in \
        amd64) ab_asset=agent-browser-linux-x64;  ab_sha256=6e04d06605c4ca62da36e3263086e0f7ceae808b55508de2c3958d4b7fe430aa ;; \
        arm64) ab_asset=agent-browser-linux-arm64; ab_sha256=281cce8e3e9eb11fd823b13c085996d7361c35923ad454ce5cb06a5515630e9b ;; \
        *) echo "unsupported agent-browser architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl --fail --location --silent --show-error \
        "https://github.com/vercel-labs/agent-browser/releases/download/v${AGENT_BROWSER_VERSION}/${ab_asset}" \
        -o /tmp/agent-browser; \
    echo "${ab_sha256}  /tmp/agent-browser" | sha256sum -c -; \
    mkdir -p /usr/local/lib/ao; \
    install -m 0755 /tmp/agent-browser /usr/local/lib/ao/agent-browser; \
    rm -f /tmp/agent-browser; \
    /usr/local/lib/ao/agent-browser --version; \
    chromium --version
COPY --from=build --chown=ao-worker:ao-worker /out/ao-worker /ao-worker
COPY --from=build --chown=ao-worker:ao-worker /out/ao /usr/local/bin/ao
ENV AO_CLOUD_CHROMIUM_PATH=/usr/bin/chromium \
    AO_CLOUD_AGENT_BROWSER_PATH=/usr/local/lib/ao/agent-browser
USER ao-worker
WORKDIR /workspace/repository
ENTRYPOINT ["/ao-worker"]

FROM gcr.io/distroless/static-debian12:nonroot AS control-plane
COPY --from=build --chown=nonroot:nonroot /out/ao-cloud /ao-cloud
COPY --from=build --chown=nonroot:nonroot /out/ao-cloud-migrate /ao-cloud-migrate
COPY --from=build --chown=nonroot:nonroot /out/ao-cloud-healthcheck /ao-cloud-healthcheck
COPY --from=build --chown=nonroot:nonroot /out/ao-worker /ao-worker
COPY --from=build --chown=nonroot:nonroot /out/ao /ao
EXPOSE 8080
ENTRYPOINT ["/ao-cloud"]
