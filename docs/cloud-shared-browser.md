# Shared browser sessions

PR #5543 contains the session-local Chromium service, browser commands, live
viewer and VM DevTools. Cold-start preparation, composer reuse and checkout
optimization are a separate change. This branch works with ordinary session
creation and does not require preparation endpoints or migrations.

## Architecture

Chromium and the pinned browser-command executable live in the worker image.
The worker starts a loopback service protected by a per-session capability.
Chromium starts only after explicit browser intent: a command or viewer attach.
The desktop does not render the remote site's DOM. It paints streamed frames
and sends bounded input controls to that same worker browser.

The viewer uses an authenticated WebSocket relay, not the durable webhook or
worker-request queues. Tickets are single-use and worker-epoch-bound. Origin
checks, session access, operate permission and ownership checks remain in force
after attach. Input acknowledgements report acceptance or rejection; stale
surface input and frames are discarded. Replacing a viewer requires explicit
retry rather than two viewers repeatedly taking control from each other.

## DevTools

Choose **Browser controls > Open DevTools** with a connected, controlling
viewer. F12 also toggles DevTools while the remote surface has keyboard focus.
The worker opens Chromium's bundled inspector for the selected page and streams
it through the same viewer. Elements, Console and Network inspect the VM page.
Closing DevTools returns to the page without closing it or changing the agent's
selected page. Switching tabs or disconnecting closes the inspector.

Only one surface is streamed at a time: page or inspector, not two side-by-side
views. No raw debugging endpoint, arbitrary protocol method, or inspector token
is exposed to the desktop. Opening from `ao browser devtools-open` requires an
attached viewer. Older workers without support leave the control unavailable.

## Configuration and compatibility

- `AO_CLOUD_BROWSER_VIEWER=1` enables the relay in the control plane and worker.
- `AO_CLOUD_BROWSER_VIEWER_ORIGINS` lists exact additional renderer origins.
  Packaged desktop origin handling is separate. Do not expose the worker CDP
  port or allow wildcard origins to make a test pass.
- Rebuild worker images when updating worker code. The Docker image includes
  Chromium and a per-architecture checksum-pinned browser executable.
- The relay currently assumes matching worker/viewer routing to the same
  control-plane replica. Hosted multi-replica routing remains an operational
  prerequisite, not something established by a local Docker run.

Browser ticket storage uses existing tables. Preparation migrations and local
task-delegation changes are not part of this PR. Both independent PRs include
the same small Docker test-support changes and local Docker sign-in entry.

## Automated checks

```sh
cloud/scripts/test-cloud-browser-viewer.sh --local
cd cloud && GOWORK=off go test -race -count=1 ./...
```

Run the complete backend and frontend suites and cloud-client contract checks
as well. Set `AO_TEST_DATABASE_URL` to a disposable database for ticket/interaction
persistence tests. The real Chromium test is opt-in with
`AO_TEST_REAL_BROWSER=1` inside the worker image, where the pinned browser
executable and Chromium exist. It covers editing, resize, native inspector
panels, console effects on the inspected page, and disconnect cleanup.

## Manual desktop review

Use `.agents/skills/ao-desktop-dev/SKILL.md` to launch the real Electron app from
an isolated checkout. Start the local Docker control plane, sign in through its
local auth flow, choose Docker, and start a disposable cloud session. Do not
substitute a mock page or browser-only renderer for the desktop shell.

1. Before opening Browser, inspect the worker process list: Chromium must be
   absent. Open Browser and navigate to a development server running inside
   the worker, such as `http://localhost:3000`.
2. Type, click, paste and scroll. Resize the panel, open/switch/close tabs,
   navigate back/forward, reload, and handle a page dialog. Confirm the agent's
   browser snapshot sees the same page and values.
3. Open DevTools. Inspect Elements, change a field through Console, and inspect
   a page request in Network. Close it and confirm the page change remains.
   Repeat after tab switching and reconnecting.
4. Exercise read-only access, temporary disconnect, explicit retry, viewer
   replacement, worker replacement and agent ownership. Confirm clear state,
   no unauthorized input, no duplicate actions, and no stale-page input.
5. Close the viewer and terminate the test session. Confirm inspector targets
   are removed and worker resources are cleaned up. Browser commands without
   a viewer must still work; DevTools open without a viewer must explain why it
   cannot open.

Report the worker image, platform, URL type, viewer ownership, exact action and
sanitized error. Include a real app capture where useful, without credentials,
private page content or ticket URLs. Docker verification does not prove hosted
provider authentication, provisioning latency or multi-replica routing.

## Limits

Native DevTools Network is available for human inspection. The separate
`ao browser network-*` capture API is still unsupported. Download/file transfer,
profile persistence across sandbox replacement, and side-by-side DevTools are
not implemented by this change. Earlier Stage 1 notes are historical; this
runbook and the DevTools spec describe the current browser scope.

See the [split validation report](superpowers/reports/2026-09-26-cloud-shared-browser-validation.md)
and [native desktop evidence](screenshots/pr-5543/README.md) for observed results
and the remaining hosted-provider and platform coverage gaps.
