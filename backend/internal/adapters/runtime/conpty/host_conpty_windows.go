//go:build windows

package conpty

import (
	"errors"
	"fmt"
	"os"
	"sync"

	gopty "github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/windows"
)

// conptyConn is the real ptyConn implementation backed by go-pty's ConPty
// (Windows ConPTY API). Only compiled on Windows.
type conptyConn struct {
	pty gopty.ConPty
	cmd *gopty.Cmd

	doneOnce      sync.Once
	doneC         chan struct{}
	consoleMu     sync.RWMutex
	consoleClosed bool
	disposeOnce   sync.Once
	disposeErr    error
	exitCode      int
	exited        bool
	exitMu        sync.Mutex
}

// newConPTY creates a ConPTY session running shellCmd in cwd with shellArgs.
// It starts the process and returns a ptyConn ready for use.
func newConPTY(cwd, shellCmd string, shellArgs []string) (ptyConn, error) {
	// go-pty's New() returns a ConPty on Windows.
	p, err := gopty.New()
	if err != nil {
		return nil, fmt.Errorf("conpty: create pty: %w", err)
	}
	cp, ok := p.(gopty.ConPty)
	if !ok {
		_ = p.Close()
		return nil, fmt.Errorf("conpty: expected ConPty on windows, got %T", p)
	}

	// Set an initial size matching node-pty defaults from pty-host.ts.
	if err := cp.Resize(initialConPTYColumns, initialConPTYRows); err != nil {
		_ = cp.Close()
		return nil, fmt.Errorf("conpty: initial resize: %w", err)
	}

	cmd := cp.Command(shellCmd, shellArgs...)
	cmd.Dir = cwd
	// Inherit parent env so PATH, HOME, etc. are available.
	cmd.Env = os.Environ()

	if err := cmd.Start(); err != nil {
		_ = cp.Close()
		return nil, fmt.Errorf("conpty: start command: %w", err)
	}

	c := &conptyConn{
		pty:   cp,
		cmd:   cmd,
		doneC: make(chan struct{}),
	}

	go c.wait()
	return c, nil
}

func (c *conptyConn) wait() {
	_ = c.cmd.Wait()
	code := 0
	if c.cmd.ProcessState != nil {
		code = c.cmd.ProcessState.ExitCode()
	}
	c.exitMu.Lock()
	c.exitCode = code
	c.exited = true
	c.exitMu.Unlock()
	c.doneOnce.Do(func() { close(c.doneC) })

	// Unlike Unix PTYs, ConPTY can keep its output pipe open after the child
	// exits. Close only the pseudoconsole handle here so buffered output can
	// still drain before Read returns EOF. The shared host then follows the
	// same exit path as macOS and Linux and broadcasts the completed status.
	c.closeConsole()
}

func (c *conptyConn) Read(b []byte) (int, error)  { return c.pty.Read(b) }
func (c *conptyConn) Write(b []byte) (int, error) { return c.pty.Write(b) }
func (c *conptyConn) Close() error {
	c.closeConsole()
	c.disposeOnce.Do(func() {
		c.disposeErr = errors.Join(
			c.pty.InputPipe().Close(),
			c.pty.OutputPipe().Close(),
		)
	})
	// Best-effort kill: a child that ignores ConPTY EOF still gets terminated
	// so Done() fires. Mirrors pty.kill() in pty-host.ts.
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	return c.disposeErr
}

func (c *conptyConn) closeConsole() {
	c.consoleMu.Lock()
	defer c.consoleMu.Unlock()
	if c.consoleClosed {
		return
	}
	windows.ClosePseudoConsole(windows.Handle(c.pty.Fd()))
	c.consoleClosed = true
}
func (c *conptyConn) Resize(cols, rows int) error {
	c.consoleMu.RLock()
	defer c.consoleMu.RUnlock()
	if c.consoleClosed {
		return fmt.Errorf("conpty: resize: %w", os.ErrClosed)
	}
	return c.pty.Resize(cols, rows)
}
func (c *conptyConn) Done() <-chan struct{} { return c.doneC }
func (c *conptyConn) PID() int {
	if c.cmd.Process == nil {
		return 0
	}
	return c.cmd.Process.Pid
}
func (c *conptyConn) ExitCode() (int, bool) {
	c.exitMu.Lock()
	defer c.exitMu.Unlock()
	return c.exitCode, c.exited
}
