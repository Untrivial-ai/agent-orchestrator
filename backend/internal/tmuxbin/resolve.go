// Package tmuxbin resolves the tmux executable shared by the runtime and
// diagnostics.
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
	// SourceBundled means tmux is packaged in the macOS app's daemon directory.
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

// bundledCandidate recognizes the macOS application layout populated by the
// release pipeline. Restricting bundled provenance to this layout prevents a
// system ao and tmux that merely share a bin directory from being mislabeled.
func bundledCandidate(self string) (string, bool) {
	daemonDir := filepath.Dir(self)
	resourcesDir := filepath.Dir(daemonDir)
	contentsDir := filepath.Dir(resourcesDir)
	appDir := filepath.Dir(contentsDir)
	if filepath.Base(daemonDir) != "daemon" ||
		filepath.Base(resourcesDir) != "Resources" ||
		filepath.Base(contentsDir) != "Contents" ||
		!strings.HasSuffix(strings.ToLower(filepath.Base(appDir)), ".app") {
		return "", false
	}
	return filepath.Join(daemonDir, "tmux"), true
}
