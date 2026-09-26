# Independent cold-start preparation validation

Date: 2026-09-26

The preparation-only branch starts from main at `460d9c45f`. It includes
composer preparation, reuse, checkout gating, early-message ordering, durable
provider cleanup and local submission idempotency. It contains no shared
browser runtime, viewer endpoint or Chromium image changes.

## Executed checks

| Check | Result |
| --- | --- |
| Backend build and vet | Passed, Go 1.27.1, standalone module |
| Complete backend race suite | Passed on the complete rerun |
| Cloud build, vet, complete race suite | Passed with a disposable Postgres 17 database |
| Backend lint | Passed, pinned v2.13.2 |
| API and sqlc regeneration | No artifact drift |
| Cloud client generation, typecheck, tests, build | Passed, 23 tests |
| Frontend and E2E typechecks | Passed |
| Full frontend tests | 338 files passed; 5,323 passed, 7 skipped |
| Full renderer smoke | 60 passed with one worker |
| Product UI typecheck, tests, package dry-run | Passed |
| Frontend docs build | Passed |
| Docker preparation and lifecycle smoke | Passed with fresh workers and cleanup |
| Startup measurement and variation-script self-tests | Passed |

Frontend checks used Node 24.21.0 and separate locked installs. Persistence
tests used the normal migrations, including Postgres 42 through 45. The Docker
lifecycle exercised preparation renewal, commit, expiry, restart and replacement
without a browser endpoint or browser-ready requirement.

## Local cold-start measurement

A fresh Docker worker, with image build time excluded, produced these elapsed
times from session submission:

| Milestone | Seconds |
| --- | ---: |
| Session accepted | 0.102 |
| Early message stored | 0.300 |
| Worker connected | 8.721 |
| Runtime running | 8.824 |
| Checkout started | 9.033 |
| Checkout complete | 16.630 |
| Workspace ready | 16.937 |
| Harness ready | 17.256 |
| Early message delivered | 17.760 |

This is one local Docker sample with the test harness, not a user-provider login
test or a hosted cold-start promise. Browser readiness is absent from the
preparation-only result. The separate lifecycle run recorded 3.495 seconds to
preparation readiness; its clock and scenario differ from the submission run.

## Native desktop evidence

See [the captures and recording](../../screenshots/cloud-preparation/README.md).
An editable draft started one hidden preparation after harness selection.
Closing and reopening with the same configuration retained preparation
`7c33fe2f` and worker `7c0df0a64601`. The existing orchestrator was a separate
worker. Two earlier idle preparations had terminated. No task was submitted.

The account used local development auth and a placeholder harness credential.
These observations establish preparation/reuse, not hosted authentication or
successful task execution. Menu triggers used keyboard activation; harness
selection used its DOM handler in the actual Electron app.

## Failures and remaining coverage

The first backend race run failed only `TestServerShutdownEndpoint` at its
five-second graceful-shutdown deadline during concurrent image builds. The
complete rerun passed without source changes; the first failure remains part
of the record. The first renderer run passed 56 tests and timed out in four readiness or
focus cases. A full one-worker rerun passed all 60 without source changes.

The local recombination audit recovered all runtime source, generated API
artifacts and package tests from combined snapshot `b2e13feb4`. Six overlapping
files need ordinary resolution when both PRs land. The worker-startup test also
needs the browser environment argument restored when both features are present.

Native Windows/macOS jobs, packaged release builds and hosted Coder/NodeOps
authentication/provisioning were not run. The transport-variation self-test
passed; the complete Docker transport matrix was not rerun for this split.
Remote checks must run on the new PR after publication.
