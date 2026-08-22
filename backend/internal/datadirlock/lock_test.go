package datadirlock_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/datadirlock"
)

func TestAcquireExclusive(t *testing.T) {
	dir := t.TempDir()
	first, err := datadirlock.Acquire(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer func() { _ = first.Close() }()

	second, err := datadirlock.Acquire(dir)
	if !errors.Is(err, datadirlock.ErrLocked) {
		t.Fatalf("second acquire err=%v, want ErrLocked", err)
	}
	if second != nil {
		t.Fatal("second lease must be nil")
	}

	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	third, err := datadirlock.Acquire(dir)
	if err != nil {
		t.Fatalf("re-acquire after close: %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatalf("close re-acquired lease: %v", err)
	}
}

func TestAcquireConcurrentOnlyOneSucceeds(t *testing.T) {
	dir := t.TempDir()
	const contenders = 16
	var wins atomic.Int32
	var wg sync.WaitGroup
	leases := make(chan *datadirlock.Lease, contenders)
	wg.Add(contenders)
	for range contenders {
		go func() {
			defer wg.Done()
			lease, err := datadirlock.Acquire(dir)
			if err == nil {
				wins.Add(1)
				leases <- lease
				return
			}
			if !errors.Is(err, datadirlock.ErrLocked) {
				t.Errorf("unexpected err: %v", err)
			}
		}()
	}
	wg.Wait()
	close(leases)
	if wins.Load() != 1 {
		t.Fatalf("winners=%d, want 1", wins.Load())
	}
	for lease := range leases {
		if err := lease.Close(); err != nil {
			t.Errorf("close lease: %v", err)
		}
	}
}

// TestConcurrentSubprocessOneReachesReconcileMarker proves that when two real
// processes contend for one data dir, exactly one reaches the durable-mutation
// marker after acquiring the lease (mirrors boot: lease -> reconcile).
func TestConcurrentSubprocessOneReachesReconcileMarker(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "reconciled.pids")
	loserResult := filepath.Join(dir, "contender.error")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	startChild := func() *exec.Cmd {
		cmd := exec.Command(exe, "-test.run=TestHelperProcessAcquireAndReconcile", "-test.v")
		cmd.Env = append(os.Environ(),
			"DATADIRLOCK_HELPER=1",
			"DATADIRLOCK_DIR="+dir,
			"DATADIRLOCK_MARKER="+marker,
			"DATADIRLOCK_LOSER_RESULT="+loserResult,
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd
	}
	first, second := startChild(), startChild()
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	if err := second.Start(); err != nil {
		_ = first.Process.Kill()
		t.Fatal(err)
	}

	done := make(chan error, 2)
	go func() { done <- first.Wait() }()
	go func() { done <- second.Wait() }()
	var exitErrs int
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				exitErrs++
			}
		case <-time.After(10 * time.Second):
			_ = first.Process.Kill()
			_ = second.Process.Kill()
			t.Fatal("timeout waiting for helper children")
		}
	}
	if exitErrs != 1 {
		t.Fatalf("non-zero exits=%d, want 1 (one locked, one owner)", exitErrs)
	}
	loserErr, err := os.ReadFile(loserResult)
	if err != nil {
		t.Fatalf("read losing contender result: %v", err)
	}
	if !strings.Contains(string(loserErr), datadirlock.ErrLocked.Error()) {
		t.Fatalf("losing contender err=%q, want ErrLocked", loserErr)
	}
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	lines := 0
	for _, b := range raw {
		if b == '\n' {
			lines++
		}
	}
	if lines != 1 {
		t.Fatalf("marker lines=%d content=%q, want exactly one pid line", lines, raw)
	}
	if _, err := strconv.Atoi(string(raw[:len(raw)-1])); err != nil {
		t.Fatalf("marker pid parse: %v (%q)", err, raw)
	}
}

// TestHelperProcessAcquireAndReconcile is the subprocess body for
// TestConcurrentSubprocessOneReachesReconcileMarker, not a standalone test.
func TestHelperProcessAcquireAndReconcile(t *testing.T) {
	if os.Getenv("DATADIRLOCK_HELPER") != "1" {
		t.Skip("helper process")
	}
	dir := os.Getenv("DATADIRLOCK_DIR")
	marker := os.Getenv("DATADIRLOCK_MARKER")
	loserResult := os.Getenv("DATADIRLOCK_LOSER_RESULT")
	lease, err := datadirlock.Acquire(dir)
	if err != nil {
		_ = os.WriteFile(loserResult, []byte(err.Error()), 0o600)
		os.Exit(1)
	}
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		_ = lease.Close()
		os.Exit(2)
	}
	if _, err := f.WriteString(strconv.Itoa(os.Getpid()) + "\n"); err != nil {
		_ = f.Close()
		_ = lease.Close()
		os.Exit(2)
	}
	_ = f.Close()
	// Keep the lease until the peer records its failed acquisition. This avoids
	// relying on scheduler timing while still exercising two real processes.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(loserResult); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) || time.Now().After(deadline) {
			_ = lease.Close()
			os.Exit(2)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = lease.Close()
	os.Exit(0)
}
