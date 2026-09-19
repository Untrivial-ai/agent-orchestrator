// Package datadirlock guards a daemon's SQLite data directory with an exclusive,
// OS-level advisory file lock.
//
// The lock is scoped to the data directory itself, independent of AO_PORT and
// AO_RUN_FILE, so two daemons pointed at the same AO_DATA_DIR with different
// ports/run files can never both own the database. Without it a second daemon
// can open and hot-migrate SQLite underneath a still-running first daemon,
// invalidating the first daemon's prepared statements against the new schema
// (issue #3716).
//
// Crash recovery is free: the lock lives on the open file descriptor, so the
// kernel releases it automatically when the holder exits — cleanly or by crash.
// A successor therefore reclaims a stale lock with no PID-liveness bookkeeping,
// and the normal single-instance path pays only one non-blocking syscall.
package datadirlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// lockFileName is the advisory-lock file created inside the data directory. It
// carries the holding daemon's PID as plain text purely for diagnostics; the
// lock itself is enforced by the OS, not by the file's contents.
const lockFileName = "daemon.lock"

// ErrLocked is returned by Acquire when another live process already holds the
// data-directory lock. Callers should treat it as "a daemon already owns this
// data dir" and fail fast.
var ErrLocked = errors.New("data directory already locked by another daemon")

// Lock is a held data-directory lock. Release (or the process exiting) frees it.
type Lock struct {
	path string
	file *os.File
}

// Acquire takes the exclusive lock for dataDir without blocking. It returns a
// held *Lock on success. If another process holds the lock it returns an error
// wrapping ErrLocked; the message names the owning PID when it can be read.
//
// Acquire does not create dataDir; callers open the store right after, which
// does. It assumes the directory already exists (the daemon creates it while
// stabilizing its working directory before locking).
func Acquire(dataDir string) (*Lock, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, fmt.Errorf("data dir lock: data dir is required")
	}
	path := filepath.Join(dataDir, lockFileName)
	// O_RDWR (not O_TRUNC): the PID line is rewritten only after the lock is
	// held, so a failed acquire leaves the current owner's PID readable.
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o640)
	if err != nil {
		return nil, fmt.Errorf("data dir lock: open %s: %w", path, err)
	}
	if err := tryLock(file); err != nil {
		holder := readHolderPID(file)
		_ = file.Close()
		if errors.Is(err, ErrLocked) {
			if holder > 0 {
				return nil, fmt.Errorf("%w: %s held by daemon pid %d; stop it before starting another daemon on the same AO_DATA_DIR", ErrLocked, path, holder)
			}
			return nil, fmt.Errorf("%w: %s; stop the daemon that owns this AO_DATA_DIR before starting another", ErrLocked, path)
		}
		return nil, fmt.Errorf("data dir lock: %w", err)
	}
	// The lock is ours: record our PID for anyone who later inspects the file.
	if err := writeHolderPID(file, os.Getpid()); err != nil {
		_ = unlock(file)
		_ = file.Close()
		return nil, fmt.Errorf("data dir lock: record owner: %w", err)
	}
	return &Lock{path: path, file: file}, nil
}

// Release frees the lock. It is safe to call once; a nil or already-released
// Lock is a no-op. Closing the file descriptor drops the OS lock.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unlock(l.file)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		return fmt.Errorf("data dir lock: release: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("data dir lock: close: %w", closeErr)
	}
	return nil
}

// Path returns the lock file's absolute path (for logging/diagnostics).
func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

func writeHolderPID(file *os.File, pid int) error {
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.WriteAt([]byte(strconv.Itoa(pid)+"\n"), 0); err != nil {
		return err
	}
	return file.Sync()
}

// readHolderPID best-effort reads the PID the current owner recorded. Any error
// (empty/partial file during a race) yields 0, which callers treat as unknown.
func readHolderPID(file *os.File) int {
	buf := make([]byte, 32)
	n, err := file.ReadAt(buf, 0)
	if n == 0 && err != nil {
		return 0
	}
	pid, convErr := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if convErr != nil {
		return 0
	}
	return pid
}
