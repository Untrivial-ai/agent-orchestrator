package gitworktree

import (
	"context"
	"errors"
	"os"
	"time"
)

// Worktree removal races process exit on every platform. A PTY (or any agent
// child) rooted in the worktree can still hold a handle on that directory for a
// short window AFTER the call that killed it returned — the OS releases handles
// asynchronously during process teardown, and the process is already gone from
// every liveness probe by then. os.RemoveAll then fails transiently: on Windows
// ERROR_SHARING_VIOLATION / ERROR_ACCESS_DENIED ("The process cannot access the
// file because it is being used by another process"); on macOS/Linux an
// unlinkat EACCES / ENOTEMPTY / EBUSY. In every case nothing is meaningfully
// using the directory anymore and a retry a moment later succeeds.
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
	// removeAllRetryEnabled turns on the bounded retry. It defaults on for every
	// platform: the transient race is not Windows-only. On macOS/Linux a
	// just-killed agent child can still hold the worktree dir for a short window
	// after `ao session kill` destroys its shells (the kernel releases handles
	// asynchronously), so os.RemoveAll fails with a transient unlinkat
	// EACCES/ENOTEMPTY/EBUSY and a retry a moment later succeeds. Gating this to
	// Windows stranded worktrees and 500'd the kill on Unix in production. The
	// tradeoff is that a genuinely permanent Unix failure now sleeps out the
	// (ctx-bounded, ~7.25s) budget before returning; that is strictly better than
	// stranding the worktree, and a caller that gives up cancels ctx to stop it.
	// Still a var so tests can drive both the enabled and disabled paths on any
	// platform CI runs.
	removeAllRetryEnabled = true
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
	if !removeAllRetryEnabled {
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
