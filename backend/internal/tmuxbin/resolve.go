// Package tmuxbin resolves the tmux executable shared by the runtime,
// prerequisite gates, and diagnostics.
package tmuxbin

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var errTmuxNotFound = errors.New("tmux executable not found")

// Source identifies where AO found tmux.
type Source string

const (
	// SourceConfigured means AO_TMUX_BINARY selected an explicit executable. The
	// packaged desktop sets this to its bundled resource.
	SourceConfigured Source = "configured"
	// SourceBundled means tmux was found in the packaged desktop resource layout.
	SourceBundled Source = "bundled"
	// SourceSystem means tmux was resolved from PATH.
	SourceSystem Source = "system"
)

// Resolution is the tmux executable AO will use and where it came from.
type Resolution struct {
	Path   string
	Source Source
}

// Resolve applies the production lookup order.
func Resolve() (Resolution, error) {
	return ResolveWith(os.Getenv("AO_TMUX_BINARY"), os.Executable, exec.LookPath)
}

// ResolveWith is Resolve with process lookups injected for callers and tests.
// A configured path is fail-closed: packaged builds must never silently fall
// back to a machine tmux when their bundled resource is missing or broken.
func ResolveWith(configured string, executable func() (string, error), lookPath func(string) (string, error)) (Resolution, error) {
	if configured = strings.TrimSpace(configured); configured != "" {
		path, err := lookPath(configured)
		if err == nil && path != "" {
			return Resolution{Path: path, Source: SourceConfigured}, nil
		}
		if err == nil {
			err = errTmuxNotFound
		}
		return Resolution{}, errors.Join(err, errTmuxNotFound)
	}

	var bundledErr error
	if self, err := executable(); err == nil && self != "" {
		if resolved, resolveErr := filepath.EvalSymlinks(self); resolveErr == nil {
			self = resolved
		}
		if candidate, ok := bundledCandidate(self); ok {
			path, lookupErr := lookPath(candidate)
			if lookupErr == nil && path != "" {
				return Resolution{Path: path, Source: SourceBundled}, nil
			}
			bundledErr = lookupErr
		}
	} else if err != nil {
		bundledErr = err
	}

	path, err := lookPath("tmux")
	if err == nil && path != "" {
		return Resolution{Path: path, Source: SourceSystem}, nil
	}
	if err == nil {
		err = errTmuxNotFound
	}
	return Resolution{}, errors.Join(bundledErr, err, errTmuxNotFound)
}

// bundledCandidate recognizes Electron's resource layout on macOS and Linux:
// resources/daemon/ao and resources/tmux/bin/tmux. Requiring the daemon and
// resources directory names avoids treating unrelated sibling binaries as a
// desktop bundle.
func bundledCandidate(self string) (string, bool) {
	daemonDir := filepath.Dir(self)
	resourcesDir := filepath.Dir(daemonDir)
	if filepath.Base(daemonDir) != "daemon" || !strings.EqualFold(filepath.Base(resourcesDir), "resources") {
		return "", false
	}
	return filepath.Join(resourcesDir, "tmux", "bin", "tmux"), true
}
