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
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/processalive"
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
	// BrowserStreamAddress is the private Electron media socket locator. It is
	// authenticated with the same per-launch token as the command runtime.
	BrowserStreamAddress string `json:"browserStreamAddress,omitempty"`
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
func Read(path string) (*Info, error) {
	data, err := os.ReadFile(path)
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
