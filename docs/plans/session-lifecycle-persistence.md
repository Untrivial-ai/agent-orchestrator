# Plan: Save-on-Close / Restore-on-Open Session Lifecycle

> **Historical status:** This plan is a historical design record. The current
> workspace-aware lifecycle writes `session_worktrees` rows during workspace
> saves, so the single-row shutdown marker described below is not current
> guidance. See [the current session manager](../../backend/internal/session_manager/manager.go)
> and [the architecture notes](../architecture.md).

## Goal

Make the intended lifecycle real and lean: on app close, save every running
session (worker AND orchestrator, no filtering) plus its uncommitted work, then
force-remove the worktrees. On app launch, recreate the worktrees, replay the
saved uncommitted work, and restore all sessions. The daemon already starts on
launch and shuts down + frees its port on quit; this plan fills the missing
save/restore middle.

## Core architectural decisions (settled)

1. **All save/restore logic lives in the daemon**, not the frontend. The daemon
   owns the store, the gitworktree adapter, and the `git` binary. The frontend's
   only new responsibility: call the existing `POST /shutdown` endpoint before
   it kills the daemon, so the save runs gracefully (SIGTERM remains the
   fallback and triggers the same daemon-side save path).
2. **The "last-stop manifest" is the existing SQLite state, not a new file.**
   `ListAllSessions` already records id, kind (worker/orchestrator), harness,
   `is_terminated`, and `Metadata{branch, workspacePath, agentSessionId,
prompt}`. The `session_worktrees` table already has a `preserved_ref` column
   (migration 0009) that nothing currently writes. No manifest.json, no new
   migration, no new format. The manifest is a query.
3. **Uncommitted work is captured as a git commit object pointed to by a ref**
   `refs/ao/preserved/<session-id>`. Reject the user's original
   `refs/{worktree-path}/uncomit/` naming (worktree paths contain `/`, are not
   valid single ref components, and are not stable identity). The session id is
   the stable key the rest of the system already uses.
4. **Untracked files: respect `.gitignore`.** Build the preserve commit through
   a temp index (`GIT_INDEX_FILE=<tmp> git add -A; git write-tree; git
commit-tree`) so tracked + staged + new (non-ignored) files are captured,
   side-effect-free, without mutating the working tree or the stash stack.
   Ignored paths (`node_modules/`, build output, ignored `.env`) are skipped.
   Log a one-line count of skipped ignored paths so it is never silent. (Chosen
   over `git stash create`, which silently drops all untracked files, and over
   `git stash push -u`, which mutates the worktree and the global stash stack.)
5. **Do not weaken the existing dirty-worktree refusal** used by interactive
   `ao session kill` / `ao cleanup`. Add a separate `ForceDestroy` that the
   shutdown path calls only AFTER the work is captured. Adding `--force` to the
   shared remove path would silently destroy work in the interactive flows.

## Global Constraints (binding — reviewers enforce verbatim)

- App state resolves under `~/.ao` only (`AO_DATA_DIR`/`AO_RUN_FILE`
  overridable). Never `~/Library/Application Support`. The manifest is the
  existing SQLite DB at the configured data dir; preserve refs live in each
  project repo's `.git`.
- Preserve ref name is exactly `refs/ao/preserved/<session-id>`.
- Untracked capture respects `.gitignore` (no `-f`, no force-include). Skipped
  ignored paths are logged with a count.
- No kind filtering anywhere in the save or restore loops: orchestrator and
  worker sessions are both saved and both restored.
- Save is strictly capture-then-destroy, per session, with the DB write
  committed before the worktree is removed (crash-safety invariant).
- Never delete a preserve ref except immediately after a successful clean
  apply. A failed apply keeps the ref and leaves conflict markers for the agent.
- No new manifest file, no new migration, no new HTTP endpoint (reuse the
  existing `POST /shutdown`).
- The existing single-session `POST /sessions/{id}/restore` endpoint and the
  interactive dirty-refusal removal path stay behaviorally unchanged.
- No em dashes anywhere (prose, comments, commit messages).

## Key files

- `backend/internal/adapters/workspace/gitworktree/workspace.go` — Destroy,
  Restore, isDirty, findWorktree (re-add logic lives here)
- `backend/internal/adapters/workspace/gitworktree/commands.go` — git arg
  builders (`worktreeRemoveArgs` deliberately omits `--force`)
- `backend/internal/ports/outbound.go` — `Workspace` interface (~line 120)
- `backend/internal/session_manager/manager.go` — Kill (~411-446), Cleanup
  (~556-588), Restore (~451), dirty-refusal translation
- `backend/internal/daemon/daemon.go` — boot/shutdown sequence (startSession
  ~112, `srv.Run(ctx)` ~144)
- `backend/internal/storage/sqlite/store/session_store.go` — `ListAllSessions`
  (~173)
- `backend/internal/storage/sqlite/store/session_worktree_store.go` —
  `preserved_ref` CRUD (`UpsertSessionWorktree`)
- `backend/internal/domain/session.go`, `domain/project.go` — record + worktree
  domain types
- `frontend/src/main.ts` — `before-quit` (~694-700), running.json port read
  (~338)

## Tasks (smallest coherent diff first; each ends with ONE runnable check)

### Task 1 — `ForceDestroy` on the workspace port + gitworktree adapter

Add `ForceDestroy(ctx, info) error` to the `ports.Workspace` interface and the
gitworktree adapter. It runs `git worktree remove --force <path>`, then prune,
then `os.RemoveAll` as a backstop. New arg builder in `commands.go`; leave the
existing safe `Destroy`/`worktreeRemoveArgs` untouched. Add the `ponytail:`
comment that ForceDestroy is only safe after the work is captured.
**Check:** Go test in `gitworktree` that creates a worktree, dirties it, calls
`ForceDestroy`, and asserts the path is gone and the worktree is deregistered.

### Task 2 — `StashUncommitted` + `ApplyPreserved` on the gitworktree adapter

- `StashUncommitted(ctx, info) (ref string, err error)`: build the preserve
  commit via a temp index that respects `.gitignore`
  (`GIT_INDEX_FILE=<tmp> git add -A` → `git write-tree` → `git commit-tree`),
  point `refs/ao/preserved/<id>` at it via `git update-ref`, return the ref name
  (empty if the worktree is clean — nothing to preserve). Log count of ignored
  paths skipped.
- `ApplyPreserved(ctx, info, ref) error`: apply the preserve commit's tree onto
  the worktree (`git stash apply <sha>` style, or `git read-tree`/checkout from
  the commit). On clean success delete the ref (`git update-ref -d`); on
  conflict, keep the ref, leave conflict markers, return a sentinel the caller
  logs.
  **Check:** Go test that round-trips a tracked edit AND a new non-ignored file
  through StashUncommitted → ForceDestroy → re-add → ApplyPreserved and asserts
  both reappear; and that a path matched by `.gitignore` does NOT reappear.

### Task 3 — `SaveAndTeardownAll` + `RestoreAll` on the session manager

- `SaveAndTeardownAll(ctx)`: `ListAllSessions`; for each live (non-terminated)
  session with a non-empty `Metadata.WorkspacePath`: `StashUncommitted` →
  `UpsertSessionWorktree(preserved_ref=...)` (commit) → `MarkTerminated`
  (reuse the LCM path Kill uses) → runtime teardown → `ForceDestroy`. Mirror
  `Kill` but swap refuse-on-dirty for capture-then-force. No kind filter.
- `RestoreAll(ctx)`: `ListAllSessions`; for each terminated session that the
  shutdown save actually processed: ensure worktree via the existing
  `workspace.Restore`, `ApplyPreserved` if a preserve ref is recorded, then
  `manager.Restore(ctx, id)`. Reuse existing `Restore`; do not duplicate its
  argv/resume logic.
  - **The "shutdown-saved" marker is the presence of a `session_worktrees`
    row for that session.** Today nothing else writes `session_worktrees`
    rows, so a row existing == "this session was saved by SaveAndTeardownAll".
    A session the user killed earlier (already terminated when the save ran)
    is skipped by the save and has no row, so RestoreAll skips it too. Do NOT
    gate on `preserved_ref` being non-empty: a clean worktree at shutdown
    writes a row with an empty `preserved_ref` and must still be restored.
    No new column is needed (consistent with Task 6 leaving `state` alone).
    **Check:** Go test with fakes asserting (a) save calls capture-then-force in
    order and writes preserved_ref before ForceDestroy, (b) RestoreAll restores BOTH
    a worker and an orchestrator, (c) a session the user killed before shutdown is
    not resurrected.

### Task 4 — Wire into daemon boot/shutdown (`daemon.go`)

- After `startSession` returns and before `srv.Run(ctx)`: call `RestoreAll`
  (best-effort; log failures; never block boot).
- After `srv.Run(ctx)` returns and before the store closes: call
  `SaveAndTeardownAll` with a fresh bounded context (not the cancelled `ctx`).
- Expose the manager (or a minimal `LifecycleSaver`/`LifecycleRestorer` seam)
  from the wiring up to `Run`.
  **Check:** Manual run documented in report — spawn a session, edit a tracked
  file + add a new file, `POST /shutdown`; assert worktree removed and
  `refs/ao/preserved/<id>` exists; restart daemon; assert worktree re-created and
  both edits reapplied. Plus `go build ./backend/...` green.

### Task 5 — Frontend: call `/shutdown` before kill (`main.ts`)

In `before-quit`: `event.preventDefault()` once, `await fetch(
http://127.0.0.1:<port>/shutdown, {method:'POST'})` with an ~8s bounded timeout
(port from the running.json the app already reads), then `killDaemon` +
`app.exit()`. Keep the `process.on('exit')` SIGTERM fallback intact.
**Check:** `cd frontend && <typecheck cmd>` green; manual: quit the app, daemon
log shows the save ran and exited cleanly (not just SIGTERM-killed).

### Task 6 — Trim the over-built `session_worktrees.state` enum usage

No schema change. Ensure the save/restore code reads/writes only `preserved_ref`
and leaves `state` at its default; add `ponytail:` comments noting the enum is
unused multi-repo scaffolding.
**Check:** `go test ./backend/internal/storage/...` still green.

## Edge cases the lean version must still handle

1. Crash mid-shutdown: per-session capture-then-destroy with DB commit as the
   commit point. Processed sessions recover via ref; unprocessed keep live
   worktrees. No third lossy state.
2. User manually deleted a worktree dir: `workspace.Restore` re-adds from the
   branch; stray non-worktree dir → it refuses, restore loop logs and skips.
3. Base branch moved: worktree re-added on the session's own branch; restores
   to the agent's last state regardless of base.
4. Orchestrator vs workers: no kind filter in either loop.
5. Preserved diff conflicts on apply: keep the ref, leave conflict markers,
   still relaunch the agent. Never delete the ref on failed apply.
6. Incomplete session (no branch/path): skipped on both save and restore.

## Net change

Added: 2 adapter methods (`ForceDestroy`, `StashUncommitted`/`ApplyPreserved`),
2 manager methods (`SaveAndTeardownAll`, `RestoreAll`), 2 daemon call sites,
1 frontend fetch. Reuses `ListAllSessions`, `session_worktrees.preserved_ref`,
`manager.Restore`, the LCM terminate path, and the existing `/shutdown`
endpoint. No new file, migration, format, or endpoint.

## Build & verify commands (from repo root; see AGENTS.md for the full list)

- `npm run lint` — backend `go test ./...` + golangci-lint v2.12.2
- `cd backend && go build ./...` / `go test ./...` / `go test -race ./...` /
  `go vet ./...`
- `npm run frontend:typecheck` — frontend TypeScript check (Task 5)
- Do NOT hand-edit `backend/internal/storage/sqlite/gen/*`. This plan adds no
  new queries/migrations, so `npm run sqlc` should not be needed; if a task
  finds it does need a new query, change `queries/*` and run `npm run sqlc`.
- This plan adds NO new HTTP routes, so the OpenAPI/`npm run api` flow and the
  `internal/httpd` spec-drift tests should stay green untouched. If a reviewer
  sees spec drift, a task wrongly added a route.

## Starting point for the implementing session

- Baseline: this plan and the cleanup are committed on `main` (the plan file
  lives at `docs/plans/session-lifecycle-persistence.md`). Branch off `main`
  as `feat/session-lifecycle-persistence`.
- The file:line references above are approximate (prefixed `~`). Verify each
  with codegraph or grep before editing; the daemon is loopback-only and the
  store is sqlc-generated, so confirm signatures rather than assuming.
- Use the `superpowers:subagent-driven-development` skill to execute: fresh
  implementer subagent per task, task review (spec + quality) per task, then a
  final whole-branch review. Subagents follow TDD.

## Execution order

Tasks are sequential where coupled: Task 2 shares the gitworktree adapter with
Task 1 (do 1 then 2, same package); Task 3 depends on 1 + 2; Task 4 depends on 3. Task 5 (frontend) and Task 6 (storage cleanup) are independent and can run
anytime. Suggested order: 1 → 2 → 3 → 4, then 5 and 6.
