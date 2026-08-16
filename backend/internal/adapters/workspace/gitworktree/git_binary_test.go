package gitworktree

import (
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestResolveGitBinaryPrefersPath confirms the common case is unchanged:
// when git is genuinely resolvable via PATH, resolveGitBinary returns that
// path directly and never consults the fallback candidate list.
func TestResolveGitBinaryPrefersPath(t *testing.T) {
	want := filepath.Join("fake", "path", "to", "git")
	lookPath := func(name string) (string, error) {
		if name != defaultGitBinary {
			t.Fatalf("lookPath called with %q, want %q", name, defaultGitBinary)
		}
		return want, nil
	}
	exists := func(path string) bool {
		t.Fatalf("fallback exists(%q) should not be consulted when PATH lookup succeeds", path)
		return false
	}

	got, err := resolveGitBinary(lookPath, exists)
	if err != nil {
		t.Fatalf("resolveGitBinary: %v", err)
	}
	if got != want {
		t.Fatalf("resolveGitBinary() = %q, want %q", got, want)
	}
}

// TestResolveGitBinaryFallsBackWhenPathMisses is the regression test for
// issue #1777: a PATH lookup failure (the ENOENT trigger — a degraded PATH
// that doesn't include git's directory) must not be fatal as long as git
// lives in one of this platform's well-known install locations. lookPath is
// mocked to fail exactly like a real PATH miss; exists is mocked so only the
// platform's last candidate "exists", proving earlier misses are walked past
// rather than the first candidate being blindly trusted.
func TestResolveGitBinaryFallsBackWhenPathMisses(t *testing.T) {
	candidates := gitFallbackCandidates()
	if len(candidates) == 0 {
		t.Skipf("no known fallback candidates for GOOS %q", runtime.GOOS)
	}
	want := candidates[len(candidates)-1]

	lookPath := func(string) (string, error) { return "", exec.ErrNotFound }
	exists := func(path string) bool { return path == want }

	got, err := resolveGitBinary(lookPath, exists)
	if err != nil {
		t.Fatalf("resolveGitBinary: %v", err)
	}
	if got != want {
		t.Fatalf("resolveGitBinary() = %q, want %q", got, want)
	}
}

// TestResolveGitBinaryNotFoundNamesTheProblem confirms that when neither PATH
// nor any fallback location has git, the error clearly names the real
// problem (git missing) instead of leaking a bare, confusing ENOENT.
func TestResolveGitBinaryNotFoundNamesTheProblem(t *testing.T) {
	lookPath := func(string) (string, error) { return "", exec.ErrNotFound }
	exists := func(string) bool { return false }

	_, err := resolveGitBinary(lookPath, exists)
	if !errors.Is(err, ErrGitBinaryNotFound) {
		t.Fatalf("resolveGitBinary() err = %v, want ErrGitBinaryNotFound", err)
	}
	if !strings.Contains(err.Error(), "git executable not found") {
		t.Fatalf("resolveGitBinary() err = %q, want a message naming the missing git executable", err.Error())
	}
}

// TestWindowsGitCandidatesOrderAndContent locks in the well-known Windows
// install locations and their order: both Program Files bases before the
// per-user LocalAppData install, cmd before bin within each base (matching
// how Git for Windows itself orders PATH entries).
func TestWindowsGitCandidatesOrderAndContent(t *testing.T) {
	got := windowsGitCandidates(`C:\Program Files`, `C:\Program Files (x86)`, `C:\Users\tester\AppData\Local`)
	want := []string{
		filepath.Join(`C:\Program Files`, "Git", "cmd", "git.exe"),
		filepath.Join(`C:\Program Files`, "Git", "bin", "git.exe"),
		filepath.Join(`C:\Program Files (x86)`, "Git", "cmd", "git.exe"),
		filepath.Join(`C:\Program Files (x86)`, "Git", "bin", "git.exe"),
		filepath.Join(`C:\Users\tester\AppData\Local`, "Programs", "Git", "cmd", "git.exe"),
	}
	if len(got) != len(want) {
		t.Fatalf("windowsGitCandidates() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidate %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestWindowsGitCandidatesSkipsUnsetBases confirms an unavailable base (an
// unset env var, surfaced as "") contributes no candidates instead of
// producing a malformed path.
func TestWindowsGitCandidatesSkipsUnsetBases(t *testing.T) {
	if got := windowsGitCandidates("", "", ""); len(got) != 0 {
		t.Fatalf("windowsGitCandidates(\"\",\"\",\"\") = %#v, want empty", got)
	}
	got := windowsGitCandidates("", "", `C:\Users\tester\AppData\Local`)
	want := []string{filepath.Join(`C:\Users\tester\AppData\Local`, "Programs", "Git", "cmd", "git.exe")}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("windowsGitCandidates with only localAppData set = %#v, want %#v", got, want)
	}
}

// TestNewResolvesDefaultBinaryFromPATH confirms New's real wiring (not just
// resolveGitBinary in isolation) still resolves git via PATH in the ordinary
// case, where git is genuinely on PATH — true throughout this repo's own
// dev/CI environment.
func TestNewResolvesDefaultBinaryFromPATH(t *testing.T) {
	requireGit(t)
	resolved, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("exec.LookPath(git): %v", err)
	}

	ws, err := New(Options{ManagedRoot: t.TempDir(), RepoResolver: StaticRepoResolver{"proj": "unused"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if ws.binary != resolved {
		t.Fatalf("ws.binary = %q, want %q", ws.binary, resolved)
	}
}

// TestNewHonorsExplicitBinaryOverride confirms Options.Binary remains an
// escape hatch that bypasses resolution entirely — load-bearing for tests
// and callers that already know the exact binary to use.
func TestNewHonorsExplicitBinaryOverride(t *testing.T) {
	ws, err := New(Options{
		Binary:       "custom-git-that-is-not-on-path",
		ManagedRoot:  t.TempDir(),
		RepoResolver: StaticRepoResolver{"proj": "unused"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if ws.binary != "custom-git-that-is-not-on-path" {
		t.Fatalf("ws.binary = %q, want explicit override preserved verbatim", ws.binary)
	}
}

// TestNewSurfacesClearErrorWhenGitUnresolvable drives New's real resolution
// path (exec.LookPath + isRegularFile) end to end into a total miss, by
// forcing PATH and every Windows fallback base to directories that do not
// contain git. This is the scenario the original bug report describes: a
// process whose inherited PATH does not include git's directory. Windows-only
// because the fallback candidates on darwin/linux are fixed absolute system
// paths (not env-driven) that this test cannot safely redirect away from a
// real install on a CI machine that has one.
func TestNewSurfacesClearErrorWhenGitUnresolvable(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("fallback candidates on this GOOS are fixed system paths, not env-driven")
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("ProgramFiles", t.TempDir())
	t.Setenv("ProgramFiles(x86)", t.TempDir())
	t.Setenv("LocalAppData", t.TempDir())

	_, err := New(Options{ManagedRoot: t.TempDir(), RepoResolver: StaticRepoResolver{"proj": "unused"}})
	if !errors.Is(err, ErrGitBinaryNotFound) {
		t.Fatalf("New() err = %v, want ErrGitBinaryNotFound", err)
	}
}
