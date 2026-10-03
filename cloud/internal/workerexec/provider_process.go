package workerexec

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Provider CLIs may be Node launchers that spawn native binaries. Stop their
// process group so descendants cannot retain pipes or a conversation writer.
func configureProviderProcess(process *exec.Cmd) {
	process.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	process.WaitDelay = 2 * time.Second
	process.Cancel = func() error { return stopProviderProcess(process) }
}

func stopProviderProcess(process *exec.Cmd) error {
	if process.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-process.Process.Pid, syscall.SIGKILL)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return process.Process.Kill()
}
