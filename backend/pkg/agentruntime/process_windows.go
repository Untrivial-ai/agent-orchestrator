//go:build windows

package agentruntime

import (
	"os"
	"os/exec"
)

func configureProcessGroup(_ *exec.Cmd) {}

func signalProcessGroup(process *os.Process, signal os.Signal) error {
	return process.Signal(signal)
}

func killProcessGroup(process *os.Process) error {
	return process.Kill()
}

func terminationSignal() os.Signal {
	return os.Kill
}
