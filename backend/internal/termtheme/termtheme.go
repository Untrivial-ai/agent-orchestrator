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
// has not already set those keys (project env and explicit tests win).
func Apply(env map[string]string, dataDir string) {
	if env == nil {
		return
	}
	scheme, ok := Read(dataDir)
	if !ok {
		return
	}
	if strings.TrimSpace(env[EnvTheme]) == "" {
		env[EnvTheme] = string(scheme)
	}
	if strings.TrimSpace(env[EnvColorFgBg]) == "" {
		env[EnvColorFgBg] = colorFgBg(scheme)
	}
}

func colorFgBg(scheme Scheme) string {
	if scheme == SchemeLight {
		return "0;15"
	}
	return "15;0"
}
