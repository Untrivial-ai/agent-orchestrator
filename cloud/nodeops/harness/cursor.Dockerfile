# cursor harness layer, appended to Sandbox.base.Dockerfile by
# publish-nodeops-template.sh. Version kept in step with Sandbox.Dockerfile.

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
