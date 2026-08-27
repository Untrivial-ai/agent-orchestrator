package conpty

import (
	"io"
	"os"
	"os/exec"
	"sync"
)

// maxCapturedStderr bounds how much pty-host stderr we retain for diagnostics.
const maxCapturedStderr = 8192

// boundedBuffer is a thread-safe io.Writer that retains up to max bytes of what
// is written and discards the rest. It always consumes its input, so it is a
// safe stderr sink for the detached pty-host while still keeping a capped copy
// of startup diagnostics.
type boundedBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.max - len(b.buf); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		b.buf = append(b.buf, p[:room]...)
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

type managedHostProcess struct {
	cmd    *exec.Cmd
	stdout *os.File

	closeStdoutOnce sync.Once
	exitC           chan error
}

func startManagedHostProcess(cmd *exec.Cmd, stderr *boundedBuffer) (*managedHostProcess, error) {
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return nil, err
	}

	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	if err := cmd.Start(); err != nil {
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = stderrR.Close()
		_ = stderrW.Close()
		return nil, err
	}

	// The child inherited the pipe writers. Close the parent's copies so the
	// readers observe EOF when the child exits.
	_ = stdoutW.Close()
	_ = stderrW.Close()

	proc := &managedHostProcess{
		cmd:    cmd,
		stdout: stdoutR,
		exitC:  make(chan error, 1),
	}

	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		defer func() { _ = stderrR.Close() }()
		_, _ = io.Copy(stderr, stderrR)
	}()

	go func() {
		err := cmd.Wait()
		proc.closeStdout()
		_ = stderrR.Close()
		<-stderrDone
		proc.exitC <- err
		close(proc.exitC)
	}()

	return proc, nil
}

func (p *managedHostProcess) closeStdout() {
	p.closeStdoutOnce.Do(func() {
		_ = p.stdout.Close()
	})
}

func (p *managedHostProcess) killAndWait() error {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	p.closeStdout()
	return <-p.exitC
}
