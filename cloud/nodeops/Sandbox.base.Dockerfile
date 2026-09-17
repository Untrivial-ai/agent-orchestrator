# Common base for the per-harness sandbox templates: OS tooling, Node (the npm
# harnesses need it), the GitHub CLI, and the worker user/workspace layout.
# publish-nodeops-template.sh appends exactly one nodeops/harness/*.Dockerfile
# snippet plus the worker bake layer; Sandbox.Dockerfile remains the all-in-one
# variant. Keeping one agent per template roughly halves the image, which
# shrinks the provider's cold-host pull - the dominant worst-case cost of
# creating a sandbox.
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
    groupadd --gid 10001 ao-worker && \
    useradd --uid 10001 --gid ao-worker --home-dir /workspace/.ao/home \
        --shell /bin/bash ao-worker && \
    mkdir -p /workspace/repository /workspace/.ao/home /workspace/.ao/worker && \
    chown -R ao-worker:ao-worker /workspace && \
    rm -rf /var/lib/apt/lists/* && \
    gh --version && \
    node --version
