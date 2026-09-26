# Cloud cold-start validation report

Date: 2026-09-22

## Result

The cold-start interaction is implemented and passes the local Docker, desktop,
transport, lifecycle, failure, contract, type, build, race, and focused
regression checks described below.

The user can type immediately into a pending session. Session creation uses a
stable idempotency key, additional messages are saved with stable keys and a
monotonic client sequence, and the client flushes them in order after the
control plane accepts the session. The pending surface remains until a visible,
input-enabled terminal crosses the first-frame boundary.

Opening the Cloud task composer now starts a hidden, promptless cold session.
Provider allocation, worker connection, and repository preparation advance
while the user types. Start Task atomically reveals that same session and
stores the first prompt as one durable turn. Closing the composer now detaches
it with a fresh two-minute lease. A compatible reopen in the same renderer
process reuses the hidden session. Server expiry still reclaims idle, crashed,
and abandoned preparations.

No warm capacity was used. Every measured session received a fresh worker.
Browser startup remained lazy: the test first proved that Chromium was absent,
then issued an explicit browser command and measured readiness. A prompt that
mentions a browser does not start Chromium by itself. The reliable intent signal
is the first browser operation requested by the coding harness.

One product-validation gap remains:

- No supported hosted-provider credentials were present, so no remote latency
  claim was made.

The earlier frontend browser failures were repaired. The hook fixture passes 20
consecutive runs when its local fixture process is permitted. A separate full
backend lint attempt still finds unrelated branch-level drift in tmux contracts,
inherited worker-session environment, and the expected bundled runtime asset.
Backend build, vet, and static analysis pass, and no backend package was changed
for this Cloud implementation.

## Implemented behavior

- Immediate pending-session navigation and a focused composer.
- Click-triggered hidden preparation with commit, cancellation, and expiry.
- Stable create and message idempotency keys for safe retries.
- Ordered early-message delivery with `clientSequence` values.
- Durable startup events for checkout, restore, workspace, agent launch, ready,
  and bounded failure states.
- Credential preparation and terminal reservation overlap repository setup.
- Reconcile wakes interrupt the normal polling interval.
- Stream with relay, stream with durable delivery, and polling fallback.
- Worker replacement, pause and resume, control-plane restart, full-stack
  restart, persistent workspace storage, and cleanup.
- Lazy browser startup with bounded restart behavior.
- Non-shallow, single-branch, blobless checkout of the known default branch,
  with recovery when project branch metadata is stale.
- Explicit terminal replay completion, including frame buffering before
  listeners subscribe and a reset-only replay that cannot reveal an empty pane.
- A database readiness check that rejects the temporary first-run PostgreSQL
  postmaster. This fixes a race where migration could connect just as the
  temporary server shut down.

## User-visible desktop timing

The real Electron app ran from an isolated checkout with isolated data and the
local Docker control plane. The window was mapped, focused, and visibly painted.
A renderer-side observer measured one task submission:

| Boundary | Time after submit |
| --- | ---: |
| Pending session visible | 339 ms |
| Durable session route bound | 822 ms |
| Sandbox provisioned | 301 ms |
| Browser terminal ready | 861 ms |
| First terminal output accepted | 1,314 ms |
| Terminal uncovered and input-ready | 1,751 ms |

The server provisioned the fresh Docker sandbox in 205 ms. The terminal ticket
returned 201, then the browser terminal attached and became ready. The first
output followed, the renderer consumed the explicit replay boundary, xterm
completed its paint preparation, and the pending surface disappeared. The final
frame showed a connected state, no replay cover, and enabled terminal input.
This synthetic run validates the renderer and transport boundaries only. It is
not production harness evidence and must not be presented as the shipped UI.

An earlier 120-second apparent stall was invalid test evidence. The isolated
window was mapped on an inactive compositor workspace, which paused animation
frames even though the document reported itself visible. Focusing the exact
window allowed the pending paint boundary to complete. The final measurement
was taken only after the window was focused.

## Click-triggered preparation follow-up

The final local Docker lifecycle started a hidden preparation at the simulated
New Task action. The fresh worker reached a running harness in 3,392 ms before
the simulated Start Task action. Commit reused the same session identifier,
made it visible once, and delivered the first prompt once. The same run also
proved explicit cancellation, server expiry, provider deletion, and session
termination.

This was a cached-image local run against the small smoke repository. It
validates the control flow and establishes a local lower bound. It does not
establish a hosted-provider percentile.

The final transport rerun also passed stream with relay, stream with durable
delivery, and polling fallback. The first uncached relay sample reached agent
readiness in 18,015 ms. A later cached polling sample reached it in 4,148 ms.
The lifecycle run then exercised control-plane restart, full-stack restart,
worker replacement, pause and resume, and persistent workspace recovery.

## Reconnect grace follow-up, 2026-09-23

The hidden preparation now has an explicit renewal contract. Prepare and renew
responses return the authoritative expiry and fixed 120-second lease. Renewal
locks the session and sandbox together, applies one database timestamp to both
rows, and rejects expired, committed, unavailable, foreign-organization, and
foreign-user preparations with stable responses.

The renderer keeps compatible preparations in a process-scoped registry.
Closing the composer releases its attachment and performs one best-effort
renewal without deleting the sandbox. Reopening reuses the same session and
renews it immediately. Prompt edits, attachment changes, and task-setting
changes coalesce into at most one renewal every 45 seconds. An untouched open
composer performs no periodic keepalive. Harness, provider, project, account,
or organization changes still invalidate and delete the old preparation.

Commit retries ambiguous responses with the same key. An explicit expiry keeps
the pending prompt intact and falls back to one fresh durable session. Lifecycle
telemetry records acquisition, reuse, detach, reattach, renewal outcome, expiry,
and commit recovery without recording draft content.

Observed validation:

- focused renderer and Cloud client checks passed 73 tests;
- frontend typecheck passed;
- the Cloud module passed its full test suite, full race suite, vet, and build;
- the Docker lifecycle passed preparation metadata, renewal, equal session and
  sandbox deadlines, commit idempotency, post-commit rejection, forced-expiry
  rejection, provider cleanup, restart, replacement, transport, checkout, and
  lazy-browser regressions;
- the prepared worker reached ready in 4,313 ms in that cached-image local run;
- the broad frontend suite passed 4,848 tests and skipped 7. Its remaining 120
  failures are environment-only: 102 archive fixtures require a missing `zip`
  executable, and 18 profile-import cases require rebuilding a native module
  for the host Node ABI.

The lifecycle run removed its Compose services, volumes, network, workers, and
scratch state. No warm capacity or implicit browser startup was introduced.

## Five-run local Docker distribution

These numbers begin with session creation after the local control plane is
ready. Worker images and container layers were cached. Control-plane build and
boot time are excluded. With only five samples, observed p95 equals the maximum.

| Boundary | p50 | p95 / max |
| --- | ---: | ---: |
| Session accepted | 14 ms | 16 ms |
| Early message accepted | 23 ms | 25 ms |
| Worker connected | 237 ms | 239 ms |
| Worker ready | 237 ms | 342 ms |
| Checkout, restore, workspace, and agent ready | 1,166 ms | 1,269 ms |
| Early message delivered | 1,171 ms | 1,273 ms |
| Explicit browser command to browser ready | 895 ms | 933 ms |

Individual session-ready results were 1,269 ms, 1,166 ms, 1,169 ms, 1,165 ms,
and 1,166 ms. Early delivery followed at 1,273 ms, 1,170 ms, 1,173 ms,
1,169 ms, and 1,171 ms.

## Transport and host-load variations

| Mode | Session accepted | Worker ready | Agent ready | Early delivery | Browser command |
| --- | ---: | ---: | ---: | ---: | ---: |
| Stream plus relay | 119 ms | 8,115 ms | 9,965 ms | 10,471 ms | 18,229 ms |
| Stream plus durable mirror | 14 ms | 237 ms | 1,063 ms | 1,067 ms | 972 ms |
| Polling fallback | 14 ms | 238 ms | 1,576 ms | 1,579 ms | 879 ms |

The first row is a real cold-host and host-load outlier, not the normal cached
distribution. It is retained because it demonstrates that the pending input and
exactly-once delivery still work through a roughly 10-second worker start.

The lifecycle continuation passed child coordination, pause and resume, worker
replacement, control-plane restart, full-stack restart, workspace persistence,
and cleanup.

## Checkout comparison

All required operations passed for every candidate: status, log, blame, diff,
branch, fetch, rebase, submodule handling, and large-file pointer handling.

| Repository | Candidate | Clone median | First omitted blob | Git bytes | Missing objects before access |
| --- | --- | ---: | ---: | ---: | ---: |
| Small | Full | 14 ms | 2 ms | 70,389 | 0 |
| Small | Partial | 26 ms | 10 ms | 81,774 | 15 |
| Small | Protocol-tuned | 21 ms | 10 ms | 81,774 | 15 |
| Medium | Full | 22 ms | 2 ms | 91,152 | 0 |
| Medium | Partial | 30 ms | 10 ms | 115,877 | 39 |
| Medium | Protocol-tuned | 29 ms | 10 ms | 115,877 | 39 |
| Large | Full | 36 ms | 2 ms | 133,910 | 0 |
| Large | Partial | 46 ms | 11 ms | 184,818 | 69 |
| Large | Protocol-tuned | 48 ms | 10 ms | 184,818 | 69 |

The synthetic matrix alone favored full checkout for these tiny repositories.
A later representative checkout transferred a 289.55 MiB Git pack before user
work began, so the production decision changed for known project branches. The
worker now uses a non-shallow, single-branch, blobless clone. It retains commit
and tree history for that branch, materializes the current worktree before
launch, and fetches historical blob contents on demand. If project branch
metadata is stale, the worker resolves the remote symbolic HEAD once and
retries the same optimized clone. Hosted measurements must still quantify the
transfer reduction on representative repositories.

## Failure matrix

The following owning boundaries passed:

- Checkout denial prevents agent launch.
- Credential fetch failure publishes a bounded startup failure.
- Restore failure stops startup.
- Process launch failure cleans up the terminal reservation.
- Stream disconnect falls back to durable delivery, while permanent rejection
  stops retrying.
- Repeated browser launch failure parks Chromium rather than looping forever.
- The renderer projects startup failures without enabling raw terminal input.

## Regression results

- Frontend unit suite: 327 files passed, 4,953 tests passed, 7 skipped.
- Frontend browser suite: 88 passed, 4 performance workloads skipped.
- Frontend typecheck, end-to-end typecheck, and packaged desktop build: passed.
- Cloud client: 22 tests, typecheck, generated-schema drift check, and build
  passed.
- Product UI: 128 tests, typecheck, and build passed.
- Cloud module: tests, race detector, vet, and build passed.
- Backend: build and vet passed. Static analysis reported zero issues. The hook
  fixture passed 20 consecutive runs and its complete package passed when local
  fixture processes were enabled.
- Docker Compose configuration validation passed after the PostgreSQL health
  check change.
- Focused pending-session, startup projection, timing, telemetry, contract,
  lifecycle, failure, browser, and checkout checks passed.
- The click-preparation focused frontend suite passed 39 tests. The broader
  affected frontend suite passed 109 tests.
- The final local variation matrix passed all transport modes and the complete
  preparation lifecycle.

The full backend `npm run lint` command is not green on this branch. Its
unrelated failures include tmux tests that expect commands without the named
`ao` socket while the implementation emits that socket, CLI tests that inherit
this worker's session identifier, system checks that expect a bundled tmux asset
outside the checkout, and shell integration that assumes POSIX parameter syntax
while this host is configured for fish. The Cloud module has its own complete
test, race, vet, and build pass, so these failures do not reduce the Cloud
cold-start evidence. They should be resolved with the backend-cleanup branch
owner rather than folded into this feature.

## Hosted-provider limitation

The implementation supports the configured hosted providers, but this workspace
contains no matching provider environment variables or deployment credentials.
Only the example environment file is present. A deployment was neither required
nor authorized for this local implementation task, so remote fresh-start,
restore, and browser percentiles were not measured.

Do not use the local Docker percentiles as hosted-provider promises. Remote
numbers must be gathered against the actual provider, region, image cache
state, repository size, and network path.

## Cleanup

The isolated desktop app and daemon were stopped. The five worker containers,
three Compose containers, five workspace volumes, Compose network, test-specific
image tags, temporary desktop worktree, scratch application data, PostgreSQL
data, fixture tooling, measurement scripts, screenshot, credential scratch
paths, and transfer file were removed. No unrelated container was touched.
