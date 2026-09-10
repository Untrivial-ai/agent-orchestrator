package ptyexec

import (
	"testing"
	"time"
)

// TestWaitForExitBoundsThePostKillWait guards the shutdown-hang escape hatch:
// a process wedged in uninterruptible kernel state survives SIGKILL, and if
// Close blocked on cmd.Wait it would stall daemon shutdown forever. waitForExit
// is the bounded tail — it must return after grace even when done never closes.
func TestWaitForExitBoundsThePostKillWait(t *testing.T) {
	never := make(chan struct{})
	start := time.Now()
	waitForExit(never, 50*time.Millisecond)
	elapsed := time.Since(start)
	if elapsed < 40*time.Millisecond {
		t.Fatalf("waitForExit returned after %v; expected it to wait out the grace", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("waitForExit blocked for %v; expected a bounded return after grace", elapsed)
	}
}

// TestWaitForExitReturnsOnDone: when the process exits, done closes and
// waitForExit returns without waiting out the grace.
func TestWaitForExitReturnsOnDone(t *testing.T) {
	done := make(chan struct{})
	close(done)
	start := time.Now()
	waitForExit(done, 5*time.Second)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("waitForExit took %v on a closed channel; expected an immediate return", elapsed)
	}
}
