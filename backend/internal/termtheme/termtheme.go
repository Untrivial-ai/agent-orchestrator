// Package termtheme injects terminal appearance hints into PTY environments.
//
// Cursor Agent probes OSC 11 (~100ms) and defaults to the wrong prompt colors
// when the reply does not make it back across tmux and the websocket mux. It
// checks TERM_THEME before that probe, so setting it at spawn is the reliable
// fix; COLORFGBG is the older portable hint the same CLIs consult next.
package termtheme

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// FileName is written by the desktop app under AO_DATA_DIR.
	FileName = "terminal-theme"

	// EnvTheme is the PTY variable Cursor Agent reads before its OSC 11 probe.
	EnvTheme = "TERM_THEME"
	// EnvColorFgBg is the portable COLORFGBG hint for terminal appearance.
	EnvColorFgBg = "COLORFGBG"
)

// Scheme is the resolved terminal appearance, never "system".
type Scheme string

const (
	// SchemeDark is a dark terminal canvas.
	SchemeDark Scheme = "dark"
	// SchemeLight is a light terminal canvas.
	SchemeLight Scheme = "light"
)

// Read returns the scheme written by the desktop app. Missing or unreadable
// files return false so callers leave the PTY env alone rather than forcing
// dark onto a light canvas.
func Read(dataDir string) (Scheme, bool) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return "", false
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, FileName))
	if err != nil {
		return "", false
	}
	switch strings.ToLower(strings.TrimSpace(string(raw))) {
	case string(SchemeLight):
		return SchemeLight, true
	case string(SchemeDark):
		return SchemeDark, true
	default:
		return "", false
	}
}

// Apply writes TERM_THEME and COLORFGBG when a scheme is known and the caller
// has not already set those keys (project env and explicit tests win). Keys
// compare case-insensitively on Windows, where the OS treats them that way.
func Apply(env map[string]string, dataDir string) {
	ApplyFoldingKeys(env, dataDir, runtime.GOOS == "windows")
}

// ApplyFoldingKeys is Apply with explicit key-case semantics. With foldKeys, a
// project's `term_theme=dark` counts as already set: adding an uppercase
// TERM_THEME beside it would leave two case variants for Windows to dedupe in
// map order, so the project's value could be lost nondeterministically.
func ApplyFoldingKeys(env map[string]string, dataDir string, foldKeys bool) {
	if env == nil {
		return
	}
	scheme, ok := Read(dataDir)
	if !ok {
		return
	}
	if !hasValue(env, EnvTheme, foldKeys) {
		env[EnvTheme] = string(scheme)
	}
	if !hasValue(env, EnvColorFgBg, foldKeys) {
		env[EnvColorFgBg] = colorFgBg(scheme)
	}
}

// hasValue reports whether env already carries a non-blank key, matching case
// variants too when foldKeys is set. A blank variant is removed so the value
// written in its place is the only one left.
func hasValue(env map[string]string, key string, foldKeys bool) bool {
	if !foldKeys {
		return strings.TrimSpace(env[key]) != ""
	}
	found := false
	for existing, value := range env {
		if !strings.EqualFold(existing, key) {
			continue
		}
		if strings.TrimSpace(value) != "" {
			found = true
			continue
		}
		delete(env, existing)
	}
	return found
}

func colorFgBg(scheme Scheme) string {
	if scheme == SchemeLight {
		return "0;15"
	}
	return "15;0"
}
