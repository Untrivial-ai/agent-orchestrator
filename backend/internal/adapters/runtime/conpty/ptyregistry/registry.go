// Package ptyregistry is a sideband JSON list of live Windows pty-host
// processes so ao stop can find and graceful-kill them even when session
// metadata is lost. Ported from agent-orchestrator's windows-pty-registry.ts.
package ptyregistry

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

// registryFile resolves ~/.ao/windows-pty-hosts.json. Uses os.UserHomeDir()
// so t.Setenv("HOME", dir) in tests redirects reads/writes to a temp dir.
// ponytail: HOME-based resolution; no AO_DATA_DIR override needed here.
func registryFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ao", "windows-pty-hosts.json"), nil
}

// readRaw reads and defensively parses the registry. Missing file or malformed
// JSON both return an empty slice (mirrors readRaw in the TS source).
func readRaw() []Entry {
	path, err := registryFile()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		// Missing file is fine.
		return nil
	}
	var parsed []json.RawMessage
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	out := make([]Entry, 0, len(parsed))
	for _, raw := range parsed {
		var e Entry
		if err := json.Unmarshal(raw, &e); err != nil {
			continue
		}
		// Drop entries missing required fields (mirrors TS filter).
		if e.SessionID == "" || e.PtyHostPID == 0 || e.PipePath == "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// writeRaw atomically writes entries to the registry file. When entries is
// empty it deletes the file instead (mirrors writeRaw in the TS source).
func writeRaw(entries []Entry) error {
	path, err := registryFile()
	if err != nil {
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

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
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

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Register adds or replaces the entry for entry.SessionID. registeredAt must
// be set by the caller (e.g. time.Now().UTC().Format(time.RFC3339)).
func Register(entry Entry) error {
	next := make([]Entry, 0)
	for _, e := range readRaw() {
		if e.SessionID != entry.SessionID {
			next = append(next, e)
		}
	}
	next = append(next, entry)
	return writeRaw(next)
}

// Unregister removes the entry for sessionID. No-op if absent.
func Unregister(sessionID string) error {
	all := readRaw()
	next := make([]Entry, 0, len(all))
	for _, e := range all {
		if e.SessionID != sessionID {
			next = append(next, e)
		}
	}
	if len(next) == len(all) {
		return nil // absent, no-op
	}
	return writeRaw(next)
}

// List returns all entries whose PtyHostPID is still alive, auto-pruning dead
// ones. The file is rewritten if any entries were pruned.
func List() ([]Entry, error) {
	all := readRaw()
	live := make([]Entry, 0, len(all))
	for _, e := range all {
		if pidAlive(e.PtyHostPID) {
			live = append(live, e)
		}
	}
	if len(live) != len(all) {
		if err := writeRaw(live); err != nil {
			return live, err
		}
	}
	return live, nil
}

// Clear deletes the registry file. Best-effort; used by tests and recovery.
func Clear() error {
	return writeRaw(nil)
}
