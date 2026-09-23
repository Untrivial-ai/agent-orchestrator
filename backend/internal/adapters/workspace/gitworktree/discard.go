package gitworktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Teardown used to unlink the session's directory inline, inside the request
// that asked for it. A worker checkout carries its build output with it:
// node_modules, target/, .venv, language server caches, none of it tracked by
// git and all of it deleted by `git worktree remove`. Measured on this repo's
// own sessions that is ~2s per 60,000 files, so a real session took tens of
// seconds to kill and a large one ran past the daemon's 60s REST timeout: the
// request context was cancelled mid-teardown, the handler answered 500, and the
// session was left half-dead (agent stopped, row still marked alive).
//
// Discarding splits the two halves apart. Moving the directory into a trash
// area next to it is a single rename, so the caller's teardown is done as soon
// as git agrees the worktree is gone; the unlinking itself runs on a background
// goroutine with nobody waiting on it. Nothing observable is deferred: once the
// rename lands, the worktree path is free for reuse and git's registration has
// already been pruned.
const discardedDirName = ".discarded"

// discardedRoot is where discarded worktrees wait to be unlinked. It lives
// inside the managed root so the rename never crosses a filesystem boundary
// (which would silently turn the O(1) move back into a full copy), and is
// dot-prefixed so it cannot collide with a project id.
func (w *Workspace) discardedRoot() string {
	return filepath.Join(w.managedRoot, discardedDirName)
}

// discard moves path into the discard root and schedules its removal in the
// background. It reports false when the move did not happen (the path is gone,
// the rename failed, or the trash directory could not be created), leaving the
// caller to fall back to deleting the path inline.
func (w *Workspace) discard(path string) (string, bool) {
	root := w.discardedRoot()
	if err := os.MkdirAll(root, 0o750); err != nil {
		return "", false
	}
	base := filepath.Base(path)
	for i := range 100 {
		candidate := filepath.Join(root, fmt.Sprintf("%s-%d-%d", base, time.Now().UnixNano(), i))
		if _, err := os.Lstat(candidate); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", false
		}
		if err := os.Rename(path, candidate); err != nil {
			return "", false
		}
		return candidate, true
	}
	return "", false
}

// undiscard moves a discarded directory back to its original path. It is the
// rollback for a teardown that turned out to be refusable after the move. The
// directory must survive a refusal, exactly as it did when git itself declined
// to remove it.
func (w *Workspace) undiscard(discarded, path string) {
	if err := os.Rename(discarded, path); err != nil {
		// Nothing further to try: report the directory's real location rather
		// than leaving the operator to guess where the contents went.
		w.logf("gitworktree: could not restore discarded worktree %q to %q: %v", discarded, path, err)
	}
}

// removeInBackground unlinks a discarded directory off the caller's path. The
// work is tracked so tests (and shutdown) can wait for it; failures are logged
// rather than returned because no caller is left to act on them; a leftover
// under the discard root is swept on the next daemon start.
func (w *Workspace) removeInBackground(path string) {
	w.discards.Add(1)
	go func() {
		defer w.discards.Done()
		if err := removeAllWithRetry(context.Background(), path); err != nil {
			w.logf("gitworktree: could not remove discarded worktree %q: %v", path, err)
		}
	}()
}

// sweepDiscarded removes anything left in the discard root by a previous run.
// A daemon that exits (or is killed) mid-removal would otherwise leak the
// directory forever: nothing else ever looks at this path again.
func (w *Workspace) sweepDiscarded() {
	entries, err := os.ReadDir(w.discardedRoot())
	if err != nil {
		return
	}
	for _, entry := range entries {
		w.removeInBackground(filepath.Join(w.discardedRoot(), entry.Name()))
	}
}

// waitForDiscards blocks until every scheduled background removal has finished.
// Test-only: production callers deliberately do not wait.
func (w *Workspace) waitForDiscards() {
	w.discards.Wait()
}

func (w *Workspace) logf(format string, args ...any) {
	if w.logger == nil {
		return
	}
	w.logger.Warn(fmt.Sprintf(format, args...))
}

// worktreeRegistration reports whether git still has path registered as a
// worktree and whether it is locked. conclusive is false when git could not be
// asked at all, which routes the caller back to the git-driven path instead of
// letting a failed probe look like an answer.
func (w *Workspace) worktreeRegistration(ctx context.Context, repo, path string) (registered, locked, conclusive bool) {
	records, err := w.listRecords(ctx, repo)
	if err != nil {
		return false, false, false
	}
	rec, found := findWorktree(records, path)
	return found, found && rec.Locked, true
}

// worktreeDirty is isDirty with the same "could not ask" distinction as
// worktreeRegistration: an unreadable status is not a clean status. It takes a
// path rather than the worktree's registered location because the fast path
// asks about the directory after it has been moved aside.
func (w *Workspace) worktreeDirty(ctx context.Context, path string) (dirty, conclusive bool) {
	dirty, err := w.isDirty(ctx, path)
	if err != nil {
		return false, false
	}
	return dirty, true
}

// discardWorktree is Destroy's fast path: it applies the same checks git would
// have applied (registered, unlocked, clean), then moves the directory aside
// and prunes the registration instead of waiting for git to walk the tree.
//
// handled=false means "not my business, run the git-driven path": every
// inconclusive probe routes there so git stays the authority on refusals and
// their messages. A refusal discovered after the move restores the directory
// first, because a refused teardown must leave the worktree exactly as it was.
func (w *Workspace) discardWorktree(ctx context.Context, repo, path string) (bool, error) {
	registered, locked, conclusive := w.worktreeRegistration(ctx, repo, path)
	if !conclusive || locked {
		return false, nil
	}
	discarded, moved := w.discard(path)
	if !moved {
		return false, nil
	}
	// Dirtiness is judged AFTER the move, deliberately. Checking first and
	// deleting afterwards leaves a window: teardown stops the agent and gates
	// the session's shells before it gets here, but a stray watcher or
	// background child can still land a file in the worktree between the two,
	// and the delete would then take work the check said was not there. Once
	// the directory has been renamed, the worktree path no longer resolves for
	// anyone, so what git reports here is exactly what gets unlinked. The
	// worktree stays registered until the prune below, which is what keeps
	// `git status` answerable at the new location.
	if registered {
		dirty, conclusive := w.worktreeDirty(ctx, discarded)
		if !conclusive {
			w.undiscard(discarded, path)
			return false, nil
		}
		if dirty {
			w.undiscard(discarded, path)
			return true, fmt.Errorf("gitworktree: refusing to remove %q: %w (worktree has uncommitted changes)", path, ports.ErrWorkspaceDirty)
		}
	}
	if _, err := w.run(ctx, w.binary, worktreePruneArgs(repo)...); err != nil {
		w.undiscard(discarded, path)
		return false, nil //nolint:nilerr // the git-driven path re-runs prune and reports the failure
	}
	// Past this point the registration is gone, so restoring the directory is
	// no longer a safe undo: it would put a de-registered worktree back on disk,
	// and the next teardown would treat it as an unregistered stray and delete
	// it without a dirty check. Nothing is at risk in letting it go instead,
	// because the move only happened after a conclusive clean status: every
	// change in there is already committed on the session branch.
	stillRegistered, _, conclusive := w.worktreeRegistration(ctx, repo, path)
	if !conclusive {
		w.removeInBackground(discarded)
		return true, nil
	}
	if stillRegistered {
		w.undiscard(discarded, path)
		return true, fmt.Errorf("gitworktree: refusing to remove %q: path is still registered after git worktree prune", path)
	}
	w.removeInBackground(discarded)
	return true, nil
}
