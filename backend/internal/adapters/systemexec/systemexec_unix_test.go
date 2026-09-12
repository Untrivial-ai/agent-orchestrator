//go:build !windows

package systemexec

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestRunInstallCancellationKillsProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- (Adapter{}).RunInstall(ctx, ports.InstallCommand{
			Argv: []string{"sh", "-c", "sleep 30 & echo $!; wait"},
		}, &output, &output)
	}()

	childPID := waitForChildPID(t, &output)
	t.Cleanup(func() { _ = syscall.Kill(childPID, syscall.SIGKILL) })
	cancel()
	if err := <-done; err == nil {
		t.Fatal("RunInstall error = nil, want cancellation")
	}
	assertProcessExited(t, childPID, "installer descendant")
}

func TestRunCancellationKillsProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- (Adapter{}).Run(ctx, []string{"sh", "-c", "sleep 30 & echo $!; wait"}, &output, &output)
	}()

	childPID := waitForChildPID(t, &output)
	t.Cleanup(func() { _ = syscall.Kill(childPID, syscall.SIGKILL) })
	cancel()
	if err := <-done; err == nil {
		t.Fatal("Run error = nil, want cancellation")
	}
	assertProcessExited(t, childPID, "command descendant")
}

func waitForChildPID(t *testing.T, output *lockedBuffer) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		value := strings.TrimSpace(output.String())
		if value != "" {
			pid, _ := strconv.Atoi(strings.Fields(value)[0])
			if pid != 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("child PID was not reported")
	return 0
}

func assertProcessExited(t *testing.T, pid int, label string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s %d survived cancellation", label, pid)
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

var _ io.Writer = (*lockedBuffer)(nil)
