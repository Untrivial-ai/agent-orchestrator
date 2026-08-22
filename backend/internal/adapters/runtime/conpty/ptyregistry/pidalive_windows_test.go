//go:build windows

package ptyregistry

import (
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestDefaultPidAlive_Windows guards the WaitForSingleObject branch added for
// #3327 / #3080. OpenProcess succeeds for a terminated process whose kernel
// object is still referenced by an open handle, so OpenProcess success alone is
// NOT liveness. We hold a handle to a child, terminate the child, and require
// defaultPidAlive to report it dead despite the lingering handle — the exact
// scenario the old OpenProcess-only probe got wrong.
func TestDefaultPidAlive_Windows(t *testing.T) {
	// ping is present on every supported Windows SKU and blocks ~n seconds.
	cmd := exec.Command("ping", "-n", "60", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	pid := cmd.Process.Pid
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// 1) Live child must read alive.
	if !defaultPidAlive(pid) {
		t.Fatal("defaultPidAlive reported an alive child as dead")
	}

	// 2) Hold our own handle so the terminated kernel object lingers after exit
	//    (the ConPTY scenario that made the old OpenProcess-only probe return
	//    true for a dead PID).
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatalf("open child handle: %v", err)
	}
	defer windows.CloseHandle(h)

	// 3) Terminate, then require defaultPidAlive to report dead within a grace
	//    window even though the lingering handle keeps OpenProcess succeeding.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill child: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !defaultPidAlive(pid) {
			return // fix verified
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("defaultPidAlive still reports the terminated child as alive; WaitForSingleObject check missing?")
}
