// Package tmuxbin resolves the tmux executable shared by the runtime and
// diagnostics.
package tmuxbin

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

var errTmuxNotFound = errors.New("tmux executable not found")

// Source identifies where AO found tmux.
type Source string

const (
	// SourceBundled means tmux is packaged next to the ao executable.
	SourceBundled Source = "bundled"
	// SourceSystem means tmux was resolved from PATH.
	SourceSystem Source = "system"
)

// Resolution is the tmux executable AO will use and where it came from.
type Resolution struct {
	Path   string
	Source Source
}

// Resolve prefers a tmux executable next to the running ao binary, as used by
// the macOS app bundle, then falls back to the user's PATH.
func Resolve() (Resolution, error) {
	return ResolveWith(os.Executable, exec.LookPath)
}

// ResolveWith is Resolve with process lookups injected for CLI and unit tests.
func ResolveWith(executable func() (string, error), lookPath func(string) (string, error)) (Resolution, error) {
	var bundledErr error
	if self, err := executable(); err == nil && self != "" {
		candidate := filepath.Join(filepath.Dir(self), "tmux")
		path, err := lookPath(candidate)
		if err == nil && path != "" {
			return Resolution{Path: path, Source: SourceBundled}, nil
		}
		bundledErr = err
		if bundledErr == nil {
			bundledErr = errTmuxNotFound
		}
	} else {
		bundledErr = err
		if bundledErr == nil {
			bundledErr = errTmuxNotFound
		}
	}

	path, err := lookPath("tmux")
	if err == nil && path != "" {
		return Resolution{Path: path, Source: SourceSystem}, nil
	}
	if err == nil {
		err = errTmuxNotFound
	}
	return Resolution{}, errors.Join(bundledErr, err)
}
