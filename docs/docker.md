# Headless AO daemon (Docker)

Run the AO daemon without the Electron desktop app. The image ships the `ao`
CLI and a long-running `ao daemon` process. It does **not** serve the desktop
supervisor UI in a browser: `frontend` `dev:web` is a Vite mock/dev loop, and
the daemon does not embed renderer assets. Off-host access uses the same
authenticated **Connect Mobile** LAN listener as the desktop product (default
port `3011`).

Published images (stable tags only):

```text
ghcr.io/untrivial-ai/agent-orchestrator/daemon:<version>
ghcr.io/untrivial-ai/agent-orchestrator/daemon:latest
```

## Quick start

```bash
docker run --rm --init \
  --name ao-daemon \
  -e AO_MOBILE_ENABLE=1 \
  -e AO_MOBILE_PASSWORD='ReplaceMe1' \
  -p 3011:3011 \
  -v "$PWD:/workspace" \
  -v ao-data:/ao/data \
  ghcr.io/untrivial-ai/agent-orchestrator/daemon:latest
```

Then:

1. Register the mounted workspace (from another shell):

   ```bash
   docker exec ao-daemon ao project add /workspace
   ```

2. Point the [AO mobile app](../packages/mobile/README.md) (or any API client)
   at `http://<docker-host>:3011` with bearer password `ReplaceMe1`.

3. Check health inside the container:

   ```bash
   docker exec ao-daemon ao status
   ```

Do **not** publish port `3001` to untrusted networks. That listener is
loopback-oriented and unauthenticated; Connect Mobile on `3011` is the
supported remote path.

### Environment

| Variable | Default | Purpose |
| --- | --- | --- |
| `AO_DATA_DIR` | `/ao/data` | Durable daemon state (SQLite, mobile config, worktrees) |
| `AO_RUN_FILE` | `/ao/data/running.json` | Daemon PID/port handshake file |
| `AO_MOBILE_ENABLE` | unset | `1` / `true` seeds Connect Mobile enabled on boot |
| `AO_MOBILE_PASSWORD` | generated if enable set | Bearer password for the LAN listener |
| `AO_MOBILE_PORT` | `3011` | Preferred LAN bind port (persisted as `lastPort`) |
| `AO_PORT` | `3001` | In-container loopback API port |

On first enable, the entrypoint writes `/ao/data/mobile/config.json` so
`restoreMobileOnBoot` arms the LAN listener with your password. Persist
`/ao/data` in a volume so the password and projects survive restarts.

## Agent CLIs and credentials

The image includes `git`, `tmux`, `curl`, and `jq`. It does **not** bundle
Claude Code, Codex, Cursor Agent, or other harness CLIs (same policy as the
desktop app).

### Option A — mount host installs and auth

```bash
docker run --rm --init \
  --name ao-daemon \
  -e AO_MOBILE_ENABLE=1 \
  -e AO_MOBILE_PASSWORD='ReplaceMe1' \
  -p 3011:3011 \
  -v "$PWD:/workspace" \
  -v ao-data:/ao/data \
  -v "$HOME/.claude:/home/ao/.claude" \
  -v "$HOME/.codex:/home/ao/.codex" \
  -v "$HOME/.cursor:/home/ao/.cursor" \
  -v "$HOME/.config/gh:/home/ao/.config/gh" \
  -v /usr/local/bin/claude:/usr/local/bin/claude:ro \
  -v /usr/local/bin/codex:/usr/local/bin/codex:ro \
  ghcr.io/untrivial-ai/agent-orchestrator/daemon:latest
```

Adjust binary paths to wherever the harnesses live on your host. API keys that
live in those config directories travel with the mounts; prefer mounts over
baking secrets into a derived image.

### Option B — derive a thicker image

```dockerfile
FROM ghcr.io/untrivial-ai/agent-orchestrator/daemon:latest
USER root
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
 && apt-get install -y --no-install-recommends nodejs \
 && npm install -g @anthropic-ai/claude-code @openai/codex \
 && rm -rf /var/lib/apt/lists/*
USER ao
```

Pass provider credentials at runtime (`-e ANTHROPIC_API_KEY=…`,
`-e OPENAI_API_KEY=…`, or mounted config dirs). Never commit credentials into
the image.

### GitHub CLI

For GitHub-backed projects, install `gh` in a derived image or mount a host
`gh` binary plus `~/.config/gh`. Inside the container:

```bash
docker exec -it ao-daemon gh auth status
```

## Build locally

```bash
docker build -f docker/Dockerfile -t ao-daemon .
bash test/docker/smoke.sh
```

Label any session-scoped containers with `--label ao.session=$AO_SESSION_ID`
when running inside an AO worker session.

## Security notes

- Connect Mobile is plaintext HTTP by design (trusted LAN / private network).
  Do not expose `3011` on the public internet.
- The loopback listener (`3001`) has no auth; keep it inside the container
  network unless you know you need host-local `docker exec` only (default).
- See [ADR 0001](adr/0001-lan-listener-for-mobile.md) and
  [Connect Mobile](../frontend/src/landing/content/docs/configuration/remote-access.mdx).
