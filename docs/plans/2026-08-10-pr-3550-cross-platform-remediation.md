# PR #3550 cross-platform remediation and validation

Status: active; Slices 1-6 complete and refreshed onto upstream `main`
`cfbefa4c`; Slice 7 review remediation and exact-head revalidation in progress

Date: 2026-08-10

## Goal

Address the confirmed non-Linux test regression in upstream PR #3550 without
weakening its Linux-only `systemd` runtime contract. Make the configuration
boundary deterministically testable for Linux, macOS, and Windows; add the
missing native-platform CI coverage; then revalidate the complete containment
change on one exact current head.

## Observed inputs

The following GitHub objects were read authoritatively on 2026-08-10. Re-read
them before any rebase, push, review, or response because all are movable.

| Input | Observed value | Role |
| --- | --- | --- |
| Upstream PR | [Untrivial-ai/agent-orchestrator#3550](https://github.com/Untrivial-ai/agent-orchestrator/pull/3550) | existing delivery surface |
| PR head | `bd7baa54e829c3426cdeefe345b8252d1c8ed746` | pre-remediation rollback and lease point |
| PR base snapshot | `5f3e6bcd5a47bb7312f80cfc3966464a8f948cda` | original comparison base |
| Current upstream `main` | `cfbefa4cdbd5e8c8e020177e53d105f10e5f44ee` | latest refreshed rebase target after #3817 |
| Current fork `main` | `17748630b701367bc70edfab6155c272eb10595b` | fork validation reference only |
| PR state | open, ready, mergeable, review required | blocks delivery until exact-head validation and review converge |
| GitHub checks | none reported on the upstream PR head | requires exact-head fork validation |
| Formal reviews / review threads | `0 / 0` | not equivalent to a clean independent review |
| Actionable top-level feedback | [non-Linux `TestLoadOverrides` failure](https://github.com/Untrivial-ai/agent-orchestrator/pull/3550#issuecomment-5226399480) | remediation contract |

The report is valid: `TestLoadOverrides` sets
`AO_PROCESS_CONTAINMENT=systemd` on every host, while `Load()` correctly rejects
that value when `runtime.GOOS != "linux"`. The test therefore fails on macOS
and Windows before it reaches its assertions. The production validation is not
the defect and must remain strict.

## Ownership and execution boundary

Use one writer and one isolated worktree for the existing PR branch. Slice 1
found no AO owner or existing target-branch worktree and established a new
isolated worktree at the exact PR head as the sole writer. Older containment
worktree state is unrelated, preserved, and must not be reused or cleaned as
part of this task.

Before every mutation, re-read the PR head and stop if it no longer equals the
writer's expected head. Do not patch, stage, commit, push, or resolve feedback
from another worktree. A rejected lease, new owner, new review, or changed
remote head is a stop-and-reconcile event rather than permission to overwrite.

## Non-goals

- Do not make `systemd` containment available on macOS or Windows.
- Do not replace the explicit non-Linux configuration error with silent
  fallback.
- Do not redesign containment, tmux lifecycle, daemon wiring, or cleanup.
- Do not absorb unrelated upstream-main changes while resolving the rebase.
- Do not treat compilation, React Doctor, or green CI as the required
  independent current-head code review.
- Do not install, restart, reconfigure, or otherwise modify the production AO
  binary, service, database, Dashboard, or environment.

## Affected contracts

| Surface | Owner | Required outcome |
| --- | --- | --- |
| Configuration parsing | `backend/internal/config/config.go` | unset remains portable; `systemd` is accepted only for Linux; invalid values stay explicit |
| Configuration tests | `backend/internal/config/config_test.go` | generic override coverage is host-neutral; OS policy is covered deterministically |
| Native CI | `.github/workflows/cli-e2e.yml` | the config package runs on Ubuntu, macOS, and Windows |
| Runtime selection and tmux containment | runtime and tmux packages | no behavioral change beyond conflict reconciliation |
| Linux containment canary | tagged tmux integration test | `setsid`, ignored `TERM`, restart, destroy, and negative-control contracts still pass |
| PR delivery | GitHub exact head | required checks, separate review, and actionable feedback all refer to the same commit |

## Execution graph

### Slice 1: authoritative readback and single-writer confirmation

1. Read AO health before owner lookup.
2. Read the registered project catalog, complete session inventory, PR
   associations, and target-branch worktree associations.
3. Read GitHub PR metadata, fork branch head, upstream `main`, comments,
   reviews, and review threads.
4. Confirm that the PR branch is not checked out by another local worktree and
   that no matching host process is an active writer.
5. Establish one isolated worktree at the exact PR head; preserve all unrelated
   and dirty worktrees.

Acceptance:

- PR and fork branch both resolve to
  `bd7baa54e829c3426cdeefe345b8252d1c8ed746`;
- no AO session or PR association owns #3550;
- no pre-existing worktree or active process owns the target branch;
- one isolated worktree is designated as the sole writer; and
- no source, remote, PR, or production state is changed by the readback.

Status: complete on 2026-08-10.

### Slice 2: refresh and rebase the PR branch

1. Re-read the upstream PR head, fork branch head, and upstream `main`.
2. Fetch those exact objects and require the remote head still to equal the
   recorded lease point.
3. Rebase the PR branch onto the freshly read upstream `main`.
4. Resolve only conflicts required to preserve the existing containment
   contract and the current upstream implementation.
5. Inspect the complete rebased range before changing behavior.

Acceptance:

- the rebased branch contains the latest accepted upstream `main` as an
  ancestor;
- the containment diff remains scoped to the existing PR contract;
- no unrelated fork-only change is introduced; and
- the old exact head remains recoverable as the recorded rollback point.

Status: complete on 2026-08-10. The first refresh rebased the branch onto
`c17b6c3f5a76dbbff38a05589ff6f9ac6c22ea54`. Range comparison against the
original PR commit found only the upstream-required `errors` import in the tmux
integration test and the containment ADR renumber from `0002` to `0003`, because
upstream now owns ADR 0002.

Later the same day, upstream `main` advanced by one commit to
`00330ae9b06a40ad21273511915fdeaf56438bf9` (`#3808`). That commit changes only
the landing `DownloadButton` label and its test, with zero file overlap against
this PR. The four local commits rebased without conflicts, and `git range-diff`
reported all four patches equivalent. The refreshed containment commit was
`5591250dc79506dd884de845c6f1ce4541692670`.

On 2026-08-11, upstream `main` advanced by five more commits to
`cfbefa4cdbd5e8c8e020177e53d105f10e5f44ee` (`#3809`, `#3811`, `#3816`,
`#3648`, and `#3817`). Their changed files have zero overlap with this PR.
The same four local commits rebased without conflicts, and `git range-diff`
again reported every patch equivalent. The refreshed containment commit is
`6a436d9499b05b5552841af50c96d8b74da7bd32`; the previously published exact
head `bd047301bbd5933f5b3df9343d907a1b49e21486` remains the lease point until
the Slice 7 push.

### Slice 3: make configuration policy deterministic

Extract the environment-value and target-OS decision into a small unexported
pure helper, for example `parseProcessContainment(raw, goos)`. `Load()` passes
`runtime.GOOS`; tests pass explicit OS values.

Keep these invariants:

- empty input selects no containment on every OS;
- `systemd` succeeds only when `goos == "linux"`;
- `systemd` returns the existing Linux-only class of error for Darwin and
  Windows;
- unknown non-empty values remain invalid on every OS; and
- `Load()` remains the environment wiring boundary.

Remove `AO_PROCESS_CONTAINMENT=systemd` from the generic
`TestLoadOverrides`. Add focused table tests for empty, Linux, Darwin, Windows,
case/whitespace normalization, and invalid values. Keep one Linux-host wiring
test if needed to prove `Load()` passes the environment value through the pure
policy boundary.

Acceptance:

- tests describe policy independently of the runner OS;
- production behavior is unchanged; and
- the collaborator's reproduced macOS/Windows failure is no longer possible.

Status: complete on 2026-08-10. `parseProcessContainment(raw, goos)` now owns
the portable policy, while `Load()` remains the environment wiring boundary.
The table test covers empty values, Linux, Darwin, Windows, normalization, and
invalid values; the generic override test no longer injects a Linux-only
setting. A Linux-only `Load()` wiring test preserves the real environment path.

### Slice 4: add native-platform config coverage

Reuse the existing Ubuntu/macOS/Windows native matrix in
`.github/workflows/cli-e2e.yml`. Add a focused step:

```yaml
- name: Config unit tests
  run: go test -count=1 ./internal/config
```

Do not create a second platform matrix unless the existing job cannot provide
the required signal. The native runners, not cross-compilation from Linux, are
the authority for host-specific execution.

Acceptance:

- the config package executes on all three GitHub-hosted OS families;
- the workflow still runs for `backend/**` and workflow changes; and
- a future unconditional Linux-only override fails the macOS and Windows jobs.

Status: source complete on 2026-08-10. The existing native matrix now runs
`go test -count=1 ./internal/config` before CLI E2E on Ubuntu, macOS, and
Windows. Execution on all three hosted runners remains an exact-head Slice 7
gate and is not inferred from the local Linux result.

### Slice 5: local verification

Run focused checks first, then the backend gate represented by the PR:

```bash
cd backend
gofmt -w internal/config/config.go internal/config/config_test.go
go test -count=1 ./internal/config
go test ./internal/adapters/runtime/runtimeselect ./internal/adapters/runtime/tmux
go build -p 2 ./...
go vet -p 2 ./...
go test -race -p 2 ./...
```

Also run `git diff --check` and inspect the full upstream-main-to-head diff.
If repository-owned commands have changed on the rebased head, use the current
commands and record the substitution.

Acceptance:

- focused configuration and containment tests pass;
- build, vet, and race tests pass on the rebased source; and
- no formatting, whitespace, generated-artifact, or unrelated-diff drift is
  present.

Status: complete locally on 2026-08-10. Final-source evidence:

- `go test -count=1 ./internal/config` passed;
- the containment packages passed in the host process view, and the bounded
  `WaitActive` case passed 50 consecutive runs after normalizing the total
  timeout error path;
- `go build -buildvcs=false -p 2 ./...` passed (`-buildvcs=false` is required
  only because this is a linked worktree; the unmodified command stopped at Go
  VCS stamping before compilation);
- `go vet -p 2 ./...` passed;
- `go test -race -p 2 ./...` passed with a CI-like `PATH` that excludes the
  host's older `codex-cli 0.144.1`. With the host CLI visible, the only failure
  was the repository's installed-provider conformance check because generated
  protocol data targets `codex-cli 0.146.0`; no source in that package differs
  from upstream `main`; and
- `git diff --check` passed and the full upstream-main range contained only the
  containment contract, its ADR/plan, the config remediation, and native CI
  coverage.

The validation also exposed a 10 ms error-classification race in the PR's new
`WaitActive` test (2 failures in 10 runs). The implementation now maps expiry
of the total wait context to the existing `did not become active` contract;
50 consecutive runs and the full race gate passed. After the second rebase,
the remediation commit was
`f01bc4b68d8808e0cc208ab48cd39d13bbd9d9fe`; after the 2026-08-11 refresh it
is `53f13a60c5133bf16d1860ff02900409f7fd65ad`.

The refreshed source repeated the config test, 50-run timeout test, uncached
host containment packages, linked-worktree build, vet, and full race command;
all passed. The full race command retained the documented CI-like `PATH` that
excludes the host's older Codex provider.

### Slice 6: Linux containment canary

Run the opt-in integration canary on a compatible Linux user-systemd host:

```bash
cd backend
AO_TEST_SYSTEMD_CONTAINMENT=1 go test ./internal/adapters/runtime/tmux \
  -run TestRuntimeIntegrationSystemdContainment -count=1 -v
```

Require evidence for:

- a descendant that calls `setsid` remains inside the worker scope;
- a descendant that ignores `TERM` is removed through the bounded kill path;
- restart creates the expected new handle and preserves command/input behavior;
- destroy leaves the worker scope inactive, dead, or not found;
- an outside-scope negative-control process survives worker teardown; and
- the canary cleans up only its disposable resources.

The canary is source validation, not production deployment.

Status: complete on 2026-08-10. The refreshed host run passed in 10.89 seconds. Its
assertions proved the `setsid` child had a different SID while remaining in the
expected scope, the ignored-TERM child was removed during Restart/Destroy, the
restarted handle and terminal input worked, and the outside-scope negative
control survived worker teardown. The isolated test also accepts its dedicated
tmux server exiting after the final session as `runtime unavailable`, while
continuing to require process disappearance and authoritative scope release.

Post-run readback reported the dedicated scope as
`not-found/inactive/dead`, with no listed unit. The installed AO binary hash
remained
`2b9b4db43ea9d8437bc2aeabb150535dae35cbf443120e7e8290d7751830ab68`.
The pre-existing stale run-file state was unchanged; no AO binary, service,
database, Dashboard, environment, or production process was updated or
restarted.

### Slice 7: exact-head GitHub validation and review

Push the existing PR branch only with a lease tied to the last authoritative
remote head. If the upstream PR does not run contributor checks, create one
temporary draft PR in the fork whose head is exactly the upstream PR head and
whose base is pinned to the same upstream-main object.

Require the relevant Actions to pass on that exact head, including the native
config matrix, Go build/test/race, lint, API drift, CLI E2E, container smoke,
and secret scanning when triggered.

Then perform a code review that is explicitly separate from CI and automated
diagnostics. Review the exact current head across:

- configuration semantics and error compatibility;
- runtime selection and daemon wiring;
- systemd command construction and scope identity;
- create, restart, destroy, and failure cleanup;
- test determinism and platform assumptions; and
- workflow path filters and matrix behavior.

Acceptance:

- all required checks refer to the exact current PR head;
- the independent review records its range, head, and conclusion;
- unresolved actionable review threads equal zero; and
- the collaborator's top-level blocker has a concise evidence-backed response
  and a re-review request.

Review round 1 on 2026-08-11 inspected exact range
`cfbefa4cdbd5e8c8e020177e53d105f10e5f44ee..b82e0918b813ac861776d1e01fc88995e93810d7`
separately from CI. Fork PR #28 passed the Go, lint, API drift, secret scan,
container, and Ubuntu/macOS/Windows native jobs, including the focused config
step on all three platforms. The code review nevertheless found one actionable
failure-cleanup defect: after `Restart` had respawned a scoped pane, a readiness
or liveness error returned no handle and did not release the replacement scope,
so a worker could remain active after the caller observed failure.

The correction releases that exact replacement scope on every post-respawn
error, uses a cancellation-detached bounded cleanup context, and joins any
cleanup failure with the original error. Hermetic tests cover uncertain
respawn outcome, readiness failure, liveness-probe failure, and cleanup failure.
Because this changes the head, all hosted checks and the exact-current-head
review must run again before the collaborator response or re-review request.

Review round 2 inspected exact range
`cfbefa4cdbd5e8c8e020177e53d105f10e5f44ee..9ab0bc5e70a0bd6f91b6e9e806beff409d34343d`
separately from CI. All hosted jobs for that head passed, including the three
native platform config steps. The independent review found one further
actionable failure-cleanup defect: `Create` discarded `Destroy` errors after a
successful `new-session`, so a readiness failure followed by a failed scope
release could return no handle while leaving an unowned worker active.

The correction routes every failure after successful `new-session` through a
single `cleanupFailedCreate` path. Cleanup detaches from caller cancellation,
retains the existing bounded runtime and containment timeouts, and joins a
cleanup failure with the original setup/readiness error. Hermetic tests cover
the readiness-plus-release failure and prove cleanup still runs when the caller
context is canceled. This changes the head again, so hosted checks and an
independent exact-current-head review must restart from the new commit.

Review round 3 inspected exact range
`cfbefa4cdbd5e8c8e020177e53d105f10e5f44ee..5ecfcc06670b00e4f12785ad74d06fba63327d2e`
separately from CI. All exact-head hosted checks passed, and the review found
one remaining actionable uncertain-outcome defect: cancellation or timeout of
the `tmux new-session` command could occur after tmux accepted the request, but
`Create` returned no handle and did not reconcile the possible session/scope.

The correction first proves that a scoped Create has no pre-existing exact tmux
session. A canceled or timed-out `new-session` then enters the same detached,
bounded cleanup path; an explicit concurrent `duplicate session` response is
never destroyed. Fault-injection tests cover uncertain accepted creation,
concurrent duplicate protection, and pre-existing session protection. This
changes the head again, so all hosted checks and the independent review must
restart from the next exact commit.

The first post-correction canary stopped safely before worker creation because
the dedicated tmux socket's normal first-use `error connecting ... (No such
file or directory)` response was classified too conservatively. The policy now
accepts only that definitely-missing-socket form while continuing to reject
inconclusive connectivity failures. Focused tests and the full Linux systemd
canary then passed; the canary completed in 10.89 seconds with its existing
setsid, ignored-TERM, restart, destroy, and negative-control assertions.

The review also retained the ADR's documented out-of-scope residual: changing
the daemon-wide containment setting across a restart is not a durable
per-session migration, and crash-time scope reconciliation/persisted cleanup
facts remain follow-up work. This slice does not widen into that lifecycle or
storage redesign.

### Slice 8: handoff and cleanup

Do not equate a clean contributor branch with upstream merge authority. Leave
the upstream PR ready for maintainer review unless the platform and explicit
authority permit a later merge action.

After the fork validation PR is no longer needed, close it and remove only its
temporary validation branch. Preserve the upstream contribution branch and
the recorded pre-remediation head until maintainer disposition is final.

## Stop conditions and rollback

Stop without overwriting state if:

- the remote PR head changes unexpectedly;
- another AO or non-AO writer appears;
- the lease-protected push is rejected;
- the rebase requires behavior outside the existing containment contract;
- macOS or Windows still fails the config package;
- Linux containment or its negative control fails;
- current-head review finds an unresolved compatibility or lifecycle defect;
  or
- any step would touch production state.

The pre-remediation rollback point is
`bd7baa54e829c3426cdeefe345b8252d1c8ed746`. Reconciliation must preserve that
object and use lease-protected remote updates; never recover by deleting dirty
worktrees or force-updating an unexpected remote head.

## Final evidence

Completion requires one exact head with all of:

1. deterministic config policy tests;
2. Ubuntu, macOS, and Windows config-package runs;
3. focused containment tests and full backend gates;
4. the Linux systemd containment canary and surviving negative control;
5. an independent exact-current-head code review;
6. zero unresolved actionable review threads;
7. an evidence-backed response to the collaborator; and
8. an explicit statement that production AO remained unchanged.
