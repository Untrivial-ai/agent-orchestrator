FROM nodeops/sandbox:debian

RUN apt-get update && \
    apt-get install --yes --no-install-recommends \
        bash \
        ca-certificates \
        curl \
        git \
        gnupg \
        jq \
        openssh-client \
        procps \
        tar \
        util-linux && \
    mkdir -p /etc/apt/keyrings && \
    curl --fail --location --silent --show-error \
        https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key \
        | gpg --dearmor -o /etc/apt/keyrings/nodesource.gpg && \
    echo "deb [signed-by=/etc/apt/keyrings/nodesource.gpg] https://deb.nodesource.com/node_22.x nodistro main" \
        > /etc/apt/sources.list.d/nodesource.list && \
    apt-get update && \
    apt-get install --yes --no-install-recommends nodejs && \
    architecture="$(dpkg --print-architecture)" && \
    gh_version=2.97.0 && \
    curl --fail --location --silent --show-error \
        "https://github.com/cli/cli/releases/download/v${gh_version}/gh_${gh_version}_linux_${architecture}.tar.gz" \
        | tar --strip-components=2 -xzf - -C /usr/bin \
            "gh_${gh_version}_linux_${architecture}/bin/gh" && \
    npm install --global \
        @anthropic-ai/claude-code@2.1.228 \
        @openai/codex@0.147.0 && \
    ln -sfn "$(npm root --global)/@anthropic-ai/claude-code/cli-wrapper.cjs" \
        /usr/local/bin/claude && \
    groupadd --gid 10001 ao-worker && \
    useradd --uid 10001 --gid ao-worker --home-dir /workspace/.ao/home \
        --shell /bin/bash ao-worker && \
    mkdir -p /workspace/repository /workspace/.ao/home /workspace/.ao/worker && \
    chown -R ao-worker:ao-worker /workspace && \
    rm -rf /var/lib/apt/lists/* /root/.npm && \
    claude --version && \
    codex --version

RUN gh --version

RUN architecture="$(dpkg --print-architecture)" && \
    case "$architecture" in \
        amd64) cursor_arch=x64 ;; \
        arm64) cursor_arch=arm64 ;; \
        *) echo "unsupported Cursor Agent architecture: $architecture" >&2; exit 1 ;; \
    esac && \
    cursor_version=2026.08.11-e8db854 && \
    mkdir -p "/opt/cursor-agent/$cursor_version" && \
    curl --fail --location --silent --show-error \
        "https://downloads.cursor.com/lab/$cursor_version/linux/$cursor_arch/agent-cli-package.tar.gz" \
        | tar --strip-components=1 -xzf - -C "/opt/cursor-agent/$cursor_version" && \
    ln -s "/opt/cursor-agent/$cursor_version/cursor-agent" /usr/local/bin/cursor-agent && \
    cursor-agent --version
