package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// stagingRecorderRunner fakes git for the non-empty-workspace clone path. It
// records the working directory of the clone (the staging root) and populates
// the clone destination with a .git dir and a tracked file so the merge and
// origin validation succeed.
type stagingRecorderRunner struct {
	cloneURL   string
	stagingDir string
}

func (r *stagingRecorderRunner) Run(_ context.Context, dir string, _ map[string]string, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "clone" {
		r.stagingDir = dir
		dest := args[len(args)-1]
		if err := os.MkdirAll(filepath.Join(dest, ".git"), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dest, "README.md"), []byte("# repo\n"), 0o644); err != nil {
			return "", err
		}
		return "", nil
	}
	// validateOrigin: `remote get-url origin`
	return r.cloneURL, nil
}

// A workspace the coding agent has already written into (non-empty, no .git)
// must check out even when its parent is the provider's durable root that the
// worker user cannot write to. The fix stages inside the workspace itself, so
// the staging dir must live under the workspace, never under its parent.
func TestCloneIntoNonEmptyWorkspaceStagesInsideWorkspace(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "repository")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	// The agent wrote a file before checkout ran — this is what forces the
	// non-empty (staging) path.
	if err := os.WriteFile(filepath.Join(workspace, ".claude"), []byte("agent"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := &stagingRecorderRunner{cloneURL: "https://github.com/acme/repo.git"}
	if err := PrepareCheckout(context.Background(), runner, workspace,
		CheckoutGrantResponse{CloneURL: "https://github.com/acme/repo.git"}); err != nil {
		t.Fatalf("PrepareCheckout: %v", err)
	}

	if r := runner.stagingDir; !strings.HasPrefix(filepath.Clean(r), filepath.Clean(workspace)+string(filepath.Separator)) {
		t.Fatalf("staging dir %q is not inside the workspace %q (would fail on a durable root the worker cannot write)", r, workspace)
	}
	// The clone's tracked files were merged in and the agent's file preserved.
	if _, err := os.Stat(filepath.Join(workspace, ".git")); err != nil {
		t.Fatalf("cloned .git not merged into workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "README.md")); err != nil {
		t.Fatalf("cloned file not merged into workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".claude")); err != nil {
		t.Fatalf("agent file not preserved: %v", err)
	}
	// The hidden staging dir is cleaned up before returning.
	entries, err := os.ReadDir(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".ao-checkout-") {
			t.Fatalf("staging dir %q was left behind in the workspace", e.Name())
		}
	}
}

func TestEnsureWorkspaceReviewBaseRecordsRemoteMergeBaseOnce(t *testing.T) {
	repo := initReviewBaseRepository(t)
	base := gitOutput(t, repo, "rev-parse", "HEAD")
	gitRun(t, repo, "update-ref", "refs/remotes/origin/main", base)
	gitRun(t, repo, "checkout", "-b", "ao/session")
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("second\n"), 0o644)
	gitRun(t, repo, "add", "README.md")
	gitRun(t, repo, "commit", "-m", "session change")

	if err := EnsureWorkspaceReviewBase(context.Background(), ExecGitRunner{}, repo, "main"); err != nil {
		t.Fatalf("EnsureWorkspaceReviewBase: %v", err)
	}
	if got := gitOutput(t, repo, "rev-parse", WorkspaceReviewBaseRef); got != base {
		t.Fatalf("review base = %s, want %s", got, base)
	}

	gitRun(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	if err := EnsureWorkspaceReviewBase(context.Background(), ExecGitRunner{}, repo, "main"); err != nil {
		t.Fatalf("second EnsureWorkspaceReviewBase: %v", err)
	}
	if got := gitOutput(t, repo, "rev-parse", WorkspaceReviewBaseRef); got != base {
		t.Fatalf("existing review base moved to %s, want %s", got, base)
	}
}

func TestEnsureWorkspaceReviewBaseFallsBackToRootCommit(t *testing.T) {
	repo := initReviewBaseRepository(t)
	root := gitOutput(t, repo, "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("second\n"), 0o644)
	gitRun(t, repo, "add", "README.md")
	gitRun(t, repo, "commit", "-m", "later")

	if err := EnsureWorkspaceReviewBase(context.Background(), ExecGitRunner{}, repo, "missing"); err != nil {
		t.Fatalf("EnsureWorkspaceReviewBase: %v", err)
	}
	if got := gitOutput(t, repo, "rev-parse", WorkspaceReviewBaseRef); got != root {
		t.Fatalf("fallback review base = %s, want root %s", got, root)
	}
}

func TestEnsureWorkspaceReviewBaseUsesRemoteHEADWhenConfiguredBranchIsMissing(t *testing.T) {
	repo := initReviewBaseRepository(t)
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("remote head\n"), 0o644)
	gitRun(t, repo, "add", "README.md")
	gitRun(t, repo, "commit", "-m", "remote head")
	remoteHead := gitOutput(t, repo, "rev-parse", "HEAD")
	gitRun(t, repo, "update-ref", "refs/remotes/origin/master", remoteHead)
	gitRun(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/master")
	gitRun(t, repo, "checkout", "-b", "ao/session")
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("session change\n"), 0o644)
	gitRun(t, repo, "add", "README.md")
	gitRun(t, repo, "commit", "-m", "session change")

	if err := EnsureWorkspaceReviewBase(context.Background(), ExecGitRunner{}, repo, "main"); err != nil {
		t.Fatalf("EnsureWorkspaceReviewBase: %v", err)
	}
	if got := gitOutput(t, repo, "rev-parse", WorkspaceReviewBaseRef); got != remoteHead {
		t.Fatalf("review base = %s, want remote HEAD %s", got, remoteHead)
	}
}

func TestEnsureWorkspaceReviewBaseRepairsLegacyRootFallback(t *testing.T) {
	repo := initReviewBaseRepository(t)
	root := gitOutput(t, repo, "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("remote head\n"), 0o644)
	gitRun(t, repo, "add", "README.md")
	gitRun(t, repo, "commit", "-m", "remote head")
	remoteHead := gitOutput(t, repo, "rev-parse", "HEAD")
	gitRun(t, repo, "update-ref", "refs/remotes/origin/master", remoteHead)
	gitRun(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/master")
	gitRun(t, repo, "update-ref", WorkspaceReviewBaseRef, root)

	if err := EnsureWorkspaceReviewBase(context.Background(), ExecGitRunner{}, repo, "main"); err != nil {
		t.Fatalf("EnsureWorkspaceReviewBase: %v", err)
	}
	if got := gitOutput(t, repo, "rev-parse", WorkspaceReviewBaseRef); got != remoteHead {
		t.Fatalf("repaired review base = %s, want remote HEAD %s", got, remoteHead)
	}
}

// A scratch / no-repo workspace is `git init` with no commit (unborn HEAD).
// EnsureWorkspaceReviewBase must no-op there rather than error, otherwise every
// scratch session's worker aborts in prepareWorkspace across all providers.
func TestEnsureWorkspaceReviewBaseNoOpOnUnbornHEAD(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "main")
	if err := EnsureWorkspaceReviewBase(context.Background(), ExecGitRunner{}, repo, "main"); err != nil {
		t.Fatalf("EnsureWorkspaceReviewBase on an empty repo: %v", err)
	}
	if _, err := (ExecGitRunner{}).Run(
		context.Background(), repo, nil, "rev-parse", "--verify", WorkspaceReviewBaseRef,
	); err == nil {
		t.Fatalf("review base ref must not be created for an empty repo")
	}
}

func initReviewBaseRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "main")
	gitRun(t, repo, "config", "user.name", "AO Test")
	gitRun(t, repo, "config", "user.email", "ao@example.test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "README.md")
	gitRun(t, repo, "commit", "-m", "initial")
	return repo
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}
