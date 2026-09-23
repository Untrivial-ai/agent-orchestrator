// Package hookutil holds small filesystem helpers shared by the agent hook
// installers (claude-code, codex, opencode). It centralizes the atomic-write
// primitive so every adapter writes hook config the same crash-safe way.
package hookutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// GitignoreSentinel marks a workspace .gitignore as AO-managed so
// EnsureWorkspaceGitignore can rewrite its own file idempotently while never
// touching a user- or repo-provided .gitignore at the same path.
const GitignoreSentinel = "# managed by agent-orchestrator: AO hook files stay out of git status"

// CopilotAgentProfileSentinel marks a Copilot custom-agent profile as
// AO-managed so writers and scratch-workspace readers share one ownership
// marker.
const CopilotAgentProfileSentinel = "<!-- managed by agent-orchestrator: copilot agent profile -->"

// EnsureWorkspaceGitignore writes a self-ignoring .gitignore into dir covering
// the named AO-installed files. Hook files land in fresh session worktrees as
// untracked files, and `git worktree remove` (without --force) refuses on ANY
// untracked file — without this ignore, AO's own hook files would make every
// session workspace permanently undeletable. The patterns are anchored to dir
// and name only AO's files, so anything else an agent drops in the same
// directory still counts as dirt and keeps blocking teardown.
//
// A .gitignore at the same path that lacks the sentinel is left untouched and
// the install proceeds: the worktree then simply stays dirty and teardown
// preserves it, which is the safe degradation.
func EnsureWorkspaceGitignore(dir string, names ...string) error {
	path := filepath.Join(dir, ".gitignore")
	existing, err := os.ReadFile(path) //nolint:gosec // path built from caller-owned workspace dir
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err == nil && !strings.Contains(string(existing), GitignoreSentinel) {
		return nil
	}
	var b strings.Builder
	b.WriteString(GitignoreSentinel)
	b.WriteString("\n/.gitignore\n")
	for _, name := range names {
		b.WriteString("/")
		b.WriteString(filepath.ToSlash(name))
		b.WriteString("\n")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := AtomicWriteFile(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// statRegularFile returns the FileInfo for path if it names an existing regular
// file (not a directory). Otherwise it returns nil, false.
func statRegularFile(path string) (os.FileInfo, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return nil, false
	}
	return info, true
}

// FileExists reports whether path names an existing regular file (not a
// directory). It does not check the exec bit — callers that need to verify
// executability should use IsExecutableFile instead.
func FileExists(path string) bool {
	_, ok := statRegularFile(path)
	return ok
}

// IsExecutableFile reports whether path names an existing regular file (not a
// directory) whose owner exec bit is set on Unix. On Windows it behaves
// identically to FileExists because os.Stat does not expose a reliable execute
// permission; Windows candidates carry explicit .cmd/.exe extensions instead.
func IsExecutableFile(path string) bool {
	info, ok := statRegularFile(path)
	if !ok {
		return false
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o100 == 0 {
		return false
	}
	return true
}

// AtomicWriteFile writes data to path via a temp file in the same directory
// followed by a rename, so a crash or signal mid-write can't leave a truncated
// or empty file that the agent then fails to parse (silently disabling hooks).
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ao-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
