package acp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/processenv"
)

type process struct {
	stdin       io.WriteCloser
	stdout      io.Reader
	stop        func() error
	terminate   func() error
	reconnected bool
	gate        *gatedReader
	acpState    *persistenthost.ACPState
}

type gatedReader struct {
	inner io.Reader
	ready chan struct{}
	once  sync.Once
}

func newGatedReader(inner io.Reader, open bool) *gatedReader {
	r := &gatedReader{inner: inner, ready: make(chan struct{})}
	if open {
		r.Open()
	}
	return r
}

func (r *gatedReader) Read(dst []byte) (int, error) {
	<-r.ready
	return r.inner.Read(dst)
}

func (r *gatedReader) Open() { r.once.Do(func() { close(r.ready) }) }

type spawnFunc func(Launch, string) (*process, error)

func spawnAgent(launch Launch, workdir string) (*process, error) {
	cmd := exec.Command(launch.Command, launch.Args...)
	cmd.Dir = workdir
	cmd.Env = processenv.Merge(launch.Env)
	configureProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", launch.Command, err)
	}

	// ACP owns stdout. Always drain stderr separately so a verbose adapter cannot
	// fill its OS pipe and deadlock the protocol.
	go func() { _, _ = io.Copy(io.Discard, stderr) }()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var once sync.Once
	return &process{
		stdin:  stdin,
		stdout: stdout,
		stop: func() error {
			var stopErr error
			once.Do(func() {
				_ = stdin.Close()
				select {
				case err := <-done:
					stopErr = processExitError(err)
				case <-time.After(3 * time.Second):
					stopErr = killProcessTree(cmd)
					select {
					case <-done:
					case <-time.After(2 * time.Second):
						if stopErr == nil {
							stopErr = errors.New("ACP process did not exit after kill")
						}
					}
				}
			})
			return stopErr
		},
	}, nil
}

func persistentStop(gate *gatedReader, stdin io.WriteCloser) func() error {
	return func() error {
		gate.Open()
		return stdin.Close()
	}
}

func persistentTerminate(
	gate *gatedReader,
	stdin io.WriteCloser,
	shutdown func(context.Context) error,
) func() error {
	var once sync.Once
	var result error
	return func() error {
		once.Do(func() {
			gate.Open()
			_ = stdin.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result = shutdown(ctx)
		})
		return result
	}
}

func processExitError(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// A provider that exits during shutdown has already released its resources.
		return nil
	}
	return err
}
