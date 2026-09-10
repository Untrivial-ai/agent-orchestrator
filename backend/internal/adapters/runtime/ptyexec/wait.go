package ptyexec

import "time"

// killGrace bounds Close's final wait after the SIGKILL fallback. A process
// wedged in an uninterruptible kernel state (D — e.g. blocked on a dead NFS
// or FUSE mount in the workspace) cannot be reaped even by SIGKILL, and
// cmd.Wait would block forever, stalling daemon shutdown (and session
// teardown, which reaches the same Close via the Spawn ctx-cancellation
// goroutine). After the grace, Close releases the PTY master and returns;
// the Wait goroutine finishes whenever the kernel eventually reaps the
// process.
const killGrace = 2 * time.Second

// waitForExit blocks until done is closed or grace elapses, whichever comes
// first. It is Close's bounded tail: SIGKILL normally reaps the process
// immediately and done closes; the timeout is the escape hatch for a wedged
// process that cannot be killed.
func waitForExit(done <-chan struct{}, grace time.Duration) {
	select {
	case <-done:
	case <-time.After(grace):
	}
}
