package opencode

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const v2DataHomeDirName = "opencode-v2-home"

// V2DataHome returns the XDG_DATA_HOME OpenCode 2 runs with: a sibling of the
// user's own data home, so OpenCode 1 and 2 never share a database.
func V2DataHome() (string, bool) {
	parent := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if filepath.Base(parent) == v2DataHomeDirName {
		return parent, true
	}
	if parent == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", false
		}
		parent = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(parent, v2DataHomeDirName), true
}

// V2NPMPrefix is the private npm prefix OpenCode 2 installs into so its
// `opencode` executable never replaces the OpenCode 1 one on PATH.
func V2NPMPrefix() (string, bool) {
	home, ok := V2DataHome()
	if !ok {
		return "", false
	}
	return filepath.Join(home, "npm"), true
}

// V2NPMBinDir is where npm places executables for V2NPMPrefix.
func V2NPMBinDir() (string, bool) {
	prefix, ok := V2NPMPrefix()
	if !ok {
		return "", false
	}
	if runtime.GOOS == "windows" {
		return prefix, true
	}
	return filepath.Join(prefix, "bin"), true
}
