package datadirlock

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A single holder acquires the lock; a second acquire on the same data dir is
// rejected with ErrLocked, while a different data dir succeeds independently.
func TestAcquireRejectsSecondHolderSameDataDir(t *testing.T) {
	dir := t.TempDir()

	first, err := Acquire(dir)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	t.Cleanup(func() { _ = first.Release() })

	if _, err := Acquire(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Acquire on same data dir: got %v, want ErrLocked", err)
	}

	// A distinct data dir is unaffected by the first lock.
	other := t.TempDir()
	otherLock, err := Acquire(other)
	if err != nil {
		t.Fatalf("Acquire on different data dir: %v", err)
	}
	if err := otherLock.Release(); err != nil {
		t.Fatalf("release other: %v", err)
	}
}

// Releasing the lock makes the same data dir immediately acquirable again,
// proving the lock is dropped on close rather than leaking for the process life.
func TestReleaseAllowsReacquire(t *testing.T) {
	dir := t.TempDir()

	first, err := Acquire(dir)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	second, err := Acquire(dir)
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("Release second: %v", err)
	}

	// Release is idempotent.
	if err := second.Release(); err != nil {
		t.Fatalf("second Release should be a no-op: %v", err)
	}
}

// The rejection error names the owning PID so operators can find and stop it.
func TestLockedErrorReportsHolderPID(t *testing.T) {
	dir := t.TempDir()

	first, err := Acquire(dir)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	t.Cleanup(func() { _ = first.Release() })

	_, err = Acquire(dir)
	if err == nil {
		t.Fatal("expected second Acquire to fail")
	}
	if !strings.Contains(err.Error(), strconv.Itoa(os.Getpid())) {
		t.Fatalf("error should name holder pid %d: %v", os.Getpid(), err)
	}
}

// A lock file left behind by a crashed holder (file present, no live flock) must
// be reclaimable: the OS drops flock on process exit, so acquiring against a
// pre-existing but unlocked file succeeds. Simulated here by writing the lock
// file directly with no live descriptor holding it.
func TestReclaimsStaleLockFile(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, lockFileName)
	if err := os.WriteFile(stale, []byte("999999\n"), 0o640); err != nil {
		t.Fatalf("seed stale lock file: %v", err)
	}

	lock, err := Acquire(dir)
	if err != nil {
		t.Fatalf("Acquire over stale lock file: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

// Missing data dir surfaces an error rather than silently succeeding.
func TestAcquireRequiresDataDir(t *testing.T) {
	if _, err := Acquire(""); err == nil {
		t.Fatal("expected error for empty data dir")
	}
}
