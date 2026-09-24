# Cloud browser review fixes

Baseline: `e18b13b07`. Scope: five review findings and the process-wait race
reproduced while testing browser startup.

## Changes

- Coalesce Chromium startup through readiness polling and restart backoff.
  Fence canceled callers and stop requests. Share one process wait between
  the watcher and shutdown path.
- Close replaced viewers with a distinct WebSocket code. Require explicit
  retry rather than allowing two viewers to repeatedly replace each other.
- Publish preparation reattachment readiness before waiting for detach, so
  an immediate submission cannot consume the previous attachment.
- Match completed frame paints by URL and sequence. Accept a completed frame
  when newer frames are queued, while rejecting stale connection/epoch paints
  and bounding retained frame metadata.
- Renew the interaction lease for authorized browser input. Throttle renewal
  and fence read-only access, inactive sessions, and stale workers. Passive
  frames, pings, and pointer hover do not keep a session active.

## Automated verification

- Full frontend suite on Node 24.21.0: 341 files passed, 5,327 tests passed,
  7 skipped. Frontend typecheck passed. The focused suite passed 43 tests.
- Full cloud `go test -race -count=1 -p 2 ./...`, `go build ./...`, and
  `go vet ./...` passed. Database coverage used disposable Postgres 17 and
  a non-superuser runtime role.
- Chromium concurrency, cancellation, and real child-process wait/stop
  regressions passed ten repetitions with the race detector.
- Regression cases were observed failing before their fixes. Go formatting
  and diff checks passed afterward.

The frontend run required an available system zip executable and a Node 24
native SQLite binding. After verification, the original Node 22 binding was
restored and its load checked. These were local environment prerequisites,
not skipped packaging tests.

## Native desktop verification

An isolated checkout ran the real Electron shell and daemon against a local
Docker control plane. Control-plane, worker, and desktop backend binaries
were rebuilt from the changed source. The cached worker runtime image was
reused with updated binaries; no image was published.

The worker served the input-check page inside its container. Desktop typing
updated the remote field and its echoed value while frames arrived. A second
viewer connection triggered the replacement notice, which remained for eight
seconds without automatic reconnection. Clicking Retry restored the page;
subsequent text input was visible in both the field and echoed value.

[Screenshots and capture limitations](../../screenshots/pr-5543/README.md#review-fixes-2026-09-25)
document the actual app. The attempted screen recording was omitted because
a host compositor recovery dialog obscured the window.

## Boundaries

Development authentication, a placeholder harness credential, and a public
repository were used. This establishes the Docker browser relay and native
viewer behavior, not hosted-provider authentication, Coder/NodeOps cold-start
latency, or real task execution. Browser lease rejection and preparation
reattachment ordering were verified by automated tests, not these screenshots.
Other native operating systems and hosted CI must be checked separately.
