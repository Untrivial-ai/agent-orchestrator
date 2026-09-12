package cursoracp

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// minimumCursorVersion is the oldest Cursor Agent build AO has verified by a
// local ACP v1 handshake and the ACP Registry build current when this binding
// was added.
const minimumCursorVersion = "2026.08.11"

var cursorVersionPattern = regexp.MustCompile(`\b(\d{4})\.(\d{2})\.(\d{2})\b`)

func versionProbe(ctx context.Context, bin string) error {
	// Cursor's standard installer makes ~/.local/bin/cursor-agent a symlink into
	// a versioned directory. Prefer that local fact: invoking --version starts
	// the full Node CLI and can wait on another live Cursor process long enough
	// to exceed a health probe's deadline.
	if resolved, err := filepath.EvalSymlinks(bin); err == nil {
		if installed, ok := parseCursorVersion(resolved); ok {
			return requireMinimumCursorVersion(installed)
		}
	}
	output, err := aoprocess.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("read Cursor Agent version: %w", err)
	}
	installed, ok := parseCursorVersion(string(output))
	if !ok {
		return fmt.Errorf("unrecognized Cursor Agent version %q (AO requires %s or newer)",
			strings.TrimSpace(string(output)), minimumCursorVersion)
	}
	return requireMinimumCursorVersion(installed)
}

func requireMinimumCursorVersion(installed cursorVersion) error {
	minimum, _ := parseCursorVersion(minimumCursorVersion)
	if !installed.less(minimum) {
		return nil
	}
	return fmt.Errorf("cursor-agent %s is older than AO's tested minimum %s",
		installed, minimumCursorVersion)
}

type cursorVersion [3]int

func parseCursorVersion(output string) (cursorVersion, bool) {
	match := cursorVersionPattern.FindStringSubmatch(output)
	if len(match) != 4 {
		return cursorVersion{}, false
	}
	var version cursorVersion
	for i := range version {
		value, err := strconv.Atoi(match[i+1])
		if err != nil {
			return cursorVersion{}, false
		}
		version[i] = value
	}
	return version, true
}

func (v cursorVersion) less(other cursorVersion) bool {
	for i := range v {
		if v[i] != other[i] {
			return v[i] < other[i]
		}
	}
	return false
}

func (v cursorVersion) String() string {
	return fmt.Sprintf("%04d.%02d.%02d", v[0], v[1], v[2])
}
