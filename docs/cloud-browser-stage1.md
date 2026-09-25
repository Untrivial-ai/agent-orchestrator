> Historical command-only milestone. The current branch also has a shared
> viewer and VM DevTools. Use [Shared browser sessions](cloud-shared-browser.md)
> for current coverage and test instructions.

# Cloud session browser (Stage 1): manual runbook

How to run and manually verify the in-VM session browser (`ao browser` inside
a cloud sandbox session) on two substrates: the local Docker provider stack,
and a CreateOS sandbox VM used as a remote machine. Everything below was
executed on 2026-09-17; expected outputs are excerpts from real runs.

Architecture reminder: the browser service ("browserd") runs inside the
session VM, binds `127.0.0.1` only, supervises a headless Chromium plus the
pinned `agent-browser` 0.33.1 engine, and speaks the desktop
`/api/v1/browser/commands` contract. Agent verbs never touch the control
plane. See `docs/cloud-development.md` "Session browser (Stage 1)" and
`docs/superpowers/specs/2026-09-17-cloud-browser.md` for the design.

Supported verbs: the full desktop set except `network-*`, `devtools-*`, and
`unhighlight` (they return `BROWSER_ACTION_UNSUPPORTED_CLOUD`). `open`
accepts `localhost:3000`-style targets, so VM-local dev servers work.

## Prerequisites (both paths)

- A checkout of a branch containing the Stage 1 work (or main once merged).
- Go toolchain per `backend/go.mod` / `cloud/go.mod`.
- Chromium/Chrome and the pinned engine binary available to the VM:
  - agent-browser 0.33.1, sha256 (from `frontend/scripts/prepare-agent-browser.mjs`,
    keep in sync):
    - linux-x64: `6e04d06605c4ca62da36e3263086e0f7ceae808b55508de2c3958d4b7fe430aa`
    - linux-arm64: `281cce8e3e9eb11fd823b13c085996d7361c35923ad454ce5cb06a5515630e9b`
- Sandbox shape: 1 vCPU / 2 GiB is enough (measured: ~330-465 MiB PSS for
  Chromium blank-to-loaded).

## Path A: Docker provider (full cloud stack, includes worker bootstrap)

This exercises the real `ao-worker` startup path: browserd starts with the
worker, capability env is injected into the agent terminal, `ao browser` is
the in-VM CLI.

### A1. Build the worker image

```bash
docker build --target worker -t ao-cloud-worker:local cloud/
```

Note: the image build resolves `github.com/aoagents/agent-orchestrator/backend`
through the `replace` pin in `cloud/go.mod`. On a fresh checkout of the
feature branch, make sure the pin points at a commit containing
`backend/pkg/browsercontract` and `backend/pkg/clibrowser` (after merge this
is automatic). Local sanity check:

```bash
cd cloud && GOWORK=off go build ./...
```

### A2. Start the local stack and spawn a session

```bash
cloud/scripts/test-cloud-local.sh        # full regression; teardown is automatic
```

For an interactive session you keep alive, bring the stack up manually with
the same variables the script exports (or copy the script and comment out its
`cleanup` trap), then spawn a session through the local API and note the
worker container. Quick identification:

```bash
docker ps --filter "label=ao.provider=docker" --format '{{.ID}} {{.Names}}'
```

### A3. Run verbs inside the worker

```bash
W=$(docker ps -q --filter label=ao.provider=docker | head -1)
docker exec $W ao browser status
docker exec $W ao browser open https://example.com
docker exec $W ao browser snapshot --interactive
docker exec $W ao browser act "the More information... link"
docker exec $W ao browser screenshot /tmp/shot.png --json
docker exec $W ao browser tabs
docker exec $W ao browser get url
```

Expected highlights (from the 2026-09-17 CreateOS run, identical shapes):

```text
Browser runtime: connected (vm-chromium)
<<<BEGIN UNTRUSTED EXTERNAL CONTENT>>>
https://example.com/
<<<END UNTRUSTED EXTERNAL CONTENT>>>
- heading "Example Domain" [level=1, ref=e1]
- link "Learn more" [ref=e2]
Acted on: link [ref=e2]
{"path": "/tmp/shot2.png", "size": 16601, "width": 780, "height": 437}
```

VM-local dev server inside the worker:

```bash
docker exec $W sh -c 'mkdir -p /tmp/www && echo "<h1>hi</h1><button id=b>Local button</button>" > /tmp/www/index.html && (cd /tmp/www && setsid nohup python3 -m http.server 3000 >/dev/null 2>&1 < /dev/null &) && sleep 1'
docker exec $W ao browser open localhost:3000
docker exec $W ao browser act "the Local button"
```

Resilience (kill the browser, next verb restarts it):

```bash
docker exec $W pkill -f user-data-dir
docker exec $W ao browser open https://example.org    # succeeds after lazy restart
```

Negative paths:

```bash
docker exec $W ao browser network start --duration 30
# -> This browser action is not available in cloud sessions yet
#    (BROWSER_ACTION_UNSUPPORTED_CLOUD) [request <uuid>], exit 1
docker exec -e AO_BROWSER_CAPABILITY= $W ao browser status
# -> usage error: capability is not set, exit 2
```

## Path B: CreateOS sandbox VM (no control plane needed)

Runs the same production `vmbrowser` code through the
`ao-browserd-standalone` developer binary, on a real remote VM. Nothing runs
on your machine except compilation and the `createos` control channel.

### B1. Create/reuse a sandbox

```bash
createos sandbox list                                  # reuse a running one
createos sandbox create --shape s-1vcpu-2gb            # or your usual flags
SB=<sandbox-id-from-list>
createos sandbox exec $SB -- bash -c 'hostname && free -m | head -2'
```

### B2. Install Chromium and the engine inside the VM

Ubuntu rootfs:

```bash
createos sandbox exec $SB -- bash -c '
  wget -q https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb -O /tmp/chrome.deb &&
  apt-get update -qq && apt-get install -y -qq /tmp/chrome.deb >/dev/null &&
  google-chrome-stable --version'
```

(The `/dev/fd/63` warnings the chrome wrapper prints under exec shells are
harmless.) Pinned engine:

```bash
createos sandbox exec $SB -- bash -c '
  mkdir -p /usr/local/lib/ao &&
  curl -fsSL https://github.com/vercel-labs/agent-browser/releases/download/v0.33.1/agent-browser-linux-x64 -o /tmp/ab &&
  echo "6e04d06605c4ca62da36e3263086e0f7ceae808b55508de2c3958d4b7fe430aa  /tmp/ab" | sha256sum -c - &&
  install -m 0755 /tmp/ab /usr/local/lib/ao/agent-browser && rm /tmp/ab &&
  /usr/local/lib/ao/agent-browser --version'
```

### B3. Build and push the two binaries

```bash
cd <checkout>/cloud
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /tmp/ao ./cmd/ao-cloud-agent
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /tmp/browserd ./cmd/ao-browserd-standalone
createos sandbox push $SB /tmp/browserd /root/browserd
createos sandbox push $SB /tmp/ao /root/ao
```

### B4. Start browserd in the VM

Background processes hold a plain `exec` channel open: start with `nohup`,
redirect, and read state back from files in a separate exec.

```bash
createos sandbox exec $SB -- bash -c '
  mkdir -p /root/bdata && cd /root &&
  AO_DATA_DIR=/root/bdata AO_SESSION_ID=sbx-1 \
  AO_CLOUD_CHROMIUM_PATH=/usr/bin/google-chrome-stable \
  AO_CLOUD_AGENT_BROWSER_PATH=/usr/local/lib/ao/agent-browser \
  nohup ./browserd > /root/browserd-env.json 2> /root/browserd.log < /dev/null &
  sleep 2; for i in $(seq 1 20); do [ -s /root/browserd-env.json ] && break; sleep 1; done;
  cat /root/browserd-env.json'
```

Expected: one JSON line with `sessionId`, `AO_BROWSER_CAPABILITY`, and
`AO_BROWSER_API_URL` (loopback). Save it:

```bash
createos sandbox exec $SB -- bash -c 'head -1 /root/browserd-env.json' > /tmp/browserd-env.json
```

### B5. Run the battery

Write the env exports once, then drive `/root/ao` (same commands as path A):

```bash
createos sandbox exec $SB -- bash -c '
  E=$(head -1 /root/browserd-env.json)
  export AO_SESSION_ID=$(printf "%s" "$E" | python3 -c "import json,sys;print(json.load(sys.stdin)[\"sessionId\"])")
  export AO_BROWSER_CAPABILITY=$(printf "%s" "$E" | python3 -c "import json,sys;print(json.load(sys.stdin)[\"AO_BROWSER_CAPABILITY\"])")
  export AO_BROWSER_API_URL=$(printf "%s" "$E" | python3 -c "import json,sys;print(json.load(sys.stdin)[\"AO_BROWSER_API_URL\"])")
  /root/ao browser status &&
  /root/ao browser open https://example.com &&
  /root/ao browser snapshot --interactive &&
  /root/ao browser act "the More information... link" &&
  /root/ao browser screenshot /tmp/shot.png --json &&
  /root/ao browser tabs'
```

For long batteries, write a script, push it, run it with output to a file
(again to avoid holding the exec channel), then pull the file:

```bash
createos sandbox push $SB battery.sh /root/battery.sh
createos sandbox exec $SB -- bash -c 'nohup /root/battery.sh > /root/battery.out 2>&1 < /dev/null & echo started'
# later:
createos sandbox pull $SB /root/battery.out battery.out
```

### B6. Cleanup

```bash
createos sandbox exec $SB -- bash -c 'pkill browserd; pkill -f user-data-dir=/root/bdata; rm -rf /root/bdata'
# or destroy the sandbox entirely:
createos sandbox rm $SB
```

## Troubleshooting

- **`Session name ... too long. Socket path would be N bytes (max 103)`**:
  fixed by the engine's short hash namespace + short `/tmp` socket dir; if
  you see it, your build predates commit `61af6f68f`.
- **ImageMagick usage text after `unable to disable agent-browser
  streaming`**: the runner exec'ed argv[0] as the binary; same fix commit
  `61af6f68f`.
- **`exec` hangs after starting a background process**: expected with plain
  backgrounding; always `nohup ... < /dev/null &` and capture output to
  files, then read them in a fresh exec.
- **`refusing to overwrite existing screenshot`**: by design; screenshots
  never clobber; use a fresh path.
- **Chromium dead after idle (30 min)**: by design; the next verb lazily
  restarts it.
- **Memory**: ~330 MiB PSS blank page, ~465 MiB loaded on Chrome 153,
  1 vCPU/2 GiB shape verified with headroom.

## What manual testing does not cover

- The worker-bootstrap wiring itself (`startBrowserd` env injection into the
  agent terminal) is exercised by `cloud/cmd/ao-worker` unit tests and Path
  A, not by Path B (the standalone binary replaces the bootstrap on purpose).
- The 30-minute idle shutdown is unit-tested at millisecond scale; do not
  wait for it manually.
