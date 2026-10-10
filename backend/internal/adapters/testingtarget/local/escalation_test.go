package local

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestCleanupEscalatesOnlyOwnedUnchangedProcesses(t *testing.T) {
	f := fixture(t)
	killed := map[int]bool{}
	f.a.ops.signal = func(pid int, signal syscall.Signal) error {
		if signal == syscall.SIGKILL {
			killed[pid] = true
			delete(f.pids, pid)
		}
		return nil // owned processes ignore SIGTERM
	}
	result, err := f.a.Stop(context.Background(), f.s.target)
	if err != nil || result.State != domain.TestCleanupComplete || len(killed) != 3 || killed[99] {
		t.Fatal("fenced escalation failed", result, killed, err)
	}
	if _, exists := f.pids[99]; !exists {
		t.Fatal("unowned process was removed")
	}
}

func TestCleanupRefusesEscalationAfterBirthTimeChanges(t *testing.T) {
	f := fixture(t)
	killed := false
	f.a.ops.signal = func(pid int, signal syscall.Signal) error {
		if signal == syscall.SIGKILL {
			killed = true
		}
		if pid == 11 {
			f.times[pid] = f.times[pid].Add(time.Microsecond)
		} else {
			delete(f.pids, pid)
		}
		return nil
	}
	result, err := f.a.Stop(context.Background(), f.s.target)
	if err == nil || result.State != domain.TestCleanupFailed || killed {
		t.Fatal("changed process was escalated", result, err)
	}
}
