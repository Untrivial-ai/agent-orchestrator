# Archived Workspace Cleanup and Retention

**Status:** Proposed
**Date:** 2026-09-16

## Summary

AO already tears down eligible workspaces for terminated sessions, but the current cleanup is a lifecycle operation rather than a complete disk-space maintenance system. It can leave substantial storage behind when a worktree is dirty, locked, in use, missing from durable session metadata, or otherwise cannot be reclaimed as a whole.

This change adds an explicit **Remove** action to archived session cards and an opt-in automatic cleanup policy. Clean archived worktrees are removed through the daemon's existing workspace adapter. Dirty worktrees keep all source changes, but AO may remove validated, regenerable `node_modules` directories after the retention period. AO reports orphaned storage instead of silently deleting unverifiable source trees.

Before removing a complete worktree, AO creates or verifies a durable Git recovery ref using the existing `session_worktrees.preserved_ref` boundary. The archived session record, conversation history, and recovery ref remain available. Restoring an archived session recreates its workspace through the existing restore path.

## Terminology

- **Archived session:** a terminated session displayed in the Archive section.
- **Workspace/worktree:** the local checkout owned by that session.
- **Remove:** the user-facing action that reclaims the archived session's local workspace storage. It does not delete the session record or conversation history.
- **Dependency pruning:** deletion of validated `node_modules` directories while preserving the containing worktree and its source changes.
- **Orphaned storage:** a directory below an AO-owned worktree root that cannot be matched safely to a durable session and valid Git worktree registration.

## What AO cleanup does today

AO currently exposes:

- `ao session cleanup --dry-run` to preview terminated-session cleanup.
- `ao session cleanup --yes` to run it without an interactive confirmation.
- `POST /api/v1/sessions/cleanup`, optionally filtered by project.

The daemon/session manager:

1. Lists session records and considers only terminated sessions.
2. Best-effort stops any remaining runtime handle.
3. Closes a session-scoped shell terminal before removing its workspace.
4. Preserves attachments before teardown.
5. Removes the worktree through the workspace adapter.
6. Reports workspaces as cleaned, already absent, or skipped.

The workspace adapter refuses unsafe removal. In particular:

- Dirty worktrees are preserved.
- Worktrees still in use are deferred.
- Missing or unavailable repositories are reported rather than force-deleted.
- Background reconciliation retries eligible terminal cleanup failures.

Cleanup removes the complete worktree when successful, so `node_modules` inside that worktree is removed automatically. It does not delete the archived session row or its conversation history.

## Current problem

The existing behavior is safe but incomplete as storage management:

1. **Dirty means all-or-nothing.** A small source change prevents removal of the entire worktree, including gigabytes of ignored `node_modules` content.
2. **No per-session archived action.** The Archive UI offers Restore but no direct way to reclaim one session's workspace.
3. **No retention policy.** There is no user-configurable rule that revisits old archived workspaces after a fixed period.
4. **Orphans are invisible.** Directories that no longer map to a valid session/worktree record are outside the current session-cleanup candidate set.
5. **Cleanup reporting is operational, not storage-oriented.** Users cannot see the logical bytes removed, remaining storage, or why a large directory survived.
6. **Logical size differs from physical space.** APFS clones, hard links, and shared objects mean deleting 10 GiB reported by `du` may increase free disk space by much less. AO must not promise exact physical bytes reclaimed.
7. **Recovery is not a visible contract.** AO has preserved-ref machinery, but cleanup does not currently present a user-facing guarantee that a removed archived workspace has a verified recovery point before its files disappear.

The local cleanup that motivated this design demonstrated the gap: `~/.ao` fell from roughly 22 GiB to 11 GiB only after combining whole-worktree removal with dependency pruning in preserved dirty/orphaned worktrees.

## Goals

- Let a user reclaim one archived session's workspace from its card.
- Automatically revisit old archived workspaces after 14 days when enabled.
- Remove complete clean worktrees through the existing daemon/workspace boundary.
- Preserve uncommitted source changes while reclaiming regenerable dependencies.
- Exclude running, non-terminated, pinned, locked, and otherwise unsafe workspaces.
- Surface orphaned storage and cleanup outcomes clearly.
- Keep archived session history and restore capability through a durable recovery ref.

## Non-goals

- Deleting archived session records, conversations, attachments, or provider history.
- Cleaning arbitrary folders outside AO-owned data/worktree roots.
- Force-deleting dirty worktrees.
- Treating a failed runtime probe as proof that a session is inactive.
- Promising an exact physical-space gain on copy-on-write filesystems.
- General-purpose macOS cache cleanup.

## Proposed behavior

### Archived-session Remove action

Each archived session card gains a button labeled **Remove** beside Restore.

Selecting Remove opens a confirmation dialog based on a daemon-provided preview:

- **Clean workspace:** explain that AO will save a recovery point and remove the local worktree, including dependencies and build output, while preserving the session and conversation history.
- **Dirty workspace:** explain that AO cannot remove source files with uncommitted changes and will remove only validated `node_modules` directories.
- **Already removed:** show the card as having no local workspace and do not offer another destructive action.
- **Locked or in use:** disable confirmation and show the reason.

After a successful removal, the card remains in Archive and displays **Workspace removed**. Restore remains available and recreates the workspace using the existing restore path.

### Recovery snapshot and restore contract

AO follows the same core safety idea documented by Codex: removing a managed worktree must not remove the associated session's recoverability.

Before complete worktree removal, the daemon must:

1. Resolve the exact checked-out commit.
2. Create or update a durable AO-owned recovery ref and persist it in `session_worktrees.preserved_ref`.
3. Verify that the ref resolves to the expected commit.
4. Only then invoke the workspace reclaimer.

If any recovery-ref step fails, cleanup is blocked. AO does not delete the worktree and returns `recovery_snapshot_failed`.

Restore recreates the worktree from the preserved ref, then relaunches the session using the existing provider/session metadata. Dependencies such as `node_modules` are regenerable and are not part of the recovery snapshot. A project setup command or explicit package-manager install restores them when needed.

#### Why AO preserves dirty worktrees

A Git recovery ref records a commit; it does not capture the worktree's current uncommitted state. A dirty worktree may contain modified tracked files, staged-but-uncommitted changes, deleted files, or new untracked files that exist nowhere else. Deleting that directory could therefore destroy user work that the recovery ref cannot restore.

AO never removes a dirty worktree completely. It preserves tracked and untracked source files and may prune only validated, regenerable `node_modules` directories. A user who wants complete workspace removal can first commit the changes or save them with a Git stash that explicitly includes any required untracked files, then retry Remove.

Dependency-only pruning does not change tracked or untracked source files.

#### Remove workspace versus permanent session deletion

These actions remain separate because they preserve different recovery guarantees:

- **Remove workspace:** reclaims local workspace storage while retaining the session record, conversation history, provider metadata, attachments, and verified recovery ref. Restore remains supported.
- **Delete session:** would remove the durable session record and history. A Git recovery ref alone cannot reconstruct the deleted conversation or complete session metadata, so Delete cannot promise the same Restore behavior.

Permanent session deletion is outside this design. If it is added later, it should use a separate endpoint, explicit confirmation copy, and a recoverable trash or grace period instead of sharing Remove's endpoint or recovery promise.

### Automatic cleanup policy

Add a Settings toggle:

> Automatically clean archived workspaces after 14 days

The policy is opt-in initially. The daemon is the source of truth for the setting and cleanup schedule; Electron only reads and updates the setting.

The daemon runs the retention sweep at startup and at most once every 24 hours. A session is eligible only when all of the following are true:

- It is terminated/archived.
- It has remained terminated for at least 14 days.
- It is not pinned.
- No runtime, shell terminal, or agent process is confirmed alive for it.
- Its worktree is not locked.
- Its path resolves beneath an AO-owned worktree root.

For eligible sessions:

- Clean worktree: remove the complete worktree through the workspace adapter.
- Dirty worktree: preserve the worktree and source changes, but prune validated `node_modules` directories.
- Missing worktree: record `already_removed` without error.
- Unavailable repository or unverifiable ownership: skip and report.

The existing immediate teardown of clean worktrees on session termination remains unchanged. The 14-day worker handles retained or previously skipped storage; it is not a delay before normal teardown.

### Age tracking

Add a durable session termination timestamp rather than deriving retention from filesystem mtimes or the general `updated_at` column.

- Set `terminated_at` when a session transitions from non-terminated to terminated.
- Clear it when a session is restored.
- Set it again on the next termination.
- Backfill existing terminated rows from their best available durable lifecycle timestamp during migration.

Filesystem mtimes are not authoritative because dependency installs and Git maintenance can change them without representing user activity.

The retention calculation is:

```text
cleanup_eligible_at = terminated_at + 14 days
eligible = automatic_cleanup_enabled
  AND is_terminated
  AND now_utc >= cleanup_eligible_at
  AND NOT is_pinned
```

Runtime, terminal, lock, ownership, dirtiness, and recovery-ref checks still run after the database candidate query and immediately before mutation. A failed or unknown runtime probe never proves that a session is safe to delete.

Persist `last_cleanup_at`, `last_cleanup_outcome`, and the next retry time for blocked/transient outcomes. The daily sweep does not repeatedly rescan dependency trees that were already pruned successfully. Pinning blocks cleanup without resetting `terminated_at`; if the user later unpins an already-eligible session, the next sweep evaluates it immediately.

### Dependency pruning safety

Dependency pruning is deliberately narrow:

- Only directories whose basename is exactly `node_modules`.
- Only beneath a validated AO-owned session worktree.
- Never follow a symlink outside the worktree.
- Refuse paths that fail canonical-path containment checks.
- Skip running, locked, pinned, or non-terminated sessions.
- Record logical bytes scanned and removed when available.

No other build/cache directory is included in the first version.

### Orphaned storage

The daemon scans only configured AO worktree roots and compares directories against durable session metadata and Git worktree registrations.

- A valid, clean Git worktree can be offered for explicit removal.
- A dirty or malformed orphan is reported with its logical size.
- Automatic deletion does not remove an unverifiable orphaned source tree.
- Validated `node_modules` inside an orphan older than 14 days may be offered as a separate dependency-pruning action, but is not silently removed in the initial release.

This keeps orphan handling visible without weakening the existing no-force-delete rule.

## What AO learns from other products

The comparison below reflects current public documentation and, for T3 Code, its public source at commit `ccf220be205f0e509021dbc8cbda90daa638e20d`.

| Product | Observed behavior | AO adopts | AO deliberately does not copy |
| --- | --- | --- | --- |
| [Codex](https://learn.chatgpt.com/docs/environments/git-worktrees) | Keeps the most recent 15 managed worktrees by default, allows the limit or automatic deletion to be changed, protects pinned/in-progress/permanent worktrees, deletes a managed worktree when its chat is archived or the cap is exceeded, and saves a snapshot that can be restored. | Snapshot/recovery ref before removal; protect pinned and running work; keep session history separate from workspace files; keep a visible Restore path. | Immediate deletion merely because a chat is archived. AO retains its lifecycle cleanup and adds the more conservative 14-day fallback policy. |
| [Cursor 3.5+](https://cursor.com/docs/configuration/worktrees) | Runs periodic cleanup, catches up after restart, and keeps a configurable machine-wide maximum (25 by default). It also exposes an explicit `/delete-worktree` action. Cursor's cloud-agent API separates reversible [Archive](https://cursor.com/docs/cloud-agent/api/endpoints.md#archive-an-agent) from irreversible [Delete](https://cursor.com/docs/cloud-agent/api/endpoints.md#delete-an-agent-permanently). | Daemon-owned periodic scheduling with restart catch-up; explicit per-workspace Remove action; clear separation between reversible archive/removal and permanent session deletion. | Treating arbitrary externally-created worktrees as automatic-deletion candidates. AO automatically mutates only AO-owned, durably matched worktrees. A count cap is deferred until age-based cleanup is validated. |
| [T3 Code](https://github.com/pingdotgg/t3code/blob/ccf220be205f0e509021dbc8cbda90daa638e20d/apps/web/src/hooks/useThreadActions.ts) | Settle/archive keeps the conversation and is reversible. Delete permanently clears conversation history. When the deleted thread is the sole owner of a worktree, T3 asks whether to delete that worktree too; shared worktrees are excluded. Its current cleanup call uses forced worktree removal and reports cleanup failure separately after thread deletion. | Detect shared ownership; keep archive/remove/delete as distinct user concepts; report workspace-cleanup failure separately and truthfully. | Forced deletion and coupling worktree cleanup to irreversible conversation deletion. AO never force-deletes dirty worktrees. |

### Product decisions for AO

1. **Adopt now:** durable recovery ref, Remove button, 14-day opt-in sweep, restart catch-up, pinned/running/locked/shared protections, dependency-only pruning for dirty worktrees, and outcome reporting.
2. **Consider later:** an optional maximum managed-worktree count, after age-based cleanup has shipped and its safety data is understood.
3. **Reject:** automatic deletion of arbitrary external worktrees, forced dirty-worktree removal, or permanent session deletion hidden behind the Remove action.

## API changes

Add daemon-owned operations; the frontend must not access the filesystem directly.

### Preview one archived workspace

`GET /api/v1/sessions/{sessionId}/cleanup-preview`

Response includes:

- eligibility and skip reason;
- cleanup mode: `remove_workspace`, `prune_dependencies`, `already_removed`, or `blocked`;
- logical workspace bytes;
- logical dependency bytes;
- dirty, locked, pinned, shared, and in-use indicators;
- recovery-ref status and whether Restore will remain available.

### Remove one archived workspace

`POST /api/v1/sessions/{sessionId}/cleanup`

The daemon revalidates every safety condition at execution time. It never trusts preview results supplied by the client.

Response includes:

- outcome;
- logical bytes removed;
- workspace disposition;
- verified recovery ref for complete workspace removal;
- a stable skip/error code suitable for UI copy.

The existing batch endpoint remains for CLI compatibility. Its implementation should share the same candidate evaluation and execution service as the new per-session endpoint and automatic worker.

### Settings and status

Persist:

- `automaticArchivedWorkspaceCleanupEnabled` (default `false`);
- `terminated_at` on sessions;
- the last completed sweep timestamp;
- per-session `last_cleanup_at`, cleanup outcome, retry time, logical bytes removed, and recovery-ref evidence.

The initial retention period is fixed at 14 days. A configurable duration can be added later if users need it.

## Backend structure

Keep policy and filesystem ownership in the daemon:

1. A cleanup-candidate evaluator reads durable session/worktree facts and produces a reasoned preview.
2. A recovery-ref service creates and verifies the durable restore point before complete workspace teardown.
3. A cleanup executor performs either normal workspace teardown or dependency pruning.
4. Manual single-session, manual batch, and scheduled cleanup call the same evaluator/recovery/executor path.
5. The scheduler invokes the service on startup and daily when enabled, with durable last-run catch-up behavior.
6. SQLite records durable timestamps, settings, outcomes, and recovery evidence; filesystem scans do not become an alternative source of session truth.

The existing `ports.WorkspaceReclaimer` remains the boundary for complete worktree removal. Dependency pruning should be added as an explicit workspace capability rather than implemented in an HTTP controller or Electron.

## Frontend behavior

- Archived cards show **Remove** only when a workspace may still exist.
- Clicking Remove loads the preview and opens a confirmation dialog.
- The dialog states whether the whole workspace or only dependencies will be removed.
- The action shows progress and cannot be submitted twice.
- Success updates cached workspace/session data and reports logical bytes removed.
- A skipped result keeps the button available and displays the reason.
- Settings exposes the automatic 14-day cleanup toggle and summarizes the safety rules.

## Error handling and observability

- Race-safe revalidation immediately before removal.
- Stable reason codes for dirty, pinned, locked, in-use, missing repository, invalid ownership, and cleanup failure.
- Structured logs include session id and outcome but do not expose arbitrary filesystem paths through API errors.
- One failed candidate does not stop a batch or scheduled sweep.
- Scheduled cleanup records its last run and aggregate counts for diagnostics.

## Testing

Backend tests cover:

- 14-day boundary and termination timestamp reset on restore;
- clean, dirty, locked, pinned, in-use, missing, and malformed workspaces;
- shared-worktree ownership exclusion;
- symlink/path-containment attacks;
- recovery-ref creation, verification failure, and restore after removal;
- whole-worktree removal and dependency-only pruning;
- startup/daily scheduling and disabled-by-default behavior;
- per-session and batch endpoint parity;
- restore after workspace removal;
- stable response shapes and OpenAPI generation.

Frontend tests cover:

- Remove button presence and accessible name;
- confirmation copy for full removal versus dependency pruning;
- disabled/blocked states;
- success, skipped, and failure notifications;
- archive card persistence and Restore behavior after cleanup;
- automatic-cleanup setting persistence.

## Rollout

1. Ship the manual per-session Remove action and improved result reporting.
2. Require verified recovery refs before complete worktree removal.
3. Ship the automatic worker behind an opt-in setting, default off.
4. Observe skip/failure categories locally through structured diagnostics.
5. Consider a configurable count cap or enabling age-based cleanup by default only after the safety behavior has been validated in real installations.

## Decisions captured

- The archived-card action is named **Remove**.
- Remove reclaims workspace storage; it does not delete session history.
- Complete removal requires a verified durable recovery ref and retains Restore.
- Clean terminated worktrees may be removed completely.
- Dirty worktrees retain source changes and may have only `node_modules` pruned.
- Automatic cleanup uses a 14-day terminated retention period and is initially opt-in.
- Active, pinned, locked, shared, and unverifiable workspaces are never automatically deleted.
