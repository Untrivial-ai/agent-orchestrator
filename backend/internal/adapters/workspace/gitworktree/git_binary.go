package gitworktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// ErrGitBinaryNotFound is returned when the default "git" binary cannot be
// located on PATH or in any well-known Git install location. Naming the
// problem here means callers see "git executable not found" instead of a raw,
// confusing os/exec ENOENT.
var ErrGitBinaryNotFound = errors.New("gitworktree: git executable not found")

// gitPathLookup abstracts exec.LookPath so tests can force a PATH miss
// without mutating the process's real PATH.
type gitPathLookup func(name string) (string, error)

// gitPathExists abstracts checking whether a fallback candidate path names a
// usable file, so fallback-location tests don't depend on a real Git install
// living at that path on the machine running the tests. It mirrors the
// commandRunner field above: an injectable seam over an OS call so tests
// don't have to hit the real filesystem/environment to exercise this path.
type gitPathExists func(path string) bool

// resolveGitBinary finds the git executable New should use when the caller
// does not supply an explicit Options.Binary override: PATH first (so the
// common case, where git is reachable, behaves exactly as before), then a
// platform's well-known Git install locations if PATH lookup fails.
//
// This mirrors the pre-rewrite TypeScript getGitExecutable() helper
// (@aoagents/ao-core, since removed with the rest of that package) that this
// Go port dropped when the codebase was rewritten: search PATH, fall back to
// common install locations, and produce a clear error naming the real problem
// if nothing is found. Go's os/exec resolves a bare "git" against PATH lazily
// at spawn time with no such fallback, so a process launched with a degraded
// PATH (e.g. a GUI app on Windows that doesn't inherit a shell profile) fails
// every git invocation with a bare ENOENT until this resolves once up front.
//
// The result is resolved once, in New, and stored on the Workspace as
// w.binary, which every command in this package already reuses for its whole
// lifetime — that per-instance reuse is the caching the TS helper did
// explicitly with its own memoized cache.
func resolveGitBinary(lookPath gitPathLookup, exists gitPathExists) (string, error) {
	if path, err := lookPath(defaultGitBinary); err == nil && path != "" {
		return path, nil
	}
	candidates := gitFallbackCandidates()
	for _, candidate := range candidates {
		if candidate != "" && exists(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf(
		"%w: not on PATH and not found in %d well-known install location(s); install Git and ensure it is on PATH, or pass an explicit binary path",
		ErrGitBinaryNotFound, len(candidates),
	)
}

// gitFallbackCandidates lists common Git install locations to probe, in
// order, for the current OS. A base directory that is unavailable (e.g. an
// unset Windows env var) simply contributes no candidates rather than
// erroring, so resolveGitBinary always has a well-defined (possibly empty)
// list to walk.
func gitFallbackCandidates() []string {
	switch runtime.GOOS {
	case "windows":
		return windowsGitCandidates(os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData"))
	case "darwin":
		return []string{
			"/opt/homebrew/bin/git", // Homebrew on Apple Silicon
			"/usr/local/bin/git",    // Homebrew on Intel, and the pre-Homebrew default prefix
			"/usr/bin/git",          // Xcode Command Line Tools
		}
	default:
		return []string{
			"/usr/bin/git",       // standard distro package location
			"/usr/local/bin/git", // locally-built or manually-installed git
			"/bin/git",
		}
	}
}

// windowsGitCandidates returns the well-known locations Git for Windows
// installs to: the official 64- and 32-bit installer's Program Files paths,
// and the per-user installer's location under %LocalAppData%\Programs. Each
// base is passed in explicitly (rather than read here) so this stays a pure,
// directly testable function; gitFallbackCandidates supplies the real
// environment values. A blank base (the env var was unset) contributes no
// candidates.
func windowsGitCandidates(programFiles, programFilesX86, localAppData string) []string {
	var out []string
	for _, base := range []string{programFiles, programFilesX86} {
		if base == "" {
			continue
		}
		out = append(out,
			filepath.Join(base, "Git", "cmd", "git.exe"),
			filepath.Join(base, "Git", "bin", "git.exe"),
		)
	}
	if localAppData != "" {
		out = append(out, filepath.Join(localAppData, "Programs", "Git", "cmd", "git.exe"))
	}
	return out
}

// isRegularFile reports whether path names an existing, non-directory file.
// It is the real gitPathExists New uses outside tests.
func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
