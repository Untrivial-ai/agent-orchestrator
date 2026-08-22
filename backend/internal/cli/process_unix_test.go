//go:build !windows

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const (
	detachedProcessHelperEnv = "AO_TEST_DETACHED_PROCESS_HELPER"
	detachedProcessScriptEnv = "AO_TEST_DETACHED_PROCESS_SCRIPT"
	detachedProcessPIDEnv    = "AO_TEST_DETACHED_PROCESS_PID"
)

func TestStartProcessDetachedReleasedChildSurvivesLauncherExit(t *testing.T) {
	if os.Getenv(detachedProcessHelperEnv) == "1" {
		runDetachedProcessHelper()
		return
	}

	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	scriptPath := filepath.Join(dir, "child.sh")
	script := "#!/bin/sh\nprintf '%s\\n' \"$$\" > \"$1\"\nsleep 30\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestStartProcessDetachedReleasedChildSurvivesLauncherExit$")
	cmd.Env = append(os.Environ(),
		detachedProcessHelperEnv+"=1",
		detachedProcessScriptEnv+"="+scriptPath,
		detachedProcessPIDEnv+"="+pidFile,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("detached process helper failed: %v\n%s", err, out)
	}

	pid := waitForPIDFile(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("detached child is not alive after launcher exit: %v", err)
	}

	parentSID, err := unix.Getsid(os.Getpid())
	if err != nil {
		t.Fatalf("get parent sid: %v", err)
	}
	childSID, err := unix.Getsid(pid)
	if err != nil {
		t.Fatalf("get child sid: %v", err)
	}
	if childSID == parentSID {
		t.Fatalf("detached child stayed in launcher session sid=%d", childSID)
	}
}

func runDetachedProcessHelper() {
	scriptPath := os.Getenv(detachedProcessScriptEnv)
	pidFile := os.Getenv(detachedProcessPIDEnv)
	if scriptPath == "" || pidFile == "" {
		fmt.Fprintln(os.Stderr, "missing detached process helper env")
		os.Exit(2)
	}
	if err := startProcess(processStartConfig{
		Path:         "/bin/sh",
		Args:         []string{scriptPath, pidFile},
		Detach:       true,
		DetachStdio:  true,
		ReleaseAfter: true,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "start detached process: %v\n", err)
		os.Exit(2)
	}
	os.Exit(0)
}

func waitForPIDFile(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if convErr != nil {
				t.Fatalf("parse child pid: %v", convErr)
			}
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for child pid file %s", pidFile)
	return 0
}
