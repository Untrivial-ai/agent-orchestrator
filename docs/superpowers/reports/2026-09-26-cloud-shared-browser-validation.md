# Independent shared-browser validation

Date: 2026-09-26

The browser-only branch targets main at `460d9c45f`. Its implementation is
preserved from combined snapshot `b2e13feb4`, including VM DevTools. Preparation
routes, checkout optimization, pending-task UI and preparation migrations are
absent. This report records local checks, not hosted CI results.

## Executed checks

| Check | Result |
| --- | --- |
| Backend build, vet, complete race suite | Passed, Go 1.27.1, standalone module |
| Cloud build, vet, complete race suite | Passed with a disposable Postgres 17 database |
| Backend lint | Passed, pinned v2.13.2 |
| API and sqlc regeneration | No artifact drift |
| Cloud client generation, typecheck, tests, build | Passed, 23 tests |
| Frontend and E2E typechecks | Passed |
| Full frontend tests | 336 files passed; 5,301 passed, 7 skipped |
| Full renderer smoke | 60 passed with one worker |
| Product UI typecheck, tests, package dry-run | Passed |
| Frontend docs build | Passed |
| Docker lifecycle and viewer smoke | Passed with fresh workers and cleanup |
| Real Chromium editing and native DevTools | Three consecutive repetitions passed with race detection |

Frontend checks used Node 24.21.0 and separate locked installs. The Docker
lifecycle covered ordinary session creation, delegation, pause/wake, replacement
and control-plane restart. It did not call preparation endpoints. The database
migrated through version 41, with no preparation schema.

The latest viewer run measured 251 ms from attach to first frame, 13 ms for user
input to become visible, 65 ms for session-side input, 20 ms to reconnect,
44 ms to recover a slow viewer, and 775 ms to recover from a Chromium crash.
These are one local Docker sample, not hosted latency estimates. Replay
prevention, tab controls, and shared command/viewer state were asserted.

## Failures retained in the record

The first smoke shell was edited while it was still reading its script and was
discarded. A later lifecycle run observed a closed old terminal epoch during
deliberate worker replacement. The test now waits, within its existing bounded
deadline, for the replacement terminal instead of treating that transient old
epoch as a final exit. Ordinary initial-session errors remain strict.

An initial renderer run during concurrent image builds passed 58 tests and
timed out in two readiness/focus cases. The complete one-worker rerun passed
all 60 without source changes. The first real Chromium repetition under load
hit browser and DevTools startup deadlines; two later repetitions passed. A
separate three-repetition run after the builds passed in full. Bounded startup
errors still require retry on a sufficiently overloaded worker.

## Native desktop evidence

See [the captures and recording](../../screenshots/pr-5543/README.md).
They show the actual isolated Electron app, a VM-local page, and its native
Elements inspector. Page/inspector switching was observed. Keyboard and DOM
automation were used; host physical-pointer behavior was not established by
this run. The worker-side regression separately exercised inspector Console,
Elements, Network, editing, resize and cleanup.

## Split audit and remaining coverage

A local recombination audit against the preserved snapshot recovered every
runtime source file, generated API artifact and package test byte-for-byte.
Six overlapping files required normal conflict resolution: worker startup,
the Postgres test fixture, the lifecycle smoke script, telemetry implementation
and test, and cloud-client tests. The startup tests also regain the browser
environment argument when both features are present. Only scoped documentation,
media placement and measurement fields intentionally differ after recombining.

Native Windows/macOS jobs, packaged release builds, hosted provider login,
Coder/NodeOps provisioning and multi-replica relay routing were not run here.
Local auth and placeholder harness credentials do not prove provider login or
task execution. New remote checks must run after publication; the earlier
combined branch's green checks do not certify this split.
