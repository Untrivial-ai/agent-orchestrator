package conpty

import (
	"fmt"
	"os"
)

// killProcessTree terminates the process tree rooted at pid. It dispatches to
// the OS-specific implementation, but never targets this process itself: a
// teardown that kills its own PID (taskkill /T is a tree kill) would suicide
// the daemon or test binary. A self-PID here means the registry evidence is
// wrong, so fail closed and let Destroy retain the PID fence.
func killProcessTree(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("conpty: refusing to kill non-positive pid %d", pid)
	}
	if pid == os.Getpid() {
		return fmt.Errorf("conpty: refusing to kill own pid %d", pid)
	}
	return killProcessTreeOS(pid)
}
