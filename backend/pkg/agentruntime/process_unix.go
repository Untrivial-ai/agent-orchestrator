//go:build !windows

package agentruntime

import (
	"os"
	"os/exec"
	"syscall"
)

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalProcessGroup(process *os.Process, signal os.Signal) error {
	native, ok := signal.(syscall.Signal)
	if !ok {
		return process.Signal(signal)
	}
	return syscall.Kill(-process.Pid, native)
}

func killProcessGroup(process *os.Process) error {
	return syscall.Kill(-process.Pid, syscall.SIGKILL)
}

func terminationSignal() os.Signal {
	return syscall.SIGTERM
}
