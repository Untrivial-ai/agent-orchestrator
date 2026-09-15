package conpty

import (
	"os"
	"testing"
)

// TestKillProcessTreeRejectsNonPositivePID guards the tree-kill entry point
// against pid 0/negative, which on Unix would address the whole process group
// (kill(0, ...)) and on Windows is never a valid target.
func TestKillProcessTreeRejectsNonPositivePID(t *testing.T) {
	for _, pid := range []int{0, -1, -99999} {
		if err := killProcessTree(pid); err == nil {
			t.Errorf("killProcessTree(%d) = nil, want an error", pid)
		}
	}
}

// TestKillProcessTreeRefusesOwnPID is a fail-closed guard: a teardown must
// never target its own process (on Windows taskkill /T would suicide the
// daemon), so a self-PID — which can only mean wrong registry evidence — is
// refused and Destroy retains the PID fence instead.
func TestKillProcessTreeRefusesOwnPID(t *testing.T) {
	if err := killProcessTree(os.Getpid()); err == nil {
		t.Error("killProcessTree(self) = nil, want a refusal error")
	}
}
