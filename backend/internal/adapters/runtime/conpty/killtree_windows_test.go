//go:build windows

package conpty

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestKillTreeHelperProcess is not a real test: the tree-kill test below
// re-executes the test binary with -test.run pointing here and
// AO_CONPTY_KILLTREE_CHILD=1 set. The helper spawns a grandchild (ping),
// prints the grandchild PID, then blocks until the tree-kill takes it down.
func TestKillTreeHelperProcess(t *testing.T) {
	if os.Getenv("AO_CONPTY_KILLTREE_CHILD") != "1" {
		t.Skip("killtree helper process only")
	}
	cmd := exec.Command("cmd.exe", "/c", "ping", "-n", "120", "127.0.0.1")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		fmt.Println("START_ERR")
		os.Exit(2)
	}
	fmt.Println(cmd.Process.Pid)
	select {} // block until taskkill /T takes the whole tree down
}

// TestKillProcessTreeKillsChildTree is the regression test for issue #4317:
// killing only the direct child (the supervise wrapper) orphaned the real
// agent. killProcessTree must take the grandchild down with the parent.
func TestKillProcessTreeKillsChildTree(t *testing.T) {
	helper := exec.Command(os.Args[0], "-test.run=^TestKillTreeHelperProcess$")
	helper.Env = append(os.Environ(), "AO_CONPTY_KILLTREE_CHILD=1")
	helper.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := helper.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	helperPID := helper.Process.Pid

	// Read the grandchild PID the helper prints after spawning ping.
	lineCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			lineCh <- strings.TrimSpace(scanner.Text())
			return
		}
		lineCh <- ""
	}()
	var grandchildPID int
	select {
	case line := <-lineCh:
		grandchildPID, err = strconv.Atoi(line)
		if err != nil || grandchildPID <= 0 {
			_ = helper.Process.Kill()
			t.Fatalf("helper reported bad grandchild pid %q (err %v)", line, err)
		}
	case <-time.After(15 * time.Second):
		_ = helper.Process.Kill()
		t.Fatal("timed out waiting for helper to report grandchild pid")
	}
	if !pidAlive(grandchildPID) {
		_ = helper.Process.Kill()
		t.Fatalf("grandchild pid %d not alive before tree-kill", grandchildPID)
	}

	if err := killProcessTree(helperPID); err != nil {
		t.Fatalf("killProcessTree(%d): %v", helperPID, err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for pidAlive(helperPID) || pidAlive(grandchildPID) {
		if time.Now().After(deadline) {
			t.Fatalf("tree-kill incomplete: helper alive=%v grandchild alive=%v",
				pidAlive(helperPID), pidAlive(grandchildPID))
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = helper.Wait()
}
