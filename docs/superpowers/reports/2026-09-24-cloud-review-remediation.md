> Historical combined-branch record. Preparation changes are now reviewed
> separately. For the current browser scope and test instructions, see
> [Shared browser sessions](../../cloud-shared-browser.md).

# Cloud review remediation: local verification

Date: 2026-09-24
Baseline: `9a92b900e9641bf75f1f7428edec26491017164d`
Specification: [review remediation](../specs/2026-09-24-cloud-review-remediation.md)

Status: remediation committed locally as `432136da5`; publication requested.
Automated regression checks pass. Native desktop evidence was added during
publication preparation. Remote-provider validation remains outstanding.
No worker image publication or deployment was performed.

## Publication preparation

The real Electron app ran from an isolated checkout with scratch data and fresh
control-plane and worker binaries on Docker. It used development authentication,
a placeholder harness credential, and a seeded public repository. No real
provider credential was imported. Provider authentication was not established.

Native paste initially failed: the canvas's default pointer action reclaimed
focus from the hidden editable input. Canceling that default action preserves
the input focus. The regression test checks both cancellation and focus.
Actual desktop Edit Paste then delivered the OS clipboard text to the worker
page. Text replacement, visible focus, resized frames, and viewer reattachment
were also observed. Native undo/redo was attempted but not independently
asserted; its component and worker-side checks remain separate.

[Screenshots and a 43.6-second native recording](../../screenshots/pr-5543/README.md#review-follow-up-2026-09-24)
show the real application and worker-hosted page. The local images only replace
application binaries in the cached worker base; they are not published releases.

The focused browser suite passed all 26 tests after the pointer-focus fix.
The final frontend typecheck and complete suite passed: 341 files, 5,324 tests,
7 skipped. Logs: `/tmp/ao69-publish-typecheck.log` and
`/tmp/ao69-publish-frontend-full.log`.

The workflow-pinned backend linter initially found six missing exported-contract
comments and an unchecked deferred rollback. After those fixes, the full linter
reported zero issues. Backend build/vet and the complete domain/store race suites
passed again. E2E typecheck and shared UI typecheck/tests also passed (133 tests).
Logs: `/tmp/ao69-publish-lint.log`, `/tmp/ao69-publish-store-final.log`, and
`/tmp/ao69-publish-product-ui.log`.

The evidence app, daemon, scratch checkout/profile/home, Docker services,
workspace volume, local image tags, and ephemeral stack secrets were removed.
Pre-existing demo services were left untouched.

## Follow-up review and verification

The [follow-up specification](../specs/2026-09-24-cloud-review-followup.md) addresses
the issues found while reviewing the first remediation:

- Rejected viewport changes now close the viewer with a persistent reconnect
  error. Late frames cannot hide the error. The existing retry action obtains a
  new ticket and requires acknowledgement plus a painted resized frame before
  accepting input. Expected agent-control contention keeps the viewer attached.
- Older worker epochs and unsupported stream versions now stop with actionable
  worker-image upgrade guidance. This deliberately rejects incompatible workers;
  it does not add backward-compatible operation or weaken input fencing.
- Migration 0157 distinguishes seeded, starting, and ready local delegations.
  An unclaimed seed resumes with the same identity. A database compare-and-swap
  chooses one startup owner across independent connections. Both terminal and
  chat paths mark startup ready only after manager setup completes.
- Uncertain starting outcomes return recovery required, including the known
  session identity on reservation replay. They never report success or start a
  second runtime. Daemon reconciliation preserves the reservation instead of
  deleting the seed. It reloads completed records rather than acting on a stale
  boot-time seed snapshot. Confirmed rollback still permits retry.

Final follow-up checks, all exit zero:

- Frontend typecheck and the full suite: 341 files passed, 5,324 tests passed,
  7 skipped. Two workers limited concurrent memory use.
- Backend build, vet, and full `go test -race -count=1 -p 2 -timeout=20m ./...`.
  SQLite's complete migration suite passed, including 0157 upgrade behavior.
- Build, vet, and the complete session-manager race suite were rerun after the
  final stale-snapshot guard, not just its focused regression.
- Focused service, manager, real SQLite concurrency/rollback, and migration
  checks passed. Browser tests cover rejected resize, reconnect, ownership
  release, and old-worker frames and controls.
- sqlc regeneration produced identical hashes; formatting and diff checks pass.

Evidence: `/tmp/ao-followup-69-backend-full.log`,
`/tmp/ao-followup-69-backend-final-check.log`,
`/tmp/ao-followup-69-backend-focused.log`,
`/tmp/ao-followup-69-startup-final.log`,
`/tmp/ao-followup-69-frontend-full.log`, and
`/tmp/ao-followup-69-typecheck.log`.

No desktop or remote-provider run was added during this follow-up. The wider
LOC cleanup remains separate. An uncertain external startup still needs explicit
inspection; automatic recovery cannot assume that a missing handle means no
runtime was created. The follow-up used scratch homes and temporary directories,
not provider credentials or the real application data. AO reporting and PR claim
refresh remained unavailable because the daemon run-file was stale.

## Finding coverage

| Finding | Implementation | Evidence |
|---|---|---|
| R1 | Startup messages can flush after readiness; pending acknowledgements keep the composer visible | Readiness-spanning draft, delayed response, and retry tests |
| R2 | Reserved terminals cannot bypass durable turns; current-worker readiness and FIFO are required | Real Postgres test replaces the worker and recovers four instructions in order |
| R3 | Persist creation obligations before provider calls; retain late results and retry deletion | Canceled context, lost lease, failed first deletion, replacement protection, and real Postgres tenant isolation |
| R4 | Nonblocking bounded CDP event queue, explicit overflow disconnect, command deadlines | Real loopback WebSocket event bursts, overflow, cancellation, and race tests |
| R5 | Reject stale epochs and invalid/replayed sequences; cache prior control outcomes | Duplicate text executes once; stale epoch, eviction, and safe-integer epoch tests |
| R6 | Recheck composer attachment ownership after delayed preparation | Close/reopen before creation returns uses one create and no stale detach |
| R7 | Forward editing shortcuts with virtual key codes; bounded plain-text paste; retain editable pointer focus | Component and actual Chromium checks, plus native desktop selection, replacement, focus, and clipboard paste |
| R8 | Require the matching resized frame to paint before coordinate input | Delayed painting, rapid resize acknowledgements, stale captured frames, and actual Chromium resize reversal |
| R9 | Serialize first runtime initialization and avoid rewriting successful configuration | Concurrent first-use race test and initialization retry |
| R10 | Normalize timeout before constructing the process runner | Default/explicit timeout checks and real blocked-process termination by runner and caller deadlines |
| R11 | Atomically bind reservation and worker seed; prevent legacy ambiguous retries | SQLite concurrency/rollback tests, manager restart without duplicate runtime, service restart/finalization tests, and upgrade guard |
| R12 | Preserve omitted message sequence; reject invalid explicit values | Legacy request, positive sequence, null, zero, negative, fractional, malformed, and unsafe-integer tests |

The shared empty browser snapshot now has one definition. This remediation does
not attempt the review's wider estimated LOC reduction or a general refactor.

## Original remediation checks

- Frontend: all 341 test files passed, 5,317 tests passed, 7 skipped.
- Frontend project and E2E TypeScript checks passed. The renderer production
  build passed with the existing large-chunk warning. This is not an Electron
  installer/package build.
- Cloud: `go test -race -count=1 -p 2 ./...`, build, and vet passed. Recovery tests
  used disposable Postgres 17 with a non-bypass runtime role and forced tenant
  policies. This includes migrations 00044 and 00045.
- Real browser: a freshly compiled test binary using the changed viewer code ran
  inside the cached worker image with its real Chromium and command binary.
  Three fresh browser runs passed resize reversal, pointer focus, text entry,
  select-all/replacement, undo, and redo. Chromium was stopped before viewer
  intent. These runs do not measure whole-session cold-start latency.
- Cloud client: generation was repeatable, typecheck passed, and 24 tests passed.
- The updated browser smoke utility compiled. Its full application/relay launch
  flow was not run in this remediation.
- Backend: build, vet, and `go test -race -count=1 -p 2 -timeout=20m ./...`
  passed in the isolated environment. This includes the full migration suite,
  migration 0156's legacy-row upgrade test, and complete affected packages.
- sqlc regeneration was repeatable; only the two expected generated files changed.
- `git diff --check` passed.

## Environment failures and corrections

The first complete frontend attempts hit a missing `zip` executable and temporary
disk quota errors. A distribution-supplied ZIP utility was extracted into a
disposable directory without installing a system package. After removing only
the redundant compiler cache used by this task, the complete frontend rerun
passed.

Backend verification also encountered a disappearing shared compiler cache.
The replacement run uses a checkout-private compiler cache, a scratch home,
temporary files outside AO state, a private terminal socket directory, and a
clean environment without inherited provider credentials. Earlier runs under
the unsuitable environment are not counted as passing full-suite evidence.

## Remaining boundaries

- Native Electron input, focus, resize, and reattachment evidence is now attached.
  Native undo/redo was not independently asserted in that run. Component and
  worker-side coverage does not establish desktop shortcut behavior.
- Remote Coder/NodeOps provisioning, provider authentication, and native macOS/
  Windows runners were not exercised here. No current remote CI result is claimed.
- Copying remote selection into the local clipboard is not implemented.
- A provider creation outcome that remains unknown deliberately blocks automatic
  replacement. An absent lookup alone cannot prove that an in-flight creation
  will never finish; operator/provider reconciliation may still be necessary.
- Older pending local delegations with unknown outcomes require recovery instead
  of automatic re-spawn. The follow-up checkpoints recover an unclaimed seed;
  an uncertain external startup returns a recovery error instead of success.
- Early AO report/preview attempts failed while the daemon was unavailable.
  No unrelated desktop instance was started to work around that problem.

## Cleanup

The disposable Postgres container was removed after checking its session label.
All real-browser test containers used automatic removal and were absent after
their runs. No provider login was imported or external sandbox launched. Test
logs remain under `/tmp/ao-review-69-*`; the checkout-private compiler cache is
ignored. Temporary test binaries, tool extraction, scratch home, and temporary
compiler directories are removed after verification.

## Acceptance ledger

All finding gates have automated passing evidence. R7 also has native desktop
selection/paste evidence from publication preparation. Remote-provider and
native-platform gaps above remain explicit; local evidence does not close them.
