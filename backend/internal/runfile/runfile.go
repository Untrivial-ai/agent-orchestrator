// Package runfile manages running.json — the PID + port handshake the Electron
// main process uses to discover, health-check, and reap the daemon. The daemon
// writes it on startup and removes it on graceful shutdown. On startup the
// daemon also checks for a stale entry left by a crashed predecessor so it can
// fail fast instead of fighting over the port.
package runfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/processalive"
)

// Reading running.json races transient Windows handle state. Right after the
// daemon writes the file (or replaces it via MoveFileEx), an external
// consumer — antivirus real-time scan, async handle release during process
// teardown — can briefly hold it without FILE_SHARE_READ, and os.ReadFile
// fails with ERROR_SHARING_VIOLATION ("The process cannot access the file
// because it is being used by another process"). A retry a moment later
// succeeds; `ao stop` hitting this window is what flaked TestE2E_Lifecycle on
// windows-latest. gitworktree.removeAllWithRetry documents the same class on
// the delete path and solves it the same way.
//
// These are vars, not consts, so tests can drive the loop without sleeping
// for real and can exercise the retry on every platform CI runs.
var (
	// readAttempts × the capped backoff below bounds the extra wait at ~1.4s
	// (25+50+100+200×6 across 10 tries, 9 sleeps). Reads are cheap and the
	// callers (`ao status`, `ao stop`, daemon startup) are interactive paths
	// where a sub-second stall is invisible, while a failed stop is not.
	readAttempts   = 10
	readBackoff    = 25 * time.Millisecond
	readBackoffCap = 200 * time.Millisecond
	// readRetryEnabled gates the retry to the platform whose handle semantics
	// need it. Elsewhere a read error is real and immediate, and sleeping out
	// the budget before returning the identical error only makes every genuine
	// failure slower. A var, not a bare runtime.GOOS check at the call site,
	// so tests exercise the retry loop on every platform.
	readRetryEnabled = runtime.GOOS == "windows"
	// readFile is os.ReadFile in production; tests substitute a stub to drive
	// the retry loop deterministically (the real sharing violation only
	// reproduces on Windows).
	readFile = os.ReadFile
)

// Info is the on-disk handshake payload.
type Info struct {
	// PID is the daemon process id.
	PID int `json:"pid"`
	// Port is the loopback port the daemon bound.
	Port int `json:"port"`
	// StartedAt is when the daemon came up (RFC 3339).
	StartedAt time.Time `json:"startedAt"`
	// Owner records how this daemon was spawned, so the app can decide whether
	// to hold a supervisor link on attach from the daemon's own durable record
	// rather than the current process env. "app" = normal desktop-spawned daemon
	// (re-link on attach); "persistent" = spawned under AO_KEEP_DAEMON, stays
	// alive across app quit and is never re-linked; empty = headless `ao start`
	// daemon, stays persistent across app quit.
	Owner string `json:"owner,omitempty"`
	// AppRunID identifies the desktop launch that supplied the private browser
	// runtime token. A later desktop launch replaces the daemon instead of
	// attaching with a token the daemon cannot know.
	AppRunID string `json:"appRunId,omitempty"`
	// BrowserRuntimeAddress is the exact Unix socket or Windows named-pipe
	// address selected by the backend for this daemon launch. It is a locator,
	// not an authentication secret; the runtime token stays out of this file.
	BrowserRuntimeAddress string `json:"browserRuntimeAddress,omitempty"`
}

// Write atomically writes running.json at path, creating parent directories
// as needed. It writes to a temp file in the same directory and then calls
// atomicReplace — POSIX rename(2) on Unix, MoveFileEx with
// MOVEFILE_REPLACE_EXISTING on Windows — so a reader never observes a
// partial file and a stale running.json from a crashed predecessor is
// overwritten without an intermediate "no file" window.
func Write(path string, info Info) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create run-file dir: %w", err)
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal run-file: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), ".running-*.json")
	if err != nil {
		return fmt.Errorf("create temp run-file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp run-file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp run-file: %w", err)
	}
	if err := atomicReplace(tmpName, path); err != nil {
		return fmt.Errorf("replace run-file: %w", err)
	}
	return nil
}

// Read loads running.json. A missing file returns (nil, nil) — that is the
// normal "no daemon recorded" state, not an error.
//
// On Windows the initial read is retried briefly (see the vars above) to ride
// out the transient sharing violation. The retry decision is deliberately
// unconditional on error identity rather than sniffing for a Windows errno:
// the syscall surface differs across Windows versions and filesystems, and
// every error os.ReadFile can return there is either
// transient-and-worth-retrying or permanent-and-still-an-error after the
// budget. os.ErrNotExist is never retried — a missing run-file is the normal
// stopped state, and sleeping out the budget would slow every `ao status` on
// a stopped daemon for nothing.
func Read(path string) (*Info, error) {
	data, err := readFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) && readRetryEnabled {
		backoff := readBackoff
		for range readAttempts - 1 {
			time.Sleep(backoff)
			if backoff *= 2; backoff > readBackoffCap {
				backoff = readBackoffCap
			}
			if data, err = readFile(path); err == nil || errors.Is(err, os.ErrNotExist) {
				break
			}
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read run-file: %w", err)
	}
	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("parse run-file: %w", err)
	}
	return &info, nil
}

// Remove deletes running.json. A missing file is not an error — graceful
// shutdown should be idempotent.
func Remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove run-file: %w", err)
	}
	return nil
}

// RemoveIfOwned deletes running.json only if it still belongs to ownerPID. This
// prevents a shutting-down daemon from removing a successor's freshly written
// handshake after an overlapping restart.
func RemoveIfOwned(path string, ownerPID int) error {
	info, err := Read(path)
	if err != nil {
		return err
	}
	if info == nil || info.PID != ownerPID {
		return nil
	}
	return Remove(path)
}

// CheckStale inspects an existing run-file before the new daemon binds. It
// returns:
//
//   - (nil, nil)        no run-file, or one left by a dead process (safe to
//     proceed; the caller should overwrite it);
//   - (*Info, nil)      a run-file whose recorded PID is still alive — a live
//     daemon already owns the port, so the caller should fail fast.
//
// A run-file pointing at a dead PID is treated as stale and reported safe; the
// fresh Write will overwrite it.
func CheckStale(path string) (*Info, error) {
	info, err := Read(path)
	if err != nil {
		return nil, err
	}
	if info == nil || info.PID <= 0 {
		return nil, nil
	}
	if processalive.Alive(info.PID) {
		return info, nil
	}
	return nil, nil
}
