//go:build !windows

package conpty

import (
	"os"
)

// killProcessTree terminates the process rooted at pid. On Unix the pty
// teardown already reaps the whole session tree (Linux session sweep in
// host_conpty_linux.go, process-group signal in host_conpty_darwin.go), so a
// direct kill here matches the existing behavior: this is the backstop for a
// host that survived graceful shutdown, not the tree reaper itself.
func killProcessTreeOS(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}
