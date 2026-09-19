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
    npm install --global opencode-ai && \
    groupadd --gid 10001 ao-worker && \
    useradd --uid 10001 --gid ao-worker --home-dir /workspace/.ao/home \
        --shell /bin/bash ao-worker && \
    mkdir -p /workspace/repository /workspace/.ao/home /workspace/.ao/worker && \
    chown -R ao-worker:ao-worker /workspace && \
    rm -rf /var/lib/apt/lists/* /root/.npm && \
    opencode --version

RUN gh --version
