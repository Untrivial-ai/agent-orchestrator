# PR #3550 latest-main resynchronization and validation

Status: R1-R6 complete locally; delivery pending

Date: 2026-08-20

## Goal

Move upstream PR #3550 from exact head
`18366d6d99ffa6644bd70d4262b3c67bc08d6728` onto current upstream `main`
`e5c4c3e8092d1fd3a9374a6c1f9e07410821dc6a`, resolve the lifecycle conflicts
without weakening either side, and produce fresh exact-head local and Linux host
evidence before any push or renewed review request.

This plan continues the accepted exact-generation design in
[the C lifecycle plan](2026-08-11-pr-3550-c-exact-generation-lifecycle.md).
ADR 0004 remains the architecture authority.

## Live baseline

- Upstream PR: `Untrivial-ai/agent-orchestrator#3550`.
- PR head and fork branch head: `18366d6d99ffa6644bd70d4262b3c67bc08d6728`.
- Current upstream `main`: `e5c4c3e8092d1fd3a9374a6c1f9e07410821dc6a`.
- The PR is open, `mergeable=false`, `mergeable_state=dirty`, and requires
  review.
- Upstream `main` has advanced 175 commits from the PR merge base
  `419fb3ba7925607469ac1627a4ad8d62281ff35e`.
- No review threads or submitted reviews are currently recorded on the
  upstream PR.
- The old fork checks belong to the previous base/head and are not acceptance
  evidence for the resynchronized head.

## Ownership and evidence boundary

- The sole writer is local branch `codex/pr3550-main-resync-20260820` in the
  primary repository worktree.
- `/home/fqzhang/tmp/ao-pr3550-c-exact-generation` remains a clean read-only
  snapshot of `18366d6d`.
- `/home/fqzhang/tmp/ao-pr3550-cross-platform` remains a read-only evidence
  source at `59a317a2` with nine uncommitted paths.
- A separate read-only audit found no unique production code or document that
  must be carried from the dirty worktree. Its random creation-token proposal
  is superseded by the committed `RuntimeLaunchID` design and would reintroduce
  a show-then-kill time-of-check/time-of-use gap.
- Production AO state, service configuration, database, and deployment are out
  of scope.

Stop if the remote PR head changes, another writer appears, or the merge needs
an API, database, frontend, default-enablement, or durable cleanup expansion.

## Merge invariants

1. Current `main` owns new product, platform, SCM, runtime, and session-manager
   behavior added since the old base.
2. PR #3550 continues to own exact `RuntimeLaunchID` propagation, generation
   fencing, preserve-required lifecycle outcomes, and release-before-workspace
   cleanup.
3. No conflict is resolved by taking an entire `ours` or `theirs` file.
4. Linux systemd containment remains explicit and opt-in. macOS tmux, Windows
   ConPTY, reviewer, shell-terminal, and Chat behavior do not opt in.
5. Unknown or foreign runtime ownership preserves the session and workspace.
6. A workspace transition or termination claim follows only authoritative
   release of the requested exact generation.

## Expected conflict surface

The pre-merge three-way audit identified these 13 textual conflicts:

- `backend/internal/adapters/runtime/conpty/runtime.go`
- `backend/internal/adapters/runtime/conpty/runtime_test.go`
- `backend/internal/adapters/runtime/runtimeselect/runtimeselect.go`
- `backend/internal/config/config.go`
- `backend/internal/config/config_test.go`
- `backend/internal/daemon/daemon.go`
- `backend/internal/ports/outbound.go`
- `backend/internal/review/launcher_test.go`
- `backend/internal/session_manager/agent_switching.go`
- `backend/internal/session_manager/chat_spawn.go`
- `backend/internal/session_manager/manager.go`
- `backend/internal/session_manager/manager_test.go`
- `docs/README.md`

Resolution starts from current `main` semantics and reapplies the PR contract
at the narrowest owning boundary. In particular, preserve upstream ConPTY
registry/viewport work, multi-SCM and mobile/autoreview wiring, authoritative
default/base-ref handling, agent-switch phase/model/draft state, in-place
restart/reconciliation, and attachment persistence.

The actual merge auto-resolved eight of these paths and left five index
conflicts: ConPTY runtime, runtime selection, config, daemon wiring, and session
manager tests. All five were resolved by composing the two contracts rather
than choosing a whole side.

## Execution graph

### R1: freeze and audit the predecessor worktrees

Predecessor: none.

- Compare the dirty predecessor with `18366d6d` path by path.
- Classify each path as identical, superseded, or uniquely required.
- Keep both predecessor worktrees read-only during the resynchronization.

Acceptance: no unique content is omitted and exactly one writer exists.

### R2: pin current remote authority

Predecessor: R1.

- Read upstream PR head, fork head, upstream `main`, merge state, reviews,
  threads, and checks.
- Fetch `upstream/main` by explicit ref and verify the fetched object equals
  the GitHub readback.

Acceptance: the local merge inputs are the exact live SHAs recorded above.

### R3: merge current main and resolve conflicts

Predecessor: R2.

- Merge `e5c4c3e8` into the sole-writer branch without rewriting the published
  PR history.
- Resolve each conflict by integrating upstream behavior with the six merge
  invariants.
- Inspect the full resolved diff and run `git diff --check` before tests.

Acceptance: no conflict markers remain; the tree builds; neither upstream
behavior nor exact-generation lifecycle guarantees are dropped.

### R4: focused compatibility verification

Predecessor: R3.

Run from `backend/`:

```bash
go test -count=1 ./internal/ports ./internal/adapters/runtime/runtimeselect
go test -count=1 ./internal/adapters/runtime/tmux ./internal/adapters/runtime/conpty
go test -count=1 ./internal/config ./internal/daemon
go test -count=1 ./internal/session_manager ./internal/service/shellterm ./internal/review
go test -race -count=1 ./internal/adapters/runtime/tmux ./internal/session_manager
```

Acceptance: focused packages pass and tests retain current-main behavior plus
the exact-generation error, cleanup, and platform matrix.

### R5: complete repository gates

Predecessor: R4.

Run the current repository-owned gates applicable to the merged runtime tree:

```bash
cd backend && go build -p 2 ./...
cd backend && go vet -p 2 ./...
cd backend && go test -race -p 2 ./...
npm run lint
npm run frontend:typecheck
git diff --check
```

Also inspect current CI definitions after the merge and run any newly required
generated-artifact or workflow gate. No API or sqlc regeneration is expected;
such a need is a stop condition until its scope is reviewed.

Acceptance: every required command passes on one exact local head, with any
unavailable gate and residual risk named explicitly.

### R6: isolated Linux containment canary

Predecessor: R5.

On a compatible isolated user-systemd host, run:

```bash
cd backend
AO_TEST_SYSTEMD_CONTAINMENT=1 go test ./internal/adapters/runtime/tmux \
  -run TestRuntimeIntegrationSystemdContainment -count=1 -v
```

Read back the exact disposable tmux server and systemd units after the test.
The canary must prove `setsid` and TERM-ignoring descendant cleanup, old-before-
new Restart release, exact marker/scope identity, foreign and outside-scope
survival, and no reviewer/shell worker scope.

Acceptance: the Go test passes, every disposable scope is inactive/dead or not
found, no contained process remains, and negative controls survive.

## Execution record

R1-R6 completed on 2026-08-20 in local branch
`codex/pr3550-main-resync-20260820`. The merge remains intentionally
uncommitted and unpushed.

- R1: the nine-path dirty predecessor was audited path by path. All content is
  identical to or superseded by `18366d6d`; no WIP code was migrated.
- R2: GitHub and explicit ref fetch agreed on PR head `18366d6d` and upstream
  main `e5c4c3e8`.
- R3: all five actual conflicts were resolved and marked resolved in the Git
  index. ConPTY retains the run-file-scoped registry and protocol negotiation
  while preferring `RuntimeConfig.RuntimeLaunchID`; runtime selection passes
  both run-file scope and Unix containment; config and daemon retain both
  GitLab and containment wiring. One upstream EOF whitespace diagnostic in
  `frontend/src/landing/src/lib/analytics/launch/utm.ts` was corrected.
- R4: all focused packages passed, including race runs for tmux and
  `session_manager`.
- R5: `go build -p 2 ./...`, `go vet -p 2 ./...`,
  `go test -race -p 2 ./...`, canonical lint, frontend typecheck, frontend
  E2E typecheck, shared Cloud/product-UI checks, and both staged and unstaged
  diff checks passed. A full Windows amd64, CGO-disabled backend cross-build
  also passed for the ConPTY/selector conflict surface. The first
  default-parallel `npm run lint` attempt hit
  the host process limit (`resource temporarily unavailable`); the identical
  test and lint rules passed with `GOFLAGS=-p=2` and
  `GOLANGCI_LINT_CONCURRENCY=2`. Its sole timing failure passed ten
  consecutive focused repetitions before the canonical rerun.
- R6: the opt-in user-systemd canary passed in 11.98 seconds. Pre/post readback
  found no `ao-session-*` units and no old/new canary processes.

Validation dependencies were installed from the committed lockfiles in
`frontend`, `packages/cloud-client`, and `packages/product-ui`. These
untracked build dependencies are ignored local state. npm reported advisories
for the frontend and Cloud dependency graphs; no lockfile was changed and
dependency remediation is outside this PR's lifecycle scope.

## Deferred delivery

Commit, push, hosted CI refresh, PR description/comment updates, and renewed
current-head review are deliberately after R1-R6. They require the final exact
head and fresh evidence; this execution does not treat the previous fork checks
as current.

## Rollback and preservation

Before push, rollback is returning the sole-writer branch to `18366d6d`; the
clean predecessor snapshot and dirty evidence worktree remain available. Do not
delete either evidence worktree during R1-R6. A failed merge or validation run
must leave the conflict tree and logs observable or abort back to the exact
published head without changing the remote PR.
