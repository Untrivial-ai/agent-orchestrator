package agentruntime

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

const (
	defaultShutdownGrace = 5 * time.Second
	killWaitGrace        = 2 * time.Second
)

// ProcessConfig controls one agent child process.
type ProcessConfig struct {
	Argv          []string
	Dir           string
	Env           []string
	Stdin         io.Reader
	Stdout        io.Writer
	Stderr        io.Writer
	ShutdownGrace time.Duration
}

// ProcessResult is the terminal result of an agent process.
type ProcessResult struct {
	ExitCode int
	Err      error
}

// Process owns one process group and reaps it exactly once.
type Process struct {
	cmd           *exec.Cmd
	done          chan struct{}
	shutdownGrace time.Duration
	mu            sync.RWMutex
	result        ProcessResult
}

// StartProcess starts an agent process. Cancelling ctx gracefully terminates
// the process group, then kills it after ShutdownGrace.
func StartProcess(ctx context.Context, cfg ProcessConfig) (*Process, error) {
	if len(cfg.Argv) == 0 {
		return nil, errors.New("agentruntime: empty process argv")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	cmd := exec.Command(cfg.Argv[0], cfg.Argv[1:]...) //nolint:gosec // argv is host-owned
	cmd.Dir = cfg.Dir
	cmd.Env = cfg.Env
	cmd.Stdin = cfg.Stdin
	cmd.Stdout = cfg.Stdout
	cmd.Stderr = cfg.Stderr
	configureProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	grace := cfg.ShutdownGrace
	if grace <= 0 {
		grace = defaultShutdownGrace
	}
	process := &Process{cmd: cmd, done: make(chan struct{}), shutdownGrace: grace}
	shutdownContext := context.WithoutCancel(ctx)
	go process.reap()
	go func() {
		select {
		case <-ctx.Done():
			_ = process.Stop(shutdownContext)
		case <-process.done:
		}
	}()
	return process, nil
}

// PID returns the process id, or zero before a process has started.
func (p *Process) PID() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Wait waits for the process to exit or for ctx to end. A wait timeout does not
// stop the process; callers use Stop when cancellation should terminate it.
func (p *Process) Wait(ctx context.Context) (ProcessResult, error) {
	if p == nil {
		return ProcessResult{}, errors.New("agentruntime: nil process")
	}
	select {
	case <-p.done:
		p.mu.RLock()
		defer p.mu.RUnlock()
		return p.result, nil
	case <-ctx.Done():
		return ProcessResult{}, ctx.Err()
	}
}

// Signal sends a signal to the complete process group.
func (p *Process) Signal(signal os.Signal) error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return errors.New("agentruntime: process is not running")
	}
	select {
	case <-p.done:
		return os.ErrProcessDone
	default:
		return signalProcessGroup(p.cmd.Process, signal)
	}
}

// Stop requests graceful termination and waits up to ShutdownGrace before
// killing the process group. Cancelling ctx shortens, but never extends, that
// grace period.
func (p *Process) Stop(ctx context.Context) error {
	if p == nil {
		return errors.New("agentruntime: nil process")
	}
	select {
	case <-p.done:
		return nil
	default:
	}

	_ = p.Signal(terminationSignal())
	timer := time.NewTimer(p.shutdownGrace)
	defer timer.Stop()
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
	case <-timer.C:
	}

	if err := killProcessGroup(p.cmd.Process); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	select {
	case <-p.done:
		return nil
	case <-time.After(killWaitGrace):
		return errors.New("agentruntime: process did not exit after kill")
	}
}

func (p *Process) reap() {
	err := p.cmd.Wait()
	result := ProcessResult{ExitCode: -1, Err: err}
	if p.cmd.ProcessState != nil {
		result.ExitCode = p.cmd.ProcessState.ExitCode()
	}
	p.mu.Lock()
	p.result = result
	p.mu.Unlock()
	close(p.done)
}
