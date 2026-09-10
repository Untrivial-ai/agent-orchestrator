// Package mobilebridge owns the durable state and helpers for the Connect
// Mobile LAN listener: the ~/.ao/mobile/config.json store and the rotating
// connection password. It has no httpd/daemon dependencies.
package mobilebridge

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// DefaultPort is the LAN listener's default port for the Connect Mobile
// bridge. Distinct from config.DefaultPort (the loopback API port) since the
// two listeners can run concurrently.
const DefaultPort = 3011

// State is the persisted Connect Mobile bridge config in ~/.ao/mobile/config.json.
// Password is stored in plaintext by deliberate decision: it is a low-value,
// rotating LAN enabler that already travels in plaintext over the LAN and is
// shown on the desktop screen, so persisting it (in a 0600 file under ~/.ao)
// lets the desktop redisplay it while the bridge is enabled. The daemon derives
// the auth hash from it in memory (HashPassword) — see BridgeService.
type State struct {
	Enabled  bool   `json:"enabled"`
	Password string `json:"password"`
	LastPort int    `json:"lastPort"`
	// SecurePairing is the opt-in TLS-over-Tailscale mode. Persisted so a daemon
	// restart (backend/internal/daemon/mobile_restore.go, via
	// BridgeService.RestoreOnBoot) knows to re-apply the `tailscale serve` proxy
	// — pointed at whatever port the restarted LAN listener actually bound, not
	// this struct's LastPort, since Start can fall back to an ephemeral port.
	SecurePairing bool `json:"securePairing"`
}

// Path returns the Connect Mobile config file location under the data dir
// (~/.ao/mobile/config.json).
func Path(dataDir string) string { return filepath.Join(dataDir, "mobile", "config.json") }

// Load reads the Connect Mobile config from path. A missing file is not an
// error: it yields the zero State (bridge disabled).
func Load(path string) (State, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return State{}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read mobile config: %w", err)
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, fmt.Errorf("parse mobile config: %w", err)
	}
	return s, nil
}

// Save atomically writes s to path (0600) via a temp file + rename, creating the
// parent dir if needed. State.Password is persisted in plaintext by deliberate,
// documented decision — see the State doc comment.
func Save(path string, s State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir mobile dir: %w", err)
	}
	//nolint:gosec // G117: persisting the rotating LAN password is the deliberate, documented purpose of State (see its doc comment).
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

const pwAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// GeneratePassword returns a fresh 8-character alphanumeric connection password
// drawn from a cryptographically secure source.
func GeneratePassword() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i, b := range buf {
		buf[i] = pwAlphabet[int(b)%len(pwAlphabet)]
	}
	return string(buf), nil
}

// HashPassword returns the hex-encoded SHA-256 of pw, the in-memory auth hash the
// LAN listener validates against (the plaintext is never used for comparison).
func HashPassword(pw string) string {
	sum := sha256.Sum256([]byte(pw))
	return hex.EncodeToString(sum[:])
}

// PasswordMatches reports whether pw hashes to hash, using a constant-time
// comparison to avoid leaking the hash via timing.
func PasswordMatches(hash, pw string) bool {
	return subtle.ConstantTimeCompare([]byte(hash), []byte(HashPassword(pw))) == 1
}
