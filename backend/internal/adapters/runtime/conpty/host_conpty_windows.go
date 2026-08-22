//go:build windows

package conpty

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	gopty "github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/windows"
)

// conptyConn is the real ptyConn implementation backed by go-pty's ConPty
// (Windows ConPTY API). Only compiled on Windows.
type conptyConn struct {
	pty gopty.ConPty
	cmd *gopty.Cmd

	once     sync.Once
	doneC    chan struct{}
	exitCode int
	exited   bool
	exitMu   sync.Mutex
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

	cmd := conptyCommand(cp, shellCmd, shellArgs)
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

// conptyCommand builds the go-pty Cmd that runs shellCmd on the ConPTY.
//
// Non-batch executables run directly. Batch files (.cmd/.bat) are routed
// through `cmd.exe /S /c "<command line>"` instead: CreateProcess executes
// batch files via cmd.exe, and when the batch path contains spaces (e.g. an
// npm global shim such as C:\Program Files\nodejs\claude.cmd) the program name
// is split at the first space, failing with "'C:\Program' is not recognized".
// Using cmd.exe (resolved via %ComSpec%, which has no spaces) as the
// application name and passing the full command line verbatim through
// SysProcAttr.CmdLine keeps the batch path intact. The /S switch makes cmd.exe
// strip exactly the outer quote pair and run the inner command literally,
// preserving the per-argument quoting windows.ComposeCommandLine applies.
func conptyCommand(cp gopty.ConPty, shellCmd string, shellArgs []string) *gopty.Cmd {
	if !isBatchFile(shellCmd) {
		return cp.Command(shellCmd, shellArgs...)
	}
	inner := windows.ComposeCommandLine(append([]string{shellCmd}, shellArgs...))
	cmd := cp.Command(comspecPath())
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine: `/S /c "` + inner + `"`,
	}
	return cmd
}

// isBatchFile reports whether path has a .cmd or .bat extension, ignoring case.
func isBatchFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cmd", ".bat":
		return true
	default:
		return false
	}
}

// comspecPath returns the absolute path to cmd.exe, preferring %ComSpec% and
// falling back to a PATH lookup. A full path is required: go-pty resolves a
// bare command name relative to the working directory, which would not find
// cmd.exe there.
func comspecPath() string {
	if comspec := os.Getenv("ComSpec"); comspec != "" {
		return comspec
	}
	if path, err := exec.LookPath("cmd.exe"); err == nil {
		return path
	}
	return "cmd.exe"
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
	c.once.Do(func() { close(c.doneC) })
}

func (c *conptyConn) Read(b []byte) (int, error)  { return c.pty.Read(b) }
func (c *conptyConn) Write(b []byte) (int, error) { return c.pty.Write(b) }
func (c *conptyConn) Close() error {
	err := c.pty.Close()
	// Best-effort kill: a child that ignores ConPTY EOF still gets terminated
	// so Done() fires. Mirrors pty.kill() in pty-host.ts.
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	return err
}
func (c *conptyConn) Resize(cols, rows int) error { return c.pty.Resize(cols, rows) }
func (c *conptyConn) Done() <-chan struct{}       { return c.doneC }
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
