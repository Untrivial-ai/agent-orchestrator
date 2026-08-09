package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Retry bounds for a transient teardown failure. The backoff caps the interval;
// a capped attempt COUNT is also enforced so a permanently-failing teardown
// (locked file, corrupt worktree registration) transitions to a terminal
// `failed` disposition instead of retrying forever with no end state.
const maxCleanupAttempts = 5

const (
	cleanupBackoffBase = 30 * time.Second
	cleanupBackoffCap  = 15 * time.Minute
)

// Machine failure codes recorded on session_cleanup_facts.failure_code.
const (
	failRuntimeDestroy      = "runtime_destroy"
	failRuntimeUnresolved   = "runtime_unresolved"
	failWorkspaceRows       = "workspace_rows"
	failWorkspaceDestroy    = "workspace_destroy"
	failReviewerTerminator  = "reviewer_terminator"
	failProjectUnresolvable = "project_unresolvable"
	failTeardown            = "teardown"
)

// cleanupResult is the structured outcome of a single terminal-resource release
// attempt. It records durable facts only — the derived display status is never
// stored (hard rule). A "" disposition means the attempt aborted before it could
// classify the workspace (the accompanying error carries why).
type cleanupResult struct {
	// disposition is the session-level rollup: removed / preserved_dirty /
	// not_applicable, or pending when a transient failure needs a retry.
	disposition domain.WorkspaceDisposition
	// runtimeReleased is true only on a genuine runtime release (not a no-op skip
	// on an empty/unrouted handle).
	runtimeReleased bool
	// workspaceProject is true for a multi-repo workspace-project session.
	workspaceProject bool
	// failureCode is a machine code for the last failure; "" when none.
	failureCode string
}

// releaseTerminalResources is the shared, idempotent release core that Kill,
// bulk Cleanup, and the reconciler's FinalizeTerminalSession all funnel through.
// Ordering is runtime-first: if the runtime cannot be destroyed the workspace is
// left untouched and the error is returned, so a worktree is never removed out
// from under a live process. Workspace teardown continues past a dirty child so
// one dirty worktree cannot strand its clean siblings. It returns a structured
// result plus a hard error for the two fail-closed cases (runtime destroy failed;
// non-dirty workspace teardown failed) — the caller decides whether to surface
// that (Kill) or fold it into a retry fact (the finalizer / Cleanup).
func (m *Manager) releaseTerminalResources(ctx context.Context, rec domain.SessionRecord) (cleanupResult, error) {
	var res cleanupResult
	var runtimeUnresolved bool

	m.stopPreviewBestEffort(ctx, rec.ID)
	m.destroyBrowserBestEffort(ctx, rec.ID)

	// Exactly one controller exists, so exactly one gets torn down. A chat
	// session has no runtime handle; its controller owns an app-server child
	// process, and closing it also settles any turn left in flight so a later
	// read does not show work that is no longer running.
	if domain.NormalizeSessionMode(rec.Mode) == domain.SessionModeChat {
		m.stopChatBestEffort(ctx, rec.ID)
		res.runtimeReleased = true
	} else if handle := runtimeHandle(rec.Metadata); handle.ID != "" {
		if err := m.runtime.Destroy(ctx, handle); err != nil {
			res.failureCode = failRuntimeDestroy
			return res, fmt.Errorf("runtime: %w", err)
		}
		res.runtimeReleased = true
	} else {
		// handle.ID == "": an unrouted / legacy-backlog row. Destroy returns nil
		// for an empty handle too, so treating that as proof of release would
		// silently leave a live-but-unrouted runtime leaked (#1458). Reconstructing
		// the adapter's deterministic session name to probe such a runtime is a
		// cross-adapter seam left as follow-up; until then, fail closed instead of
		// false-succeeding — checked below, once we know there is a workspace to
		// protect (a workspace-less terminal session has nothing to leak into).
		runtimeUnresolved = true
	}

	if err := m.terminateReviewer(ctx, rec.ID, "cancelled by worker session termination"); err != nil {
		res.failureCode = failReviewerTerminator
		return res, fmt.Errorf("reviewer: %w", err)
	}

	rows, workspaceProject, err := m.workspaceProjectRows(ctx, rec)
	if err != nil {
		// Fail closed on unresolved historical rows (mirrors Kill): a workspace
		// whose repo layout can't be reconciled must not be torn down. An archived
		// or unregistered project gets a distinct code so the UI can tell the user
		// to remove the worktree manually.
		res.failureCode = failWorkspaceRows
		if errors.Is(err, ErrProjectNotResolvable) {
			res.failureCode = failProjectUnresolvable
		}
		return res, fmt.Errorf("workspace rows: %w", err)
	}
	res.workspaceProject = workspaceProject
	ws := workspaceInfo(rec)
	if !workspaceProject && ws.Path == "" {
		// No workspace to release (no-worktree / spawn-failed / orchestrator).
		res.disposition = domain.DispositionNotApplicable
		m.cleanupSystemPromptDir(rec.ID)
		return res, nil
	}

	// A resolvable-but-unconfirmed runtime must not let the workspace go: an
	// unrouted runtime could still be live, and deleting its cwd out from under
	// it can't be undone. Fail closed here (pending/retryable) rather than
	// proceeding on the unproven assumption that "no handle" means "no process".
	if runtimeUnresolved {
		res.failureCode = failRuntimeUnresolved
		return res, fmt.Errorf("runtime handle unresolved for %s; refusing to release its workspace", rec.ID)
	}

	// Gate shut any shell terminal scoped to this session BEFORE the worktree
	// goes away: an open shell whose cwd is that directory can otherwise survive
	// the removal (and on Windows can even block it — an open handle on a
	// directory refuses deletion), or a concurrent Open could land a new one in
	// the same race window. A shell that cannot be confirmed closed stops the
	// release here — same shape as a dirty-workspace refusal — rather than
	// letting the worktree disappear out from under it.
	if ws.Path != "" {
		release, err := m.beginShellTerminalTeardown(ctx, rec.ID)
		if err != nil {
			res.disposition = domain.DispositionPreservedDirty
			m.deleteRestoreMarker(ctx, rec.ID)
			m.cleanupSystemPromptDir(rec.ID)
			return res, nil
		}
		if release != nil {
			defer release()
		}
	}

	if workspaceProject {
		out := m.releaseWorkspaceProjectRows(ctx, rows)
		if out.firstErr != nil {
			// A failed non-dirty child must stay retryable even alongside a dirty
			// sibling: surfacing the error (rather than rolling straight to the
			// terminal preserved_dirty disposition) is what lets the caller
			// schedule a retry. The failed row is already marked retry_remove and
			// the dirty row is left untouched either way, so a later retry that
			// only needs to revisit the failed child converges correctly.
			res.failureCode = workspaceFailureCode(out.firstErr)
			return res, fmt.Errorf("workspace: %w", out.firstErr)
		}
		if out.removed > 0 {
			m.cleanupAgentWorkspace(ctx, rec, rec.Metadata.WorkspacePath)
		}
		if out.dirty > 0 {
			res.disposition = domain.DispositionPreservedDirty
		} else {
			res.disposition = domain.DispositionRemoved
		}
		m.cleanupSystemPromptDir(rec.ID)
		return res, nil
	}

	// Single worktree.
	if err := m.workspace.Destroy(ctx, ws); err != nil {
		if errors.Is(err, ports.ErrWorkspaceDirty) {
			res.disposition = domain.DispositionPreservedDirty
			// Drop the restore marker so RestoreAll can't relaunch a terminated
			// session over the preserved worktree; the worktree is left on disk.
			m.deleteRestoreMarker(ctx, rec.ID)
			m.cleanupSystemPromptDir(rec.ID)
			return res, nil
		}
		res.failureCode = workspaceFailureCode(err)
		return res, fmt.Errorf("workspace: %w", err)
	}
	res.disposition = domain.DispositionRemoved
	m.cleanupAgentWorkspace(ctx, rec, ws.Path)
	m.deleteRestoreMarker(ctx, rec.ID)
	m.cleanupSystemPromptDir(rec.ID)
	return res, nil
}

// workspaceFailureCode classifies a non-dirty workspace teardown error: an
// archived/unregistered project (its repo no longer resolves) gets a distinct
// code so the UI can tell the user to remove the worktree by hand; everything
// else is a generic teardown failure.
func workspaceFailureCode(err error) string {
	if errors.Is(err, ErrProjectNotResolvable) {
		return failProjectUnresolvable
	}
	return failWorkspaceDestroy
}

// isTerminalDisposition reports whether a stored disposition is settled for the
// current generation — i.e. auto-retry is done and a finalize can no-op. Pending
// is the only non-terminal state (it is still auto-retrying).
func isTerminalDisposition(d domain.WorkspaceDisposition) bool {
	switch d {
	case domain.DispositionRemoved, domain.DispositionPreservedDirty,
		domain.DispositionFailed, domain.DispositionNotApplicable:
		return true
	default:
		return false
	}
}

// wpOutcome tallies a workspace-project teardown that continues past dirty
// children (#16). Per-repo state lives in session_worktrees.state; the
// facts-table disposition is only the rollup.
type wpOutcome struct {
	removed  int
	dirty    int
	failed   int
	firstErr error // first non-dirty teardown error, for the failure code
}

// releaseWorkspaceProjectRows removes a workspace project's worktrees child-first
// but, unlike the old abort-on-first-dirty destroyWorkspaceProjectRows, continues
// past a dirty child: clean children are removed (state → unavailable), dirty
// children are preserved untouched, and non-dirty failures are marked
// retry_remove. This stops one dirty worktree from permanently stranding its
// clean siblings.
func (m *Manager) releaseWorkspaceProjectRows(ctx context.Context, rows []ports.WorkspaceRepoInfo) wpOutcome {
	var out wpOutcome
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Path == "" {
			continue
		}
		info := workspaceInfoFromRepoInfo(rows[i])
		if err := m.workspace.Destroy(ctx, info); err != nil {
			if errors.Is(err, ports.ErrWorkspaceDirty) {
				// Preserve the dirty child as-is (never force); its row stays
				// non-restorable spawn state, so RestoreAll won't resurrect it.
				out.dirty++
				continue
			}
			if stateErr := m.upsertWorkspaceProjectRowState(ctx, rows[i], "retry_remove"); stateErr != nil && out.firstErr == nil {
				out.firstErr = stateErr
			}
			if out.firstErr == nil {
				out.firstErr = err
			}
			out.failed++
			continue
		}
		if err := m.upsertWorkspaceProjectRowState(ctx, rows[i], "unavailable"); err != nil && out.firstErr == nil {
			out.firstErr = err
		}
		out.removed++
	}
	return out
}

// deleteRestoreMarker best-effort clears a session's shutdown-restore markers.
func (m *Manager) deleteRestoreMarker(ctx context.Context, id domain.SessionID) {
	if err := m.store.DeleteSessionWorktrees(ctx, id); err != nil {
		m.logger.Warn("finalize: delete restore marker failed", "sessionID", id, "error", err)
	}
}

// cleanupBackoff returns the capped-exponential retry interval for the given
// (1-based) attempt number.
func cleanupBackoff(attempt int64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := cleanupBackoffBase
	for i := int64(1); i < attempt; i++ {
		d *= 2
		if d >= cleanupBackoffCap {
			return cleanupBackoffCap
		}
	}
	return d
}

// persistCleanupFacts writes the durable teardown facts for a terminal session,
// stamped with the cleanup generation the release ran under. Retry bookkeeping
// (attempt count, next_attempt_at) is only carried forward within the same
// generation episode: a Restore→Terminate cycle that advanced the counter starts
// a fresh episode. A pending (transient-failure) result schedules a capped
// backoff retry, or transitions to a terminal `failed` disposition once the
// attempt cap is reached.
func (m *Manager) persistCleanupFacts(ctx context.Context, rec domain.SessionRecord, generation int64, res cleanupResult) error {
	now := m.clock()
	prior, hadPrior, err := m.store.GetSessionCleanupFacts(ctx, rec.ID)
	if err != nil {
		return err
	}
	sameEpisode := hadPrior && prior.SessionGeneration == generation

	facts := domain.SessionCleanupRecord{
		SessionID:         rec.ID,
		SessionGeneration: generation,
		FailureCode:       res.failureCode,
		LastAttemptAt:     now,
	}
	switch {
	case res.runtimeReleased:
		facts.RuntimeReleasedAt = now
	case sameEpisode && !prior.RuntimeReleasedAt.IsZero():
		// Once genuinely released this episode, the stamp survives later retries.
		facts.RuntimeReleasedAt = prior.RuntimeReleasedAt
	}

	priorAttempts := int64(0)
	if sameEpisode {
		priorAttempts = prior.AttemptCount
	}
	attempts := priorAttempts + 1
	facts.AttemptCount = attempts

	if res.disposition == domain.DispositionPending {
		if attempts >= maxCleanupAttempts {
			// Exhausted: stop auto-retry. Only a user-triggered retry (PR 3 API)
			// clears this, distinct from a still-auto-retrying pending state.
			facts.WorkspaceDisposition = domain.DispositionFailed
		} else {
			facts.WorkspaceDisposition = domain.DispositionPending
			facts.NextAttemptAt = now.Add(cleanupBackoff(attempts))
		}
	} else {
		facts.WorkspaceDisposition = res.disposition
	}
	return m.store.UpsertSessionCleanupFacts(ctx, facts)
}

// FinalizeTerminalSession converges a terminated session's runtime + workspace
// toward safe release. It is idempotent and safe to call repeatedly: it is the
// reconciler's per-session unit of work and the primitive the per-session
// cleanup API (PR 3) delegates to. It never marks a session terminated — that
// durable intent is written by Kill / the lifecycle reactions — and it never
// releases a restore-pending session (RestoreAll owns those).
func (m *Manager) FinalizeTerminalSession(ctx context.Context, id domain.SessionID) error {
	unlock := m.sessionLocks.lock(id)
	defer unlock()

	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil {
		return fmt.Errorf("finalize %s: %w", id, err)
	}
	if !ok || !rec.IsTerminated {
		// Gone or no longer terminal (a Restore un-terminated it): nothing to do.
		return nil
	}

	// A restorable session_worktrees row means the session is shutdown-saved and
	// owned by RestoreAll; releasing under it would race a restore. Merge/kill
	// terminated sessions keep their spawn-time rows in state "active" (not
	// restorable), so those remain finalize-eligible.
	rows, err := m.store.ListSessionWorktrees(ctx, id)
	if err != nil {
		return fmt.Errorf("finalize %s: %w", id, err)
	}
	if len(restorableWorktreeRows(rows)) > 0 {
		return nil
	}

	generation := rec.CleanupGeneration

	// Idempotent no-op if already finalized for this generation episode. Shared
	// with finalizeOne (bulk Cleanup) so both entry points skip a re-release.
	if facts, ok, err := m.store.GetSessionCleanupFacts(ctx, id); err != nil {
		return fmt.Errorf("finalize %s: %w", id, err)
	} else if ok && facts.SessionGeneration == generation && isTerminalDisposition(facts.WorkspaceDisposition) {
		return nil
	}

	res, releaseErr := m.releaseTerminalResources(ctx, rec)
	if releaseErr != nil {
		// A terminated session's teardown failure is not surfaced to the caller;
		// it becomes a retry fact the periodic sweep re-attempts.
		m.logger.Warn("finalize: release failed; scheduling retry", "sessionID", id, "error", releaseErr)
		res.disposition = domain.DispositionPending
		if res.failureCode == "" {
			res.failureCode = failTeardown
		}
	}

	// Generation guard: if a Restore→Terminate cycle advanced the counter while we
	// released, this result is stale and must not satisfy the newer episode.
	cur, ok, err := m.store.GetSession(ctx, id)
	if err != nil {
		return fmt.Errorf("finalize %s: %w", id, err)
	}
	if !ok || !cur.IsTerminated || cur.CleanupGeneration != generation {
		return nil
	}
	return m.persistCleanupFacts(ctx, rec, generation, res)
}
