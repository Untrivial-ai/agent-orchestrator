# ADR 0004: Make worker runtime lifecycle outcomes generation-specific

- **Status:** Accepted
- **Date:** 2026-08-11
- **Related:** [ADR 0003](0003-worker-process-containment.md),
  [PR #3550](https://github.com/Untrivial-ai/agent-orchestrator/pull/3550),
  [research record](../compatibility/20260811-v1.0-tmux-systemd-worker-lifecycle-research.md)

## Context

ADR 0003 adds an opt-in Linux systemd scope because a process that calls
`setsid` escapes tmux's POSIX-session reaper. Review of PR #3550 then exposed a
different boundary: AO's runtime port returns only a handle on success and an
error on failure, while Session Manager treats a Create error as permission to
roll back the worker's worktree.

That assumption predates PR #3550. It is unsafe when a tmux client is canceled
after the server may have accepted `new-session`, when post-create cleanup
fails, or when a concurrent generation owns the reusable tmux session name.
Adding a systemd scope makes an incorrect assumption affect both a tmux session
and a cgroup-owned process tree.

The current PR also configures containment on a shared runtime adapter. Without
a per-launch boundary, reviewer and standalone shell-terminal calls receive
worker containment behavior even though they are outside ADR 0003's contract.

## Decision

Slice C will make the internal runtime lifecycle generation-specific while
remaining opt-in and worker-controller-only.

### One instance identity

The existing `RuntimeLaunchID` is the only instance identity. The internal
runtime config and handle will carry it explicitly. AO will not generate a
second creation token.

For a contained tmux worker, the same launch ID will identify:

- the supervised agent generation already recorded in session metadata;
- the tmux session user option written with session creation; and
- the transient systemd scope, through a deterministic bounded unit name
  derived from session ID plus launch ID.

The tmux session name remains a stable terminal handle. It is not sufficient
proof of launch ownership by itself.

### Three semantic Create outcomes

The internal runtime contract must distinguish these outcomes even if the Go
representation uses a result value plus a typed error:

| Outcome | Meaning | Session Manager action |
| --- | --- | --- |
| ready | this exact launch is live and verified | persist handle and launch ID, then mark spawned |
| rollback safe | no competing runtime needs the workspace and this launch is absent or confirmed released | use the existing spawn rollback path |
| preserve | this launch may remain, another generation was observed, or ownership/cleanup could not be proven | keep the session row and worktree; do not mark terminated or report teardown complete |

Untyped ambiguity is not allowed at the port boundary. An adapter that cannot
prove rollback safety must return the preserve outcome with enough exact
reference information for diagnostics and later cleanup.

### Exact-generation Destroy

Destroy targets a handle containing both stable terminal ID and launch ID.
For contained tmux workers it may stop only the systemd unit derived from that
exact pair. It may kill the tmux session only after the session's launch marker
matches the requested generation.

A missing exact scope is idempotent success. A tmux session marked for another
generation must not be killed; Destroy returns a preservation error even after
the requested scope is released, because the foreign generation may still use
the same workspace and callers must not continue into workspace deletion.
Probe failure or a scope that cannot be proven released also returns an error;
nil means the exact scope is released and no foreign terminal owner blocks the
caller's lifecycle transition.

### Stop-before-start Restart

Restart accepts downtime. It must first prove the old exact generation released
before it respawns the pane for a new `RuntimeLaunchID`. If old-generation
release is unconfirmed, no replacement starts. If replacement creation is
unconfirmed, the same preserve outcome applies.

This avoids overlapping old and replacement scopes and keeps terminal-handle
reuse separate from process-generation identity.

If replacement readiness fails, AO first confirms the replacement scope is
released and then restores the prior tmux marker with one server-side
compare-and-set command. It never rewrites the marker after release failure and
never overwrites a marker that changed to a concurrent generation.

### Caller preservation rule

Session Manager may remove a worktree or mark a worker terminated only after
runtime rollback safety or exact teardown is confirmed. On a preserve outcome
it records the available runtime handle, launch ID, and workspace facts using
existing metadata and leaves the row non-terminated.

C does not add a persisted cleanup-pending state. The existing identity and
workspace facts remain durable, but they do not record cleanup intent, retry
ownership, or a restart-time reconciliation obligation. D will define those
durable cleanup semantics.

### Caller boundary

Only primary managed TUI agent controllers supply a `RuntimeLaunchID` and are
eligible for systemd containment. This includes AO worker and orchestrator
sessions that use the TUI runtime. It excludes:

- independent reviewer runtimes;
- standalone shell terminals; and
- Chat sessions, which have no primary terminal runtime.

Those callers retain their current runtime behavior. The internal port may gain
fields and outcome types, but no CLI, HTTP API, OpenAPI schema, frontend type,
environment-variable syntax, script interface, or database migration changes
in C.

## Alternatives considered

### Keep the source assumption and fix only tmux cleanup

Rejected. No adapter can guarantee cleanup under every cancellation, probe, or
external-manager failure. Treating all Create errors as absence can delete a
worktree still used by a live or concurrent process.

### Preflight `has-session` before creation

Rejected. It is observational and has a time-of-check/time-of-use window. A
concurrent creator can win after the probe and before cleanup.

### Add a separate random creation token

Rejected. It creates two identities for one launch and makes reconciliation,
logs, tests, and future persistence translate between them. `RuntimeLaunchID`
already provides the required generation identity.

### Kill anything with the same tmux session name

Rejected. The session name is reusable across restarts. Name-only cleanup can
delete a newer or concurrent generation.

### Implement durable cleanup and default rollout in the same PR

Rejected for C. Persistence, daemon-crash recovery, configuration drift,
default enablement, resource limits, and production rollout form a separate
controller and migration problem. They remain D so #3550 can converge on one
bounded compatibility correction.

### Rebuild all of #3550 from upstream `main`

Rejected. The containment backend, cross-platform configuration fix, native CI,
and Linux canary already have exact-head evidence. C should start from a clean
checkout of the published PR head and replace only the uncommitted round-four
proposal that conflicts with this ADR. Reimplementation from bare upstream
`main` would discard validated work without reducing the caller-contract risk.

## Consequences

- C grows from a tmux-only patch into a narrow internal port and Session Manager
  compatibility change. This is intentional because the unsafe assumption lives
  at that boundary.
- Worker cleanup becomes conservative: an unresolved runtime may retain a row
  and worktree instead of risking collateral deletion.
- Restart can have visible downtime and can fail before replacement creation.
- Reviewer and standalone shell-terminal behavior stop being accidental
  consumers of the worker containment option.
- Windows ConPTY must compile and pass native tests for the internal port change,
  but gains no systemd behavior.
- C still cannot recover cleanup intent after a daemon crash. D owns that gap.

## Reopen conditions

Revisit this decision if AO replaces tmux/systemd, makes reviewer or shell
terminals first-class supervised generations, adopts a durable runtime-resource
record, or proves a stronger native create/delete transaction that removes the
need for conservative preservation.
