// Package datadirlock provides a daemon-lifetime exclusive ownership lease
// keyed to AO_DATA_DIR. Only one process may hold the lease; it must be
// acquired before opening the SQLite store or reconciling sessions so two
// concurrent starts cannot both mutate the same durable state.
package datadirlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LockFileName is the lease file under the data directory.
const LockFileName = "daemon.lock"

// ErrLocked means another live process holds the data-dir lease.
var ErrLocked = errors.New("datadirlock: data directory already owned by another daemon")

// Lease is an exclusive, process-lifetime lock on a data directory.
// Close releases the lease. The process must keep the Lease alive for the
// entire daemon lifetime (do not close early).
type Lease struct {
	file *os.File
	path string
	pid  int
}

// Path returns the absolute path of the lock file.
func (l *Lease) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// PID returns the process ID written into the lock file (this process).
func (l *Lease) PID() int {
	if l == nil {
		return 0
	}
	return l.pid
}

// Close releases the exclusive lease. Safe to call on a nil Lease.
func (l *Lease) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := unlockAndClose(l.file)
	l.file = nil
	return err
}

// Acquire takes an exclusive non-blocking lease on dataDir.
// On success the caller owns durable mutation rights for that directory until
// Close. On failure with ErrLocked, another process already owns the dir.
//
// dataDir is created if missing (0o750). The lock file is best-effort annotated
// with this process's PID for operator debugging; the OS lock is authoritative.
func Acquire(dataDir string) (*Lease, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return nil, fmt.Errorf("datadirlock: empty data directory")
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("datadirlock: abs %q: %w", dataDir, err)
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, fmt.Errorf("datadirlock: mkdir %q: %w", abs, err)
	}
	path := filepath.Join(abs, LockFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // lock file under AO data dir
	if err != nil {
		return nil, fmt.Errorf("datadirlock: open %q: %w", path, err)
	}
	if err := tryLock(f); err != nil {
		_ = f.Close()
		if errors.Is(err, ErrLocked) {
			// Surface the peer PID when readable (best-effort).
			if peer, readErr := readPeerPID(path); readErr == nil && peer > 0 {
				return nil, fmt.Errorf("%w (path %s, peer pid %d)", ErrLocked, path, peer)
			}
			return nil, fmt.Errorf("%w (path %s)", ErrLocked, path)
		}
		return nil, fmt.Errorf("datadirlock: lock %q: %w", path, err)
	}
	pid := os.Getpid()
	// Truncate and write PID for operators; lock is already held.
	if err := f.Truncate(0); err == nil {
		_, _ = f.Seek(0, 0)
		_, _ = f.WriteString(strconv.Itoa(pid) + "\n")
		_ = f.Sync()
	}
	return &Lease{file: f, path: path, pid: pid}, nil
}

func readPeerPID(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	// First line only.
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strconv.Atoi(strings.TrimSpace(s))
}
