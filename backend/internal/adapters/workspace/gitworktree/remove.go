package gitworktree

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Worktree removal races process exit on Windows. A PTY (or any agent child)
// rooted in the worktree can still hold a handle on that directory for a short
// window AFTER the call that killed it returned — the OS releases handles
// asynchronously during process teardown, and the process is already gone from
// every liveness probe by then. os.RemoveAll fails with
// ERROR_SHARING_VIOLATION / ERROR_ACCESS_DENIED ("The process cannot access the
// file because it is being used by another process"), even though nothing is
// meaningfully using the directory anymore and a retry a moment later succeeds.
//
// This is reachable from `ao session kill` on a session whose scoped shell
// terminals were just destroyed: Session Manager closes the shells, then
// removes the worktree immediately, inside the same request. Measured release
// latency ran past a second with two shells open, so the budget below is
// deliberately generous — a kill that takes a few extra seconds is strictly
// better than one that 500s and strands the worktree.
//
// These are vars, not consts, so tests can drive the loop without sleeping for
// real.
var (
	// removeAllAttempts × the capped backoff below bounds the total wait at
	// 7250ms: 50+100+200+400+500×13 across 18 tries (17 sleeps). Sized from
	// measured behaviour — a two-shell session was observed still holding its
	// worktree past the 5s mark — with headroom on top, since the alternative
	// to waiting is a failed kill that strands the worktree. Finite so a
	// genuinely wedged directory still surfaces as an error rather than
	// hanging the request forever.
	//
	// This budget is per PATH, and Session Manager removes a workspace
	// project's repos one at a time while holding that session's shell-terminal
	// gate, so N repos can serialize into N × this. Keep it comfortably under
	// shellterm's sessionGateWaitTimeout ÷ a realistic repo count, or a slow
	// teardown starts turning concurrent opens into gate-busy 409s. Honouring
	// ctx below is what keeps that bounded in practice: a caller that gives up
	// stops the whole chain rather than paying every repo's budget in turn.
	removeAllAttempts   = 18
	removeAllBackoff    = 50 * time.Millisecond
	removeAllBackoffCap = 500 * time.Millisecond
	// removeAllRetryEnabled gates the retry to the platform whose handle
	// semantics need it. Elsewhere a failure from os.RemoveAll is real and
	// immediate, and sleeping out the budget before returning the identical
	// error only makes every genuine failure slower. A var, not a bare
	// runtime.GOOS check at the call site, so tests exercise the retry loop on
	// every platform CI runs — otherwise the coverage below would silently
	// evaporate everywhere but Windows.
	removeAllRetryEnabled = runtime.GOOS == "windows"
	// removeAll is os.RemoveAll in production; tests substitute a stub to drive
	// the retry loop deterministically instead of depending on platform
	// filesystem locking semantics (the real sharing violation only reproduces
	// on Windows).
	removeAll = os.RemoveAll
)

// removeAllWithRetry is os.RemoveAll plus a bounded, backing-off retry for the
// transient Windows sharing violation described above. A path that is already
// gone is success (os.RemoveAll's own semantics), and the last error is
// returned unwrapped so callers can still match on it.
//
// Off Windows, a failed os.RemoveAll is real and immediate — except for one
// case this function repairs first: os.RemoveAll cannot delete a tree that
// contains owner-owned read-only directories (0o500/0o000). Unlinking an entry
// inside such a directory fails with EACCES/EPERM no matter how many times you
// retry the identical call, and renv-style worktrees do contain them (the
// Linux cleanup failure in issue #3807). So after a failure we walk the
// remaining tree with Lstat semantics — symlinks are leaves, never followed
// and never chmodded, since chmod on a symlink path would modify a target that
// lives outside the tree — restore owner write+exec on every real directory,
// then retry once. On Windows, os.RemoveAll clears read-only attributes
// itself, so the repair is skipped there and the retry budget, backoff, and
// ctx handling below are byte-for-byte unchanged.
//
// Retries stop early when ctx is done: time.Sleep is uninterruptible, so
// without this a caller that has already given up (client disconnected,
// deadline passed) would still pay out the remaining budget — and with a
// workspace project's repos torn down serially, several of them in a row.
//
// Within the retry the decision is deliberately unconditional on error
// identity rather than sniffing for a Windows errno: the syscall surface
// differs across Windows versions and filesystems (and Wine/CI shims), and
// every error os.RemoveAll can return there is either
// transient-and-worth-retrying or permanent-and-still-an-error after the
// budget. The backoff starts short so the common case (handle released almost
// immediately) costs ~50ms, and grows so a slower release does not burn the
// attempt budget in the first half-second.
func removeAllWithRetry(ctx context.Context, path string) error {
	err := removeAll(path)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}

	// Permission repair is an off-Windows concern: on Windows the retry loop
	// below owns the failure mode and os.RemoveAll already copes with
	// read-only attributes, so the repair path would only burn an attempt of
	// the budget. Gate on Lstat first so a stub-removal failure on an already
	// gone path (the retry-loop tests) still returns after a single attempt.
	if !removeAllRetryEnabled {
		if !errors.Is(err, fs.ErrPermission) {
			return err
		}

		if _, statErr := os.Lstat(path); statErr != nil {
			return err
		}

		if repairErr := makeTreeWritable(path); repairErr != nil {
			return err
		}

		if err = removeAll(path); err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}

		return err
	}

	timer := time.NewTimer(0)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	backoff := removeAllBackoff
	for range removeAllAttempts - 1 {
		timer.Reset(backoff)
		select {
		case <-ctx.Done():
			return err
		case <-timer.C:
		}
		if backoff *= 2; backoff > removeAllBackoffCap {
			backoff = removeAllBackoffCap
		}

		if err = removeAll(path); err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	return err
}

// makeTreeWritable walks path using Lstat semantics so symlinks are always
// leaves: never followed and never chmodded (chmod on a symlink path would
// modify the target, which lives outside the tree and must survive with its
// permissions untouched). Every real directory gets owner read/write/execute
// restored — write so unlink/rmdir can succeed, read+execute so even a 0o000
// directory can be listed on the way down. Modes are repaired before
// descending so each level is traversable when its children are visited. The
// tree is about to be deleted, so the repaired modes only need to last until
// the next removeAll.
func makeTreeWritable(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		// Symlinks are leaves, and regular files unlink with write on the
		// parent directory alone — neither needs repair.
		return nil
	}
	if err := os.Chmod(path, info.Mode().Perm()|0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := makeTreeWritable(filepath.Join(path, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
