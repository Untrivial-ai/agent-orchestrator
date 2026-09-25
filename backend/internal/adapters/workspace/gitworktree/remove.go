package gitworktree

import (
	"context"
	"errors"
	"os"
	"time"
)

var errRemoveRetryExhausted = errors.New("worktree removal retry exhausted")

type removeRetryExhaustedError struct {
	err error
}

func (e removeRetryExhaustedError) Error() string { return e.err.Error() }

func (e removeRetryExhaustedError) Unwrap() []error {
	return []error{errRemoveRetryExhausted, e.err}
}

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
	// removeAllRetryable classifies the narrow OS errors caused by a process
	// handle that has not been released yet. Tests replace it so the retry loop
	// remains covered on every platform CI runs.
	removeAllRetryable = isRetryableRemoveError
	// removeAll is os.RemoveAll in production; tests substitute a stub to drive
	// the retry loop deterministically instead of depending on platform
	// filesystem locking semantics (the real sharing violation only reproduces
	// on Windows).
	removeAll = os.RemoveAll
)

// removeAllWithRetry is os.RemoveAll plus a one-shot permission repair for
// owner-read-only directories (see repairRemovePermissions) and a bounded,
// backing-off retry for the transient Windows sharing violation described
// above. A path that is already gone is success (os.RemoveAll's own
// semantics). Permanent failures are returned immediately; an in-use failure
// that survives the complete retry budget carries errRemoveRetryExhausted
// while preserving the underlying error.
//
// Retries stop early when ctx is done: time.Sleep is uninterruptible, so
// without this a caller that has already given up (client disconnected,
// deadline passed) would still pay out the remaining budget — and with a
// workspace project's repos torn down serially, several of them in a row.
//
// The backoff starts short so the common case (handle released almost
// immediately) costs ~50ms, and grows so a slower release does not burn the
// attempt budget in the first half-second.
func removeAllWithRetry(ctx context.Context, path string) error {
	err := removeAll(path)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	// A permission failure on Unix is not transient — no amount of waiting
	// unlocks a 0500 directory — but it is repairable when AO owns the tree.
	// Repair once and retry once; a second failure is the real answer.
	if isPermissionRemoveError(err) && repairRemovePermissions(path) {
		if err = removeAll(path); err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	if !removeAllRetryable(err) {
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
		if !removeAllRetryable(err) {
			return err
		}
	}
	return removeRetryExhaustedError{err: err}
}
