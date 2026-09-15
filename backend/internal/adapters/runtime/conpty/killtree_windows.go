//go:build windows

package conpty

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

// killProcessTree terminates the process tree rooted at pid: the pty-host (or
// pty child) plus every descendant. A bare Process.Kill only terminates the
// direct child — on Windows that is the "agent-process supervise" wrapper,
// and the real agent (opencode.exe/claude.exe/...) is orphaned and keeps
// running after teardown (issue #4317). taskkill /T is the Windows equivalent
// of the Unix session/group kill and avoids leaving the agent behind.
//
// A failed taskkill falls back to a direct kill so a missing taskkill binary
// behaves no worse than the old code.
func killProcessTreeOS(pid int) error {
	kill := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	kill.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
	if err := kill.Run(); err != nil {
		proc, findErr := os.FindProcess(pid)
		if findErr != nil {
			return findErr
		}
		return proc.Kill()
	}
	return nil
}
