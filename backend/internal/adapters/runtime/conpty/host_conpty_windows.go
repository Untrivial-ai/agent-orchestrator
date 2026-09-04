//go:build windows

package conpty

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
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

// killProcessTree taskkills pid's whole process tree. Node/the ConPTY child
// launches its own children (e.g. the "agent-process supervise" wrapper and,
// under that, the actual agent binary), and cmd.Process.Kill() alone
// (TerminateProcess) does not propagate to them. /T walks the tree from pid;
// /F forces it. Mirrors killProcessTree in
// adapters/chatdriver/acp/process_windows.go, the same fix for the same
// class of problem in a sibling package.
func killProcessTree(pid int) error {
	kill := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	kill.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
	return kill.Run()
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
	//
	// killProcessTree, not cmd.Process.Kill(): the ConPTY child spawns its own
	// children (e.g. "agent-process supervise" and, under that, the actual
	// agent binary), and TerminateProcess does not propagate to descendants.
	// Killing only the direct child orphaned the real agent process, which
	// could then hold the session's worktree files open well past the
	// caller's force-remove retry budget (surfaces as "process cannot access
	// the file" when replacing/retiring the session).
	if c.cmd.Process != nil {
		_ = killProcessTree(c.cmd.Process.Pid)
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
