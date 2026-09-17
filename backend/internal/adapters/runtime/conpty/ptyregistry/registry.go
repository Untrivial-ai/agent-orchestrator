// Package ptyregistry is a sideband JSON list of live Windows pty-host
// processes so ao stop can find and graceful-kill them even when session
// metadata is lost. Ported from agent-orchestrator's windows-pty-registry.ts.
package ptyregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Entry is one registered pty-host process.
type Entry struct {
	SessionID    string `json:"sessionId"`
	PtyHostPID   int    `json:"ptyHostPid"`
	PipePath     string `json:"pipePath"`
	LaunchID     string `json:"launchId,omitempty"`
	RegisteredAt string `json:"registeredAt"` // RFC3339; set by caller
}

// pidAlive is the PID-liveness probe. Tests replace it with a fake.
// defaultPidAlive is provided in build-tagged files (pidalive_unix.go /
// pidalive_windows.go).
var pidAlive = defaultPidAlive

// rewriteRegistry is the prune-write seam. Tests replace it to prove a write
// or permission failure cannot be reported as a complete empty scan.
var rewriteRegistry = writeRaw

// ErrRegistryMalformed indicates that the durable ConPTY registry cannot be parsed safely.
var ErrRegistryMalformed = errors.New("conpty pty registry malformed")

// UnresolvedPipePath marks a durable launch reservation or a child that
// started without reporting a READY address. It is deliberately not dialable.
const UnresolvedPipePath = "ao-conpty://startup-unresolved"

// overrideDir, when set, is the directory the registry file lives in for
// this daemon instance, taking precedence over the ~/.ao default. Set once by
// SetRunFilePath at daemon startup, before any session activity begins, so
// the unsynchronized package var has no concurrent access to race against.
var overrideDir string

// registryMu makes each read-modify-write operation atomic within the daemon.
// Session starts can run concurrently; without this lock two successful hosts
// could race and leave only one recoverable registry entry on disk.
var registryMu sync.Mutex

// SetRunFilePath pins the registry to the directory containing this
// instance's running.json (backend/internal/config's already-resolved,
// absolute Config.RunFilePath). Two AO daemons on one machine — e.g. a
// headless dev daemon and the desktop app, or two dev daemons — normally run
// fully isolated via AO_RUN_FILE/AO_DATA_DIR overrides, but the registry
// ignored that and always resolved to ~/.ao regardless: with the same
// project checked out in both, their independently-numbered session ids
// (e.g. "demo-website-2") could collide, and the second instance's
// registration would silently overwrite the first's pty-host address,
// attaching that session's terminal to the wrong process. Co-locating the
// registry with each instance's own running.json keeps them isolated the
// same way the SQLite store already is. An empty path clears any override,
// reverting to the ~/.ao default.
func SetRunFilePath(path string) {
	if path == "" {
		overrideDir = ""
		return
	}
	overrideDir = filepath.Dir(path)
}

// registryFile resolves the pty-host registry path: overrideDir joined with
// the registry filename when set via SetRunFilePath, otherwise
// ~/.ao/windows-pty-hosts.json via os.UserHomeDir() so t.Setenv("HOME", dir)
// in tests redirects reads/writes to a temp dir.
func registryFile() (string, error) {
	if overrideDir != "" {
		return filepath.Join(overrideDir, "windows-pty-hosts.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ao", "windows-pty-hosts.json"), nil
}

// readRaw reads and strictly parses the registry. A missing file is a complete
// empty snapshot; read and parse failures are incomplete evidence and must
// never be collapsed into absence.
func readRaw(ctx context.Context) ([]Entry, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	path, err := registryFile()
	if err != nil {
		return nil, false, err
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(path)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, false, ctxErr
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, true, nil
		}
		return nil, false, err
	}
	var parsed []json.RawMessage
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, false, fmt.Errorf("%w: %w", ErrRegistryMalformed, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	out := make([]Entry, 0, len(parsed))
	for _, raw := range parsed {
		if err := ctx.Err(); err != nil {
			return out, false, err
		}
		var e Entry
		if err := json.Unmarshal(raw, &e); err != nil {
			return out, false, fmt.Errorf("%w: %w", ErrRegistryMalformed, err)
		}
		if e.SessionID == "" || e.PtyHostPID < 0 || e.PipePath == "" || (e.PtyHostPID == 0 && e.PipePath != UnresolvedPipePath) {
			return out, false, fmt.Errorf("%w: entry has invalid sessionId, ptyHostPid, or pipePath", ErrRegistryMalformed)
		}
		out = append(out, e)
	}
	return out, true, nil
}

// writeRaw atomically writes entries to the registry file. When entries is
// empty it deletes the file instead (mirrors writeRaw in the TS source).

func writeRaw(ctx context.Context, entries []Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := registryFile()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if len(entries) == 0 {
		err := os.Remove(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Atomic write: temp file in same dir then rename (same filesystem).
	tmp, err := os.CreateTemp(dir, "pty-hosts-*.json.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		// Best-effort cleanup of temp file on failure.
		_ = os.Remove(tmpName)
	}()
	if err := ctx.Err(); err != nil {
		_ = tmp.Close()
		return err
	}

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := ctx.Err(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return nil
}

// Register adds or replaces the entry for entry.SessionID. registeredAt must
// be set by the caller (e.g. time.Now().UTC().Format(time.RFC3339)).
func Register(ctx context.Context, entry Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	all, complete, err := scanLocked(ctx)
	if err != nil || !complete {
		return errors.Join(err, errors.New("conpty pty registry scan incomplete"))
	}
	next := make([]Entry, 0)
	for _, e := range all {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.SessionID != entry.SessionID {
			next = append(next, e)
		}
	}
	next = append(next, entry)
	return writeRaw(ctx, next)
}

// Unregister removes the entry for sessionID. No-op if absent.
func Unregister(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	all, complete, err := scanLocked(ctx)
	if err != nil || !complete {
		return errors.Join(err, errors.New("conpty pty registry scan incomplete"))
	}
	next := make([]Entry, 0, len(all))
	for _, e := range all {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.SessionID != sessionID {
			next = append(next, e)
		}
	}
	if len(next) == len(all) {
		return nil // absent, no-op
	}
	return writeRaw(ctx, next)
}

// Scan returns the live registry entries and whether the scan is complete.
// Dead entries are pruned only after a complete read and parse. Any read,
// parse, or prune-write failure returns incomplete evidence.
func Scan(ctx context.Context) (entries []Entry, complete bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	return scanLocked(ctx)
}

func scanLocked(ctx context.Context) (entries []Entry, complete bool, err error) {
	all, complete, err := readRaw(ctx)
	if err != nil || !complete {
		return all, false, err
	}
	live := make([]Entry, 0, len(all))
	for _, e := range all {
		if err := ctx.Err(); err != nil {
			return live, false, err
		}
		if e.PtyHostPID == 0 && e.PipePath == UnresolvedPipePath {
			// A prelaunch reservation has no PID to probe. Retain it until an
			// exact owner replaces or explicitly unregisters the reservation.
			live = append(live, e)
		} else if pidAlive(e.PtyHostPID) {
			live = append(live, e)
		}
		if err := ctx.Err(); err != nil {
			return live, false, err
		}
	}
	if len(live) != len(all) {
		if err := ctx.Err(); err != nil {
			return live, false, err
		}
		if err := rewriteRegistry(ctx, live); err != nil {
			return live, false, err
		}
		if err := ctx.Err(); err != nil {
			return live, false, err
		}
	}
	return live, true, nil
}

// List preserves the ordinary registry consumer API while surfacing every
// incomplete scan as an error.
func List(ctx context.Context) ([]Entry, error) {
	entries, _, err := Scan(ctx)
	return entries, err
}

// Clear deletes the registry file. Best-effort; used by tests and recovery.
func Clear(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeRaw(ctx, nil)
}
